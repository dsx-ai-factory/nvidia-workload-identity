package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/go-jose/go-jose/v4"
	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

func TestLiveMQTTAuthorization(t *testing.T) {
	brokerURL := os.Getenv("DSX_E2E_MQTT_BROKER")
	if brokerURL == "" {
		t.Skip("DSX_E2E_MQTT_BROKER is not set")
	}
	accessToken := os.Getenv("DSX_E2E_MQTT_TOKEN")
	if accessToken == "" {
		t.Fatal("DSX_E2E_MQTT_TOKEN is not set")
	}
	observerToken := os.Getenv("DSX_E2E_MQTT_OBSERVER_TOKEN")
	if observerToken == "" {
		t.Fatal("DSX_E2E_MQTT_OBSERVER_TOKEN is not set")
	}

	initialAuthorized := connectLiveMQTT(t, brokerURL, accessToken)
	initialAuthorized.Disconnect(0)

	unauthenticated := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(fmt.Sprintf("anonymous-e2e-%d", time.Now().UnixNano())).
		SetConnectTimeout(5 * time.Second))
	token := unauthenticated.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		t.Fatal("anonymous MQTT connection timed out")
	}
	if err := token.Error(); err == nil {
		unauthenticated.Disconnect(0)
		t.Fatal("anonymous MQTT connection succeeded")
	}
	freshAfterAnonymous := connectLiveMQTT(t, brokerURL, accessToken)
	freshAfterAnonymous.Disconnect(0)

	invalid := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(fmt.Sprintf("invalid-oauth-e2e-%d", time.Now().UnixNano())).
		SetUsername("oauthtoken").
		SetPassword(accessToken + ".invalid").
		SetConnectTimeout(5 * time.Second))
	token = invalid.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		t.Fatal("invalid OAuth MQTT connection timed out")
	}
	if err := token.Error(); err == nil {
		invalid.Disconnect(0)
		t.Fatal("invalid OAuth MQTT connection succeeded")
	}
	authorized := connectLiveMQTT(t, brokerURL, accessToken)
	t.Cleanup(func() { authorized.Disconnect(0) })

	allowedTopic := fmt.Sprintf("%se2e-%d", keyTopicPrefix, time.Now().UnixNano())
	received := make(chan []byte, 1)
	token = authorized.Subscribe(allowedTopic, 1, func(_ mqtt.Client, message mqtt.Message) {
		received <- message.Payload()
	})
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		t.Fatalf("in-prefix MQTT subscription failed: %v", token.Error())
	}
	if got := token.(*mqtt.SubscribeToken).Result()[allowedTopic]; got != 1 {
		t.Fatalf("in-prefix MQTT SUBACK = %#x, want QoS 1", got)
	}
	token = authorized.Publish(allowedTopic, 1, false, "allowed")
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		t.Fatalf("in-prefix MQTT publish failed: %v", token.Error())
	}
	select {
	case payload := <-received:
		if string(payload) != "allowed" {
			t.Fatalf("in-prefix MQTT payload = %q, want allowed", payload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-prefix MQTT publication was not delivered")
	}

	const deniedTopic = "outside/#"
	token = authorized.Subscribe(deniedTopic, 1, nil)
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		t.Fatalf("out-of-prefix MQTT subscription did not receive a SUBACK: %v", token.Error())
	}
	subscribeToken, ok := token.(*mqtt.SubscribeToken)
	if !ok {
		t.Fatalf("subscribe returned %T, want *mqtt.SubscribeToken", token)
	}
	if got := subscribeToken.Result()[deniedTopic]; got != 0x80 {
		t.Fatalf("out-of-prefix MQTT SUBACK = %#x, want failure (0x80)", got)
	}

	observer := connectLiveMQTT(t, brokerURL, observerToken)
	t.Cleanup(func() { observer.Disconnect(0) })
	deniedPublishTopic := fmt.Sprintf("outside/dsx-identity-e2e-%d", time.Now().UnixNano())
	deniedPublication := make(chan struct{}, 1)
	token = observer.Subscribe(deniedPublishTopic, 1, func(_ mqtt.Client, _ mqtt.Message) {
		deniedPublication <- struct{}{}
	})
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		t.Fatalf("observer subscription failed: %v", token.Error())
	}
	if got := token.(*mqtt.SubscribeToken).Result()[deniedPublishTopic]; got != 1 {
		t.Fatalf("observer SUBACK = %#x, want QoS 1", got)
	}
	token = authorized.Publish(deniedPublishTopic, 1, false, "denied")
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		t.Fatalf("out-of-prefix MQTT publish did not receive its protocol acknowledgement: %v", token.Error())
	}
	select {
	case <-deniedPublication:
		t.Fatal("out-of-prefix MQTT publication was delivered")
	case <-time.After(2 * time.Second):
	}
}

func TestLiveMQTTRetainedReplay(t *testing.T) {
	brokerURL := os.Getenv("DSX_E2E_MQTT_BROKER")
	if brokerURL == "" {
		t.Skip("DSX_E2E_MQTT_BROKER is not set")
	}
	accessToken := os.Getenv("DSX_E2E_MQTT_TOKEN")
	if accessToken == "" {
		t.Fatal("DSX_E2E_MQTT_TOKEN is not set")
	}
	var expected jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(os.Getenv("DSX_E2E_MQTT_EXPECTED_JWKS")), &expected); err != nil {
		t.Fatalf("parse DSX_E2E_MQTT_EXPECTED_JWKS: %v", err)
	}
	if len(expected.Keys) == 0 {
		t.Fatal("DSX_E2E_MQTT_EXPECTED_JWKS contains no keys")
	}

	client := connectLiveMQTT(t, brokerURL, accessToken)
	t.Cleanup(func() { client.Disconnect(0) })
	type retainedMessage struct {
		topic    string
		payload  []byte
		retained bool
	}
	retained := make(chan retainedMessage, 16)
	shardID := os.Getenv("DSX_E2E_MQTT_SHARD_ID")
	if shardID == "" {
		t.Fatal("DSX_E2E_MQTT_SHARD_ID is not set")
	}
	shardTopic := keyTopicPrefix + shardID + "/kid/+"
	token := client.Subscribe(shardTopic, 1, func(_ mqtt.Client, message mqtt.Message) {
		select {
		case retained <- retainedMessage{
			topic:    message.Topic(),
			payload:  append([]byte(nil), message.Payload()...),
			retained: message.Retained(),
		}:
		default:
		}
	})
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		t.Fatalf("retained shard subscription failed: %v", token.Error())
	}
	if got := token.(*mqtt.SubscribeToken).Result()[shardTopic]; got != 1 {
		t.Fatalf("retained shard SUBACK = %#x, want QoS 1", got)
	}

	registry := newKeyRegistry()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case message := <-retained:
			if !message.retained {
				t.Fatal("late subscriber received a live public key instead of retained replay")
			}
			keyID, ok := parseKeyTopic(message.topic)
			if !ok {
				t.Fatal("retained public key topic was invalid")
			}
			publication, err := decodePublishedKey(message.payload)
			if err != nil {
				t.Fatalf("deployed Event Bus retained an invalid public key: %v", err)
			}
			if err := registry.replace(keyID, publication); err != nil {
				t.Fatalf("deployed Event Bus retained a conflicting public key: %v", err)
			}
			if containsKeyset(registry.keyset().Keys, expected.Keys) {
				return
			}
		case <-deadline:
			t.Fatalf("deployed Event Bus retained keys = %v, want at least %v", keyIDs(registry.keyset().Keys), keyIDs(expected.Keys))
		}
	}
}

func connectLiveMQTT(t *testing.T, brokerURL, accessToken string) mqtt.Client {
	t.Helper()
	client := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(fmt.Sprintf("oauth-e2e-%d", time.Now().UnixNano())).
		SetUsername("oauthtoken").
		SetPassword(accessToken).
		SetConnectTimeout(5 * time.Second))
	token := client.Connect()
	if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
		t.Fatalf("OAuth-authenticated MQTT connection failed: %v", token.Error())
	}
	return client
}

func TestMQTTSynchronizesRetainedPublicKeys(t *testing.T) {
	broker := mqttserver.New(nil)
	broker.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := broker.AddHook(new(oauthTestHook), nil); err != nil {
		t.Fatal(err)
	}
	listener := listeners.NewTCP(listeners.Config{ID: "test", Address: "127.0.0.1:0"})
	if err := broker.AddListener(listener); err != nil {
		t.Fatal(err)
	}
	if err := broker.Serve(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	brokerURL := "tcp://" + listener.Address()
	oauth := testMQTTOAuthConfig(t)
	unauthenticated := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(brokerURL).SetClientID("anonymous"))
	connect := unauthenticated.Connect()
	if !connect.WaitTimeout(2*time.Second) || connect.Error() == nil {
		unauthenticated.Disconnect(0)
		t.Fatal("MQTT broker accepted a client without OAuth2 credentials")
	}
	wrongToken := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID("wrong-token").
		SetUsername("oauthtoken").
		SetPassword("wrong"))
	connect = wrongToken.Connect()
	if !connect.WaitTimeout(2*time.Second) || connect.Error() == nil {
		wrongToken.Disconnect(0)
		t.Fatal("MQTT broker accepted an incorrect OAuth2 token")
	}

	firstKey := testJWK(t, "first-kid", false)
	secondKey := testJWK(t, "second-kid", false)
	const leaseTTL = 2 * time.Second
	firstPayload := testPublishedKey(t, firstKey.Public(), leaseTTL)
	secondPayload := testPublishedKey(t, secondKey.Public(), leaseTTL)
	firstRegistry := newKeyRegistry()
	first, err := startKeySync(context.Background(), brokerURL, "client-one", "shard-one", []jose.JSONWebKey{firstKey.Public()}, leaseTTL, firstRegistry, oauth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	waitForReady(t, first)
	if got := keyIDs(firstRegistry.keyset().Keys); !slices.Equal(got, []string{firstKey.KeyID}) {
		t.Fatalf("first startup keys = %v, want its local key", got)
	}
	secondRegistry := newKeyRegistry()
	second, err := startKeySync(context.Background(), brokerURL, "client-two", "shard-two", []jose.JSONWebKey{secondKey.Public()}, leaseTTL, secondRegistry, oauth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	waitForReady(t, second)
	waitForKeyCount(t, firstRegistry, 2)
	waitForKeyCount(t, secondRegistry, 2)
	firstToken := signTestToken(t, firstKey)
	secondToken := signTestToken(t, secondKey)
	for name, registry := range map[string]*keyRegistry{"first": firstRegistry, "second": secondRegistry} {
		served := servedKeyset(t, registry)
		if len(served.Keys) != 2 || !verifyTestToken(served, firstToken) || !verifyTestToken(served, secondToken) {
			t.Fatalf("%s HTTP JWKS did not preserve and verify all expected public keys", name)
		}
	}

	// Invalid publications preserve the last valid aggregate.
	for name, invalid := range map[string][]byte{
		"malformed":      []byte(`{"jwk":`),
		"private":        addJWKField(t, firstPayload, "d", "private"),
		"valid mismatch": secondPayload,
	} {
		token := second.client.Publish(keyTopic("shard-one", firstKey.KeyID), mqttQoS, true, invalid)
		if !token.WaitTimeout(2*time.Second) || token.Error() != nil {
			t.Fatalf("publish %s key: %v", name, token.Error())
		}
		for registryName, registry := range map[string]*keyRegistry{"first": firstRegistry, "second": secondRegistry} {
			waitForServedKeyset(t, registry, func(served jose.JSONWebKeySet) bool {
				return len(served.Keys) == 2 && verifyTestToken(served, firstToken) && verifyTestToken(served, secondToken)
			}, registryName+" registry to recover after "+name+" publication")
		}
	}

	// A new replica publishes its new kid while the old replica remains active.
	newKey := testJWK(t, "new-kid", false)
	newToken := signTestToken(t, newKey)
	newRegistry := newKeyRegistry()
	newReplica, err := startKeySync(context.Background(), brokerURL, "client-two-new", "shard-two", []jose.JSONWebKey{newKey.Public()}, leaseTTL, newRegistry, oauth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(newReplica.Close)
	waitForReady(t, newReplica)
	waitForKeyCount(t, firstRegistry, 3)
	waitForKeyCount(t, newRegistry, 3)
	for name, registry := range map[string]*keyRegistry{"first": firstRegistry, "new": newRegistry} {
		served := servedKeyset(t, registry)
		if !verifyTestToken(served, firstToken) || !verifyTestToken(served, secondToken) || !verifyTestToken(served, newToken) {
			t.Fatalf("%s HTTP JWKS did not expose both retained kids", name)
		}
	}

	second.Close()
	waitForDisconnected(t, second.client)
	for name, registry := range map[string]*keyRegistry{"first": firstRegistry, "new": newRegistry} {
		waitForServedKeyset(t, registry, func(served jose.JSONWebKeySet) bool {
			return len(served.Keys) == 2 &&
				verifyTestToken(served, firstToken) &&
				!verifyTestToken(served, secondToken) &&
				verifyTestToken(served, newToken)
		}, name+" HTTP JWKS to remove only the retired key after lease expiry")
	}

	brokerClient, ok := broker.Clients.Get("client-one")
	if !ok {
		t.Fatal("broker did not retain client-one")
	}
	brokerClient.Stop(errors.New("injected connection loss"))

	waitForCondition(t, "MQTT reconnection readiness", func() bool {
		reconnected, ok := broker.Clients.Get("client-one")
		return ok && reconnected != brokerClient && keySyncReady(first)
	})

	restartedRegistry := newKeyRegistry()
	restarted, err := startKeySync(context.Background(), brokerURL, "client-restarted", "shard-one", []jose.JSONWebKey{firstKey.Public()}, leaseTTL, restartedRegistry, oauth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	waitForReady(t, restarted)
	waitForKeyCount(t, restartedRegistry, 2)
	if served := servedKeyset(t, restartedRegistry); !verifyTestToken(served, firstToken) || !verifyTestToken(served, newToken) {
		t.Fatal("restarted instance did not reconstruct the retained aggregate")
	}

	newReplica.Close()
	waitForDisconnected(t, newReplica.client)
}

func TestKeySyncRetriesInitialConnection(t *testing.T) {
	t.Parallel()
	broker := mqttserver.New(nil)
	broker.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	hook := new(initialConnectionRetryHook)
	if err := broker.AddHook(hook, nil); err != nil {
		t.Fatal(err)
	}
	listener := listeners.NewTCP(listeners.Config{ID: "retry", Address: "127.0.0.1:0"})
	if err := broker.AddListener(listener); err != nil {
		t.Fatal(err)
	}
	if err := broker.Serve(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	privateKey := testJWK(t, "retry-kid", false)
	key := privateKey.Public()
	syncer, err := startKeySync(
		context.Background(),
		"tcp://"+listener.Address(),
		"retry-client",
		"retry-shard",
		[]jose.JSONWebKey{key},
		time.Minute,
		newKeyRegistry(),
		testMQTTOAuthConfig(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(syncer.Close)
	waitForReady(t, syncer)
	if hook.attempts.Load() < 3 {
		t.Fatalf("connection attempts = %d, want an initial failure and retry", hook.attempts.Load())
	}
}

type oauthTestHook struct {
	mqttserver.HookBase
}

func (*oauthTestHook) ID() string { return "oauth-test" }

func (*oauthTestHook) Provides(hook byte) bool {
	return hook == mqttserver.OnConnectAuthenticate || hook == mqttserver.OnACLCheck
}

func (*oauthTestHook) OnConnectAuthenticate(_ *mqttserver.Client, packet packets.Packet) bool {
	return string(packet.Connect.Username) == "oauthtoken" && string(packet.Connect.Password) == "test-token"
}

func (*oauthTestHook) OnACLCheck(_ *mqttserver.Client, topic string, _ bool) bool {
	return strings.HasPrefix(topic, keyTopicPrefix)
}

type initialConnectionRetryHook struct {
	mqttserver.HookBase
	attempts atomic.Int32
}

func (*initialConnectionRetryHook) ID() string { return "initial-connection-retry-test" }

func (*initialConnectionRetryHook) Provides(hook byte) bool {
	return hook == mqttserver.OnConnectAuthenticate || hook == mqttserver.OnACLCheck
}

func (h *initialConnectionRetryHook) OnConnectAuthenticate(_ *mqttserver.Client, packet packets.Packet) bool {
	return h.attempts.Add(1) > 2 &&
		string(packet.Connect.Username) == "oauthtoken" &&
		string(packet.Connect.Password) == "test-token"
}

func (*initialConnectionRetryHook) OnACLCheck(_ *mqttserver.Client, topic string, _ bool) bool {
	return strings.HasPrefix(topic, keyTopicPrefix)
}

func testMQTTOAuthConfig(t *testing.T) mqttOAuthConfig {
	t.Helper()
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(tokenServer.Close)
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return mqttOAuthConfig{
		tokenURL:         tokenServer.URL,
		clientID:         "exchange",
		clientSecretFile: secretFile,
	}
}

func TestMQTTOAuthCredentialsProvider(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	clientSecrets := make(chan string, 2)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		clientID, clientSecret, basic := r.BasicAuth()
		if !basic {
			clientID, clientSecret = r.FormValue("client_id"), r.FormValue("client_secret")
		}
		if clientID != "exchange" || r.FormValue("grant_type") != "client_credentials" || r.FormValue("scope") != "mqtt" {
			t.Errorf("unexpected token request: id=%q secret=%q form=%v", clientID, clientSecret, r.Form)
		}
		clientSecrets <- clientSecret
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"access-token-%d","token_type":"Bearer","expires_in":3600}`, request)
	}))
	t.Cleanup(tokenServer.Close)

	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := (mqttOAuthConfig{
		tokenURL:         tokenServer.URL,
		clientID:         "exchange",
		clientSecretFile: secretFile,
	}).credentialsProvider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("initial token requests = %d, want 0", requests.Load())
	}
	username, password := provider()
	if username != "oauthtoken" || password != "access-token-1" || <-clientSecrets != "secret" {
		t.Fatalf("credentials = %q/%q", username, password)
	}
	if err := os.WriteFile(secretFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	username, password = provider()
	if username != "oauthtoken" || password != "access-token-1" || requests.Load() != 1 {
		t.Fatalf("cached credentials = %q/%q, requests = %d", username, password, requests.Load())
	}
}

func TestMQTTOAuthCredentialsProviderRefreshesExpiredToken(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	clientSecrets := make(chan string, 2)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, clientSecret, _ := r.BasicAuth()
		clientSecrets <- clientSecret
		request := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"access-token-%d","token_type":"Bearer","expires_in":1}`, request)
	}))
	t.Cleanup(tokenServer.Close)

	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := (mqttOAuthConfig{
		tokenURL:         tokenServer.URL,
		clientID:         "exchange",
		clientSecretFile: secretFile,
	}).credentialsProvider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, first := provider()
	if err := os.WriteFile(secretFile, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, second := provider()
	if first != "access-token-1" || second != "access-token-2" || <-clientSecrets != "secret" || <-clientSecrets != "rotated" || requests.Load() != 2 {
		t.Fatalf("refreshed credentials = %q/%q, requests = %d", first, second, requests.Load())
	}
}

func TestWaitMQTTTokenHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitMQTTToken(ctx, blockingToken{release: make(chan struct{})})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context cancellation", err)
	}
}

func TestKeySyncRecoversFailedPublicationOnRefresh(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		key := testJWK(t, "one", true)
		remote := testJWK(t, "remote", true)
		client := &retryMQTTClient{
			subscribeFailures: 1,
			publishFailures:   1,
			retained: []testMQTTMessage{{
				topic:    keyTopic("k2", remote.KeyID),
				qos:      mqttQoS,
				retained: true,
				payload:  testPublishedKey(t, remote, time.Hour),
			}},
		}
		registry := newKeyRegistry()
		syncer := &keySync{
			client: client,
			replication: keyReplication{
				registry: registry,
				shardID:  "k1",
				local:    []jose.JSONWebKey{key},
				leaseTTL: time.Hour,
			},
			priming:         newKeyCachePriming([]jose.JSONWebKey{key}),
			publishRequests: make(chan struct{}, 1),
		}
		client.open.Store(true)
		go syncer.run(ctx)
		syncer.connectionOpened(ctx)
		<-syncer.priming.completed
		if !keySyncReady(syncer) || client.subscribeCalls.Load() != 2 || client.publishCalls.Load() != 2 {
			t.Fatalf("ready=%t subscribe calls=%d publish calls=%d", keySyncReady(syncer), client.subscribeCalls.Load(), client.publishCalls.Load())
		}
		if got := keyIDs(registry.keyset().Keys); !slices.Equal(got, []string{key.KeyID, remote.KeyID}) {
			t.Fatalf("synchronized keys = %v, want retained and local keys", got)
		}
		cancel()
		synctest.Wait()
	})
}

func TestExchangeLivenessAndReadiness(t *testing.T) {
	t.Parallel()
	key := testJWK(t, "one", true)
	registry := newKeyRegistry()
	client := new(retryMQTTClient)
	syncer := &keySync{
		client: client,
		replication: keyReplication{
			registry: registry,
			local:    []jose.JSONWebKey{key},
		},
		priming: newKeyCachePriming([]jose.JSONWebKey{key}),
	}
	handler := (&ExchangeServer{keySync: syncer}).handler()
	status := func(path string) int {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder.Code
	}

	if got := status("/live"); got != http.StatusOK {
		t.Fatalf("liveness status = %d, want %d", got, http.StatusOK)
	}
	if got := status("/health"); got != http.StatusNotFound {
		t.Fatalf("removed health status = %d, want %d", got, http.StatusNotFound)
	}
	if got := status("/ready"); got != http.StatusServiceUnavailable {
		t.Fatalf("disconnected readiness status = %d, want %d", got, http.StatusServiceUnavailable)
	}

	client.open.Store(true)
	if got := status("/ready"); got != http.StatusServiceUnavailable {
		t.Fatalf("unsubscribed readiness status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	syncer.subscribed.Store(true)
	close(syncer.priming.completed)
	if got := status("/ready"); got != http.StatusServiceUnavailable {
		t.Fatalf("unleased readiness status = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if err := registry.replace(key.KeyID, publishedKey{key: key, leaseExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got := status("/ready"); got != http.StatusOK {
		t.Fatalf("ready status = %d, want %d", got, http.StatusOK)
	}
}

func TestKeySyncRepublishesUntilFreshLocalSetObserved(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		key := testJWK(t, "one", true)
		stalePayload, err := marshalPublicKey(key, time.Now().Add(-time.Second), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		client := &retryMQTTClient{
			dropDeliveries: 1,
			retained: []testMQTTMessage{{
				topic:    keyTopic("k1", key.KeyID),
				qos:      mqttQoS,
				retained: true,
				payload:  stalePayload,
			}},
		}
		client.open.Store(true)
		syncer := &keySync{
			client: client,
			replication: keyReplication{
				registry: newKeyRegistry(),
				shardID:  "k1",
				local:    []jose.JSONWebKey{key},
				leaseTTL: 4 * time.Second,
			},
			priming:         newKeyCachePriming([]jose.JSONWebKey{key}),
			publishRequests: make(chan struct{}, 1),
		}

		go syncer.run(ctx)
		syncer.connectionOpened(ctx)
		<-syncer.priming.completed
		if !keySyncReady(syncer) || client.publishCalls.Load() != 2 {
			t.Fatalf("ready=%t publish calls=%d, want readiness after refresh", keySyncReady(syncer), client.publishCalls.Load())
		}
		cancel()
		synctest.Wait()
	})
}

func TestKeySyncRefreshesThreeTimesBeforeLeaseExpiry(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		published := make(chan struct{}, 1)
		client := &retryMQTTClient{published: published}
		client.open.Store(true)
		active := testJWK(t, "active", true)
		syncer := &keySync{
			client: client,
			replication: keyReplication{
				registry: newKeyRegistry(),
				shardID:  "current",
				local:    []jose.JSONWebKey{active},
				leaseTTL: 4 * time.Second,
			},
			priming:         newKeyCachePriming([]jose.JSONWebKey{active}),
			publishRequests: make(chan struct{}, 1),
		}
		go syncer.run(ctx)
		syncer.connectionOpened(ctx)
		<-syncer.priming.completed
		<-published
		client.publishCalls.Store(0)
		for range 3 {
			<-published
		}
		synctest.Wait()
		if got := client.publishCalls.Load(); got != 3 || client.subscribeCalls.Load() != 1 {
			t.Fatalf("publish calls = %d subscribe calls = %d, want 3 refreshes on one subscription", got, client.subscribeCalls.Load())
		}
		cancel()
		synctest.Wait()
	})
}

func TestKeyRegistryBackgroundLeaseExpiry(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		registry := newKeyRegistry()
		key := testJWK(t, "expiring", true)
		if err := registry.replace(key.KeyID, publishedKey{key: key, leaseExpiresAt: time.Now().Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		registry.start()
		waitForKeyCount(t, registry, 0)
		registry.stop()
		synctest.Wait()
	})
}

func waitForKeyCount(t *testing.T, registry *keyRegistry, want int) {
	t.Helper()
	waitForCondition(t, fmt.Sprintf("key count %d", want), func() bool {
		return len(registry.keyset().Keys) == want
	})
}

func keyIDs(keys []jose.JSONWebKey) []string {
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key.KeyID)
	}
	slices.Sort(result)
	return result
}

func containsKeyset(actual, expected []jose.JSONWebKey) bool {
	for _, key := range expected {
		if !verifyJWKPresent(actual, key) {
			return false
		}
	}
	return true
}

func waitForReady(t *testing.T, syncer *keySync) {
	t.Helper()
	waitForCondition(t, "MQTT key synchronization readiness", func() bool { return keySyncReady(syncer) })
}

func keySyncReady(syncer *keySync) bool {
	return syncer.isConnectedAndSubscribed() && syncer.isCacheReady()
}

func servedKeyset(t *testing.T, registry *keyRegistry) jose.JSONWebKeySet {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	(&ExchangeServer{registry: registry}).handleJWKS(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("JWKS status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var keys jose.JSONWebKeySet
	if err := json.NewDecoder(recorder.Body).Decode(&keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

func waitForServedKeyset(t *testing.T, registry *keyRegistry, ready func(jose.JSONWebKeySet) bool, description string) {
	t.Helper()
	waitForCondition(t, description, func() bool { return ready(servedKeyset(t, registry)) })
}

func waitForDisconnected(t *testing.T, client mqtt.Client) {
	t.Helper()
	waitForCondition(t, "MQTT client disconnection", func() bool { return !client.IsConnectionOpen() })
}

func waitForCondition(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		}
	}
}

func TestValidateShardID(t *testing.T) {
	t.Parallel()
	for _, shard := range []string{"", "shard/one", "shard+", "shard#"} {
		shard := shard
		t.Run(fmt.Sprintf("%q", shard), func(t *testing.T) {
			t.Parallel()
			if validateShardID(shard) == nil {
				t.Fatal("expected invalid shard ID")
			}
		})
	}
	for _, shard := range []string{"shard", " shard", "shard id"} {
		if err := validateShardID(shard); err != nil {
			t.Fatalf("valid shard ID %q was rejected: %v", shard, err)
		}
	}
}

func TestKeyTopicRoundTrip(t *testing.T) {
	t.Parallel()
	topic := keyTopic("shard-a", "thumbprint_123")
	keyID, ok := parseKeyTopic(topic)
	if !ok || keyID != "thumbprint_123" {
		t.Fatalf("parseKeyTopic(%q) = %q, %t", topic, keyID, ok)
	}
	for _, keyID := range []string{"", "key/id", "key+id", "key#id"} {
		if _, ok := parseKeyTopic(keyTopic("shard-a", keyID)); ok {
			t.Fatalf("invalid key ID %q was accepted", keyID)
		}
	}
}

type retryMQTTClient struct {
	open              atomic.Bool
	subscribeFailures int32
	publishFailures   int32
	dropDeliveries    int32
	subscribeCalls    atomic.Int32
	publishCalls      atomic.Int32
	published         chan<- struct{}
	receive           mqtt.MessageHandler
	retained          []testMQTTMessage
}

func (c *retryMQTTClient) IsConnected() bool      { return c.open.Load() }
func (c *retryMQTTClient) IsConnectionOpen() bool { return c.open.Load() }
func (c *retryMQTTClient) Connect() mqtt.Token    { return testToken{} }
func (c *retryMQTTClient) Disconnect(uint)        { c.open.Store(false) }
func (c *retryMQTTClient) Publish(topic string, qos byte, retained bool, payload any) mqtt.Token {
	c.publishCalls.Add(1)
	if atomic.AddInt32(&c.publishFailures, -1) >= 0 {
		return testToken{err: errors.New("publish failed")}
	}
	if c.published != nil {
		c.published <- struct{}{}
	}
	if atomic.AddInt32(&c.dropDeliveries, -1) >= 0 {
		return testToken{}
	}
	if c.receive != nil {
		c.receive(c, testMQTTMessage{topic: topic, qos: qos, payload: payload.([]byte)})
	}
	return testToken{}
}
func (c *retryMQTTClient) Subscribe(_ string, _ byte, receive mqtt.MessageHandler) mqtt.Token {
	c.subscribeCalls.Add(1)
	if atomic.AddInt32(&c.subscribeFailures, -1) >= 0 {
		return testToken{err: errors.New("subscribe failed")}
	}
	c.receive = receive
	for _, message := range c.retained {
		c.receive(c, message)
	}
	c.retained = nil
	return testToken{}
}
func (*retryMQTTClient) SubscribeMultiple(map[string]byte, mqtt.MessageHandler) mqtt.Token {
	return testToken{}
}
func (*retryMQTTClient) Unsubscribe(...string) mqtt.Token     { return testToken{} }
func (*retryMQTTClient) AddRoute(string, mqtt.MessageHandler) {}
func (*retryMQTTClient) OptionsReader() mqtt.ClientOptionsReader {
	return mqtt.ClientOptionsReader{}
}

type testToken struct{ err error }

func (testToken) Wait() bool                     { return true }
func (testToken) WaitTimeout(time.Duration) bool { return true }
func (testToken) Done() <-chan struct{}          { ch := make(chan struct{}); close(ch); return ch }
func (t testToken) Error() error                 { return t.err }

type blockingToken struct{ release <-chan struct{} }

func (blockingToken) Wait() bool { return true }
func (t blockingToken) WaitTimeout(time.Duration) bool {
	<-t.release
	return true
}
func (t blockingToken) Done() <-chan struct{} { return t.release }
func (blockingToken) Error() error            { return nil }

type testMQTTMessage struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
}

func (testMQTTMessage) Duplicate() bool   { return false }
func (m testMQTTMessage) Qos() byte       { return m.qos }
func (m testMQTTMessage) Retained() bool  { return m.retained }
func (m testMQTTMessage) Topic() string   { return m.topic }
func (testMQTTMessage) MessageID() uint16 { return 1 }
func (m testMQTTMessage) Payload() []byte { return m.payload }
func (testMQTTMessage) Ack()              {}
