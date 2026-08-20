package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"golang.org/x/oauth2"
)

type fakeSVIDFetcher struct {
	svid *jwtsvid.SVID
	err  error
}

func (f fakeSVIDFetcher) Fetch(context.Context, []string) (*jwtsvid.SVID, error) {
	return f.svid, f.err
}

type fakeTokenExchanger struct {
	token *oauth2.Token
	err   error
}

func (f fakeTokenExchanger) Exchange(context.Context, string) (*oauth2.Token, error) {
	return f.token, f.err
}

func TestIdentityRequest_Validation(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		metadataHeader string
		acceptHeader   string
		wantStatus     int
	}{
		{
			name:           "missing Metadata header",
			query:          "?aud=test",
			metadataHeader: "",
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "invalid Metadata header",
			query:          "?aud=test",
			metadataHeader: "false",
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "missing aud parameter",
			query:          "",
			metadataHeader: "true",
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "empty aud parameter",
			query:          "?aud=",
			metadataHeader: "true",
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "empty additional aud parameter",
			query:          "?aud=test&aud=",
			metadataHeader: "true",
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "unsupported response type",
			query:          "?aud=test",
			metadataHeader: "true",
			acceptHeader:   "application/xml",
			wantStatus:     http.StatusNotAcceptable,
		},
	}

	server := new(MetadataServer)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test both API versions
			endpoints := []string{"/latest/meta-data/identity", "/2009-04-04/meta-data/identity"}
			for _, endpoint := range endpoints {
				req := httptest.NewRequest(http.MethodGet, endpoint+tt.query, nil)
				if tt.metadataHeader != "" {
					req.Header.Set("Metadata", tt.metadataHeader)
				}
				if tt.acceptHeader != "" {
					req.Header.Set("Accept", tt.acceptHeader)
				}
				req.RemoteAddr = "10.0.0.1:12345"
				rr := httptest.NewRecorder()

				server.handleIdentity(rr, req)

				if rr.Code != tt.wantStatus {
					t.Errorf("endpoint %s: status = %d, want %d, body: %s", endpoint, rr.Code, tt.wantStatus, rr.Body.String())
				}
			}
		})
	}
}

func TestIdentityRequest_RejectsForwardedRequests(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
	}{
		{
			name:   "Forwarded header",
			header: "Forwarded",
			value:  "for=192.0.2.60;proto=http;by=203.0.113.43",
		},
		{
			name:   "X-Forwarded-For header",
			header: "X-Forwarded-For",
			value:  "192.0.2.60",
		},
		{
			name:   "X-Forwarded-Host header",
			header: "X-Forwarded-Host",
			value:  "example.com",
		},
		{
			name:   "X-Forwarded-Proto header",
			header: "X-Forwarded-Proto",
			value:  "https",
		},
		{
			name:   "empty X-Forwarded-For header",
			header: "X-Forwarded-For",
			value:  "",
		},
	}

	server := new(MetadataServer)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/latest/meta-data/identity?aud=test", nil)
			req.Header.Set("Metadata", "true")
			req.Header.Set(tt.header, tt.value)
			req.RemoteAddr = "10.0.0.1:12345"
			rr := httptest.NewRecorder()

			server.handleIdentity(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
			}
			if !strings.Contains(rr.Body.String(), "Forwarded requests not allowed") {
				t.Errorf("body = %q, want to contain 'Forwarded requests not allowed'", rr.Body.String())
			}
		})
	}
}

func TestExchangeSPIRESVIDUsesCSIUnixSocket(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "identity-keys.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/exchange" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Host != "exchange" {
			t.Errorf("authority = %q, want exchange", r.Host)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if got := r.FormValue("subject_token"); got != "local-svid" {
			t.Errorf("subject token = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"exchanged","token_type":"Bearer","expires_in":60}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	client, err := newExchangeClient("unix://" + socketPath)
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.Exchange(context.Background(), "local-svid")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "exchanged" || !token.Expiry.After(time.Now()) {
		t.Fatalf("token = %#v", token)
	}
}

func TestExchangeSPIRESVIDRejectsUnavailableCSIUnixSocket(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	client, err := newExchangeClient("unix://" + socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exchange(context.Background(), "local-svid"); err == nil {
		t.Fatal("expected unavailable DSX Identity Keys Proxy socket to fail")
	}
}

func TestExchangeSPIRESVIDUsesDirectServiceURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/exchange" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"exchanged","token_type":"Bearer","expires_in":60}`))
	}))
	t.Cleanup(server.Close)

	client, err := newExchangeClient(server.URL + "/v1/exchange")
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.Exchange(context.Background(), "local-svid")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "exchanged" || !token.Expiry.After(time.Now()) {
		t.Fatalf("token = %#v", token)
	}
}

func TestDirectExchangeVerifiesTLSCertificate(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"exchanged","token_type":"Bearer","expires_in":60}`))
	}))
	t.Cleanup(server.Close)
	client, err := newExchangeClient(server.URL + "/v1/exchange")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exchange(context.Background(), "local-svid"); err == nil {
		t.Fatal("self-signed DSX Identity Keys certificate was accepted")
	}
}

func TestExchangeTransportConfiguration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "missing", wantErr: "required"},
		{name: "Unix endpoint", url: "unix:///identity-keys.sock"},
		{name: "HTTP endpoint", url: "http://exchange.example.com/v1/exchange"},
		{name: "HTTPS endpoint", url: "https://exchange.example.com/v1/exchange"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newExchangeClient(test.url)
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestMetadataMuxHasNoSessionTokenEndpoint(t *testing.T) {
	t.Parallel()

	mux := newMetadataMux(new(MetadataServer))
	for _, test := range []struct {
		path   string
		status int
	}{
		{path: "/latest/api/token", status: http.StatusNotFound},
		{path: "/latest/meta-data/identity", status: http.StatusMethodNotAllowed},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, test.path, nil))
		if recorder.Code != test.status {
			t.Fatalf("PUT %s status = %d, want %d", test.path, recorder.Code, test.status)
		}
	}
}

func TestIsForwardedRequest(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{
			name:    "no headers",
			headers: map[string]string{},
			want:    false,
		},
		{
			name:    "unrelated headers",
			headers: map[string]string{"Content-Type": "application/json", "Accept": "text/plain"},
			want:    false,
		},
		{
			name:    "Forwarded header",
			headers: map[string]string{"Forwarded": "for=192.0.2.60"},
			want:    true,
		},
		{
			name:    "X-Forwarded-For header",
			headers: map[string]string{"X-Forwarded-For": "192.0.2.60"},
			want:    true,
		},
		{
			name:    "X-Forwarded-Host header",
			headers: map[string]string{"X-Forwarded-Host": "example.com"},
			want:    true,
		},
		{
			name:    "X-Forwarded-Proto header",
			headers: map[string]string{"X-Forwarded-Proto": "https"},
			want:    true,
		},
		{
			name:    "multiple forwarding headers",
			headers: map[string]string{"X-Forwarded-For": "192.0.2.60", "X-Forwarded-Proto": "https"},
			want:    true,
		},
		{
			name:    "empty forwarding header",
			headers: map[string]string{"X-Forwarded-For": ""},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			if got := isForwardedRequest(h); got != tt.want {
				t.Errorf("isForwardedRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIdentityResponse_JSONMarshaling(t *testing.T) {
	resp := IdentityResponse{
		Token:     "test-token",
		ExpiresIn: 3600,
		SpiffeID:  "spiffe://example.org/test",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed IdentityResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if parsed.Token != resp.Token {
		t.Errorf("token = %q, want %q", parsed.Token, resp.Token)
	}
	if parsed.ExpiresIn != resp.ExpiresIn {
		t.Errorf("expires_in = %d, want %d", parsed.ExpiresIn, resp.ExpiresIn)
	}
	if parsed.SpiffeID != resp.SpiffeID {
		t.Errorf("spiffe_id = %q, want %q", parsed.SpiffeID, resp.SpiffeID)
	}
}

func TestAcceptHeaderParsing(t *testing.T) {
	tests := []struct {
		name         string
		acceptHeader string
		wantFormat   string
		wantErr      bool
	}{
		{
			name:       "no Accept header defaults to JSON",
			wantFormat: "application/json",
		},
		{
			name:         "Accept application/json returns JSON",
			acceptHeader: "application/json",
			wantFormat:   "application/json",
		},
		{
			name:         "Accept text/plain returns plain text",
			acceptHeader: "text/plain",
			wantFormat:   "text/plain",
		},
		{
			name:         "unsupported type is rejected",
			acceptHeader: "application/xml",
			wantErr:      true,
		},
		{
			name:         "media type list is rejected",
			acceptHeader: "application/json, text/plain",
			wantErr:      true,
		},
		{
			name:         "optional whitespace is parsed",
			acceptHeader: " application/json",
			wantFormat:   "application/json",
		},
		{
			name:         "media type is case insensitive",
			acceptHeader: "Application/Json",
			wantFormat:   "application/json",
		},
		{
			name:         "media type parameters are ignored",
			acceptHeader: "application/json; charset=utf-8",
			wantFormat:   "application/json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := determineResponseFormat(tt.acceptHeader)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.wantFormat {
				t.Errorf("determineResponseFormat() = %q, want %q", got, tt.wantFormat)
			}
		})
	}
}

func TestTTLListenerSetsResponseTTL(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := (ttlListener{Listener: listener}).Accept()
		accepted <- conn
		acceptErr <- err
	}()

	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	server := <-accepted
	if err := <-acceptErr; err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	rawConn, err := server.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var ttl int
	if err := rawConn.Control(func(fd uintptr) {
		ttl, err = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL)
	}); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if ttl != 1 {
		t.Fatalf("TTL = %d, want 1", ttl)
	}
}

func TestIdentityFailureDoesNotExposeInternalError(t *testing.T) {
	t.Parallel()

	server := &MetadataServer{
		svids: fakeSVIDFetcher{err: errors.New("private SPIRE socket detail")},
	}
	request := httptest.NewRequest(http.MethodGet, "/latest/meta-data/identity?aud=test", nil)
	request.Header.Set("Metadata", "true")
	recorder := httptest.NewRecorder()
	server.handleIdentity(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != "identity service unavailable\n" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestIdentitySuccessDisablesCaching(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   "https://ds9.dsx.nvid.id",
		Subject:  "spiffe://operator-1.ds9.dsx.nvid.id/bm/worker-1",
		Audience: jwt.Audience{"test"},
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(now.Add(time.Minute)),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	server := &MetadataServer{
		svids: fakeSVIDFetcher{
			svid: &jwtsvid.SVID{ID: spiffeid.RequireFromString("spiffe://example.org/bm/worker-1")},
		},
		exchange: fakeTokenExchanger{
			token: &oauth2.Token{AccessToken: token, ExpiresIn: 60},
		},
	}
	request := httptest.NewRequest(http.MethodGet, "/latest/meta-data/identity?aud=test", nil)
	request.Header.Set("Metadata", "true")
	recorder := httptest.NewRecorder()
	server.handleIdentity(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}
