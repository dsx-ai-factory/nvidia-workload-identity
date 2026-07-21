package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-jose/go-jose/v4"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	keyTopicPrefix       = "dsx-identity-keys/v1/pub-keys/shard/"
	keyTopicWildcard     = keyTopicPrefix + "+/kid/+"
	mqttQoS              = byte(1)
	mqttOperationTimeout = 10 * time.Second
	mqttConnectRetry     = time.Second
	syncBackoffMin       = 100 * time.Millisecond
	syncBackoffMax       = 2 * time.Second
	mqttAuthOAuth2       = "oauth2"
	mqttAuthBasic        = "basic"
	mqttAuthAnonymous    = "anonymous"
)

// keySync owns one replica's MQTT client and publication worker. Close stops both.
type keySync struct {
	client      mqtt.Client
	stop        context.CancelFunc
	closeOnce   sync.Once
	replication keyReplication
	priming     keyCachePriming
	subscribed  atomic.Bool
	subscribing atomic.Bool
	// Capacity one records a pending publication without blocking OnConnect.
	publishRequests chan struct{}
}

type keyReplication struct {
	registry *keyRegistry
	shardID  string
	local    []jose.JSONWebKey
	leaseTTL time.Duration
}

type keyCachePriming struct {
	// Paho's ordered message handler is the sole owner of unobservedLocalKIDs.
	startedAt           time.Time
	unobservedLocalKIDs map[string]struct{}
	completed           chan struct{}
}

type mqttAuthConfig struct {
	mode            string
	credentialsFile string
	oauthTokenURL   string
	oauthScopes     []string
	oauthUsername   string
}

type mqttOAuthTokenSource struct {
	ctx             context.Context
	config          clientcredentials.Config
	credentialsFile string
}

func (s mqttOAuthTokenSource) Token() (*oauth2.Token, error) {
	credentials, err := readMQTTCredentials(s.credentialsFile, mqttAuthOAuth2)
	if err != nil {
		return nil, err
	}
	config := s.config
	config.ClientID = credentials.ClientID
	config.ClientSecret = credentials.ClientSecret
	return config.Token(s.ctx)
}

func newKeyCachePriming(keys []jose.JSONWebKey) keyCachePriming {
	unobservedLocalKIDs := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		unobservedLocalKIDs[key.KeyID] = struct{}{}
	}
	return keyCachePriming{
		// published_at uses Unix seconds on the wire. Match that precision so a
		// publication from the startup second is not mistaken for retained history.
		startedAt:           time.Now().Truncate(time.Second),
		unobservedLocalKIDs: unobservedLocalKIDs,
		completed:           make(chan struct{}),
	}
}

func (p *keyCachePriming) observePublication(publication publishedKey) bool {
	// The startup timestamp is a best-effort boundary between retained history
	// and publications observed after this process began connecting.
	if len(p.unobservedLocalKIDs) == 0 || publication.publishedAt.Before(p.startedAt) {
		return false
	}
	delete(p.unobservedLocalKIDs, publication.key.KeyID)
	if len(p.unobservedLocalKIDs) != 0 {
		return false
	}
	close(p.completed)
	return true
}

func (p *keyCachePriming) isComplete() bool {
	select {
	case <-p.completed:
		return true
	default:
		return false
	}
}

func startKeySync(ctx context.Context, brokerURL, clientID, shardID string, keys []jose.JSONWebKey, leaseTTL time.Duration, registry *keyRegistry, auth mqttAuthConfig) (*keySync, error) {
	if brokerURL == "" {
		return nil, fmt.Errorf("MQTT broker URL is required")
	}
	if clientID == "" {
		return nil, fmt.Errorf("MQTT client ID is required")
	}
	if err := validateShardID(shardID); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("at least one public key is required")
	}
	if leaseTTL <= 0 {
		return nil, fmt.Errorf("public key publication lease TTL must be positive")
	}
	for i := range keys {
		if _, err := validateSigningKey(&keys[i], true); err != nil {
			return nil, fmt.Errorf("validate local public key %d: %w", i, err)
		}
	}
	replication := keyReplication{
		registry: registry,
		shardID:  shardID,
		local:    slices.Clone(keys),
		leaseTTL: leaseTTL,
	}

	syncContext, cancel := context.WithCancel(ctx)
	syncer := &keySync{
		stop:            cancel,
		replication:     replication,
		priming:         newKeyCachePriming(keys),
		publishRequests: make(chan struct{}, 1),
	}
	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(clientID).
		SetConnectTimeout(mqttOperationTimeout).
		SetConnectRetry(true).
		SetConnectRetryInterval(mqttConnectRetry).
		SetMaxReconnectInterval(5 * time.Second).
		SetOrderMatters(true)
	provider, err := auth.credentialsProvider(syncContext)
	if err != nil {
		syncer.Close()
		return nil, err
	}
	if provider != nil {
		opts.SetCredentialsProvider(provider)
	}
	opts.OnConnect = func(mqtt.Client) {
		syncer.connectionOpened(syncContext)
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		syncer.subscribed.Store(false)
		slog.Warn("MQTT key synchronization disconnected", "error", err)
	}
	syncer.client = mqtt.NewClient(opts)
	go syncer.run(syncContext)
	syncer.client.Connect()

	return syncer, nil
}

func (s *keySync) Close() {
	// Context cancellation stops the worker. Disconnect stops Paho's independent
	// connection and reconnect loops. Close may be called again while unwinding
	// startup errors.
	s.closeOnce.Do(func() {
		s.stop()
		if s.client != nil {
			s.client.Disconnect(250)
		}
	})
}

func (c mqttAuthConfig) credentialsProvider(ctx context.Context) (mqtt.CredentialsProvider, error) {
	switch c.mode {
	case mqttAuthOAuth2:
		return c.oauthCredentialsProvider(ctx)
	case mqttAuthBasic:
		return c.basicCredentialsProvider()
	case mqttAuthAnonymous:
		return nil, nil
	default:
		return nil, fmt.Errorf("MQTT auth mode %q must be oauth2, basic, or anonymous", c.mode)
	}
}

func (c mqttAuthConfig) oauthCredentialsProvider(ctx context.Context) (mqtt.CredentialsProvider, error) {
	if c.oauthTokenURL == "" || c.oauthUsername == "" || c.credentialsFile == "" {
		return nil, fmt.Errorf("MQTT OAuth2 token URL, username, and credentials file are required")
	}
	// Fail startup if the mounted credentials are missing or invalid.
	if _, err := readMQTTCredentials(c.credentialsFile, mqttAuthOAuth2); err != nil {
		return nil, err
	}
	config := clientcredentials.Config{
		TokenURL: c.oauthTokenURL,
		Scopes:   c.oauthScopes,
	}
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   10 * time.Second,
	})
	// Reuse valid tokens, but reread both mounted credentials when a refresh is required.
	tokens := oauth2.ReuseTokenSource(nil, mqttOAuthTokenSource{
		ctx:             tokenContext,
		config:          config,
		credentialsFile: c.credentialsFile,
	})
	return func() (string, string) {
		token, err := tokens.Token()
		if err != nil {
			slog.Error("Failed to obtain MQTT OAuth2 token", "error", err)
			// An empty password makes the broker reject this connection attempt.
			return c.oauthUsername, ""
		}
		return c.oauthUsername, token.AccessToken
	}, nil
}

func (c mqttAuthConfig) basicCredentialsProvider() (mqtt.CredentialsProvider, error) {
	if c.credentialsFile == "" {
		return nil, fmt.Errorf("MQTT basic credentials file is required")
	}
	if _, err := readMQTTCredentials(c.credentialsFile, mqttAuthBasic); err != nil {
		return nil, err
	}
	return func() (string, string) {
		credentials, err := readMQTTCredentials(c.credentialsFile, mqttAuthBasic)
		if err != nil {
			slog.Error("Failed to read MQTT basic credentials", "error", err)
			return "", ""
		}
		return credentials.Username, credentials.Password
	}, nil
}

type mqttCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

func readMQTTCredentials(path, mode string) (mqttCredentials, error) {
	credentialBytes, err := os.ReadFile(path)
	if err != nil {
		return mqttCredentials{}, fmt.Errorf("read MQTT %s credentials: %w", mode, err)
	}
	var credentials mqttCredentials
	if err := json.Unmarshal(credentialBytes, &credentials); err != nil {
		return mqttCredentials{}, fmt.Errorf("parse MQTT %s credentials: %w", mode, err)
	}
	switch mode {
	case mqttAuthOAuth2:
		if credentials.ClientID == "" || credentials.ClientSecret == "" {
			return mqttCredentials{}, fmt.Errorf("MQTT OAuth2 client_id and client_secret must be non-empty")
		}
	case mqttAuthBasic:
		if credentials.Username == "" || credentials.Password == "" {
			return mqttCredentials{}, fmt.Errorf("MQTT basic username and password must be non-empty")
		}
	}
	return credentials, nil
}

func waitMQTTToken(ctx context.Context, token mqtt.Token) error {
	waitContext, cancel := context.WithTimeout(ctx, mqttOperationTimeout)
	defer cancel()
	select {
	case <-token.Done():
		return token.Error()
	case <-waitContext.Done():
		return waitContext.Err()
	}
}

func (s *keySync) run(ctx context.Context) {
	// This worker serializes immediate and periodic publication.
	// Three refresh attempts occur before the previous publication lease expires.
	refresh := time.NewTicker(max(s.replication.leaseTTL/4, time.Nanosecond))
	defer refresh.Stop()
	for {
		select {
		case <-s.publishRequests:
		case <-refresh.C:
		case <-ctx.Done():
			return
		}
		if !s.subscribed.Load() {
			continue
		}
		if err := s.replication.publish(ctx, s.client); err != nil && ctx.Err() == nil {
			slog.Warn("MQTT public key publication failed", "error", err)
		}
	}
}

func (s *keySync) connectionOpened(ctx context.Context) {
	s.subscribed.Store(false)
	// A retry owner may already be carrying the subscription across a reconnect.
	if !s.subscribing.CompareAndSwap(false, true) {
		return
	}
	defer s.subscribing.Store(false)

	backoff := syncBackoffMin
	for ctx.Err() == nil {
		err := waitMQTTToken(ctx, s.client.Subscribe(keyTopicWildcard, mqttQoS, s.receive))
		if err == nil {
			s.subscribed.Store(true)
			select {
			case s.publishRequests <- struct{}{}:
			default:
			}
			return
		}
		if ctx.Err() == nil {
			slog.Warn("MQTT public key subscription failed", "error", err, "retry_in", backoff)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
		backoff = min(backoff*2, syncBackoffMax)
	}
}

func (s *keySync) isConnectedAndSubscribed() bool {
	return s.client.IsConnectionOpen() && s.subscribed.Load()
}

func (s *keySync) isCacheReady() bool {
	return s.priming.isComplete() && s.hasActiveLocalLeases()
}

func (s *keySync) hasActiveLocalLeases() bool {
	for _, key := range s.replication.local {
		if !s.replication.registry.contains(key.KeyID) {
			return false
		}
	}
	return true
}

func (s *keySync) receive(_ mqtt.Client, message mqtt.Message) {
	publication, accepted := s.replication.receive(message)
	if accepted && s.priming.observePublication(publication) {
		slog.Info("MQTT public key cache primed", "shard_id", s.replication.shardID)
	}
}

func (r *keyReplication) publish(ctx context.Context, client mqtt.Client) error {
	// One lease boundary keeps every key from this replica on the same refresh cycle.
	publishedAt := time.Now()
	leaseExpiresAt := publishedAt.Add(r.leaseTTL)
	for _, key := range r.local {
		payload, err := marshalPublicKey(key, publishedAt, leaseExpiresAt)
		if err != nil {
			return fmt.Errorf("serialize public key %q: %w", key.KeyID, err)
		}
		if err := waitMQTTToken(ctx, client.Publish(keyTopic(r.shardID, key.KeyID), mqttQoS, true, payload)); err != nil {
			return fmt.Errorf("publish local public key: %w", err)
		}
	}
	return nil
}

func (r *keyReplication) receive(message mqtt.Message) (publishedKey, bool) {
	// The shard routes the MQTT publication. The globally unique kid indexes the
	// flat aggregate, so shard ownership is not retained in memory.
	keyID, ok := parseKeyTopic(message.Topic())
	if !ok {
		slog.Warn("Ignoring public key with invalid topic", "topic", message.Topic())
		return publishedKey{}, false
	}
	publication, err := decodePublishedKey(message.Payload())
	if err != nil {
		slog.Warn("Ignoring invalid public key", "topic", message.Topic(), "error", err)
		return publishedKey{}, false
	}
	if err := r.registry.replace(keyID, publication); err != nil {
		slog.Warn("Ignoring public key", "topic", message.Topic(), "error", err)
		return publishedKey{}, false
	}
	slog.Info("Accepted public key", "topic", message.Topic(), "published_at", publication.publishedAt, "lease_expires_at", publication.leaseExpiresAt)
	return publication, true
}

func keyTopic(shardID, keyID string) string {
	return keyTopicPrefix + shardID + "/kid/" + keyID
}

func parseKeyTopic(topic string) (string, bool) {
	path, ok := strings.CutPrefix(topic, keyTopicPrefix)
	if !ok {
		return "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[1] != "kid" || validateShardID(parts[0]) != nil || validateKeyID(parts[2]) != nil {
		return "", false
	}
	return parts[2], true
}

func validateShardID(shardID string) error {
	if !validTopicSegment(shardID) {
		return fmt.Errorf("invalid shard ID %q", shardID)
	}
	return nil
}

func validTopicSegment(value string) bool {
	return value != "" && !strings.ContainsAny(value, "/+#")
}
