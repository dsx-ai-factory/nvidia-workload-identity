package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
			name:           "unsupported response type",
			query:          "?aud=test",
			metadataHeader: "true",
			acceptHeader:   "application/xml",
			wantStatus:     http.StatusNotAcceptable,
		},
	}

	server := &MetadataServer{
		exchangeSocket:  "/nonexistent/exchange.sock",
		spireSocketPath: "/nonexistent/socket",
		httpClient:      http.DefaultClient,
	}

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

	server := &MetadataServer{
		exchangeSocket:  "/nonexistent/exchange.sock",
		spireSocketPath: "/nonexistent/socket",
		httpClient:      http.DefaultClient,
	}

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

	socketPath := filepath.Join(t.TempDir(), "exchange.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/exchange" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
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

	metadata := &MetadataServer{exchangeSocket: socketPath, httpClient: newExchangeHTTPClient(socketPath)}
	token, err := metadata.exchangeSPIRESVID(context.Background(), "local-svid")
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
	metadata := &MetadataServer{exchangeSocket: socketPath, httpClient: newExchangeHTTPClient(socketPath)}
	if _, err := metadata.exchangeSPIRESVID(context.Background(), "local-svid"); err == nil {
		t.Fatal("expected unavailable Exchange proxy socket to fail")
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
			name:         "higher quality type is selected",
			acceptHeader: "text/plain;q=0.9, application/json",
			wantFormat:   "application/json",
		},
		{
			name:         "first supported type is selected",
			acceptHeader: "text/plain, application/json",
			wantFormat:   "text/plain",
		},
		{
			name:         "unsupported type is rejected",
			acceptHeader: "application/xml",
			wantErr:      true,
		},
		{
			name:         "unsupported alternative is ignored",
			acceptHeader: "application/xml, text/plain",
			wantFormat:   "text/plain",
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
			name:         "zero quality is rejected",
			acceptHeader: "application/json;q=0",
			wantErr:      true,
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

func TestTTLListener_SetsTTLOnAcceptedConnections(t *testing.T) {
	// Create a TCP listener on a random port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer ln.Close()

	// Wrap with ttlListener
	ttlLn := &ttlListener{Listener: ln}

	// Channel to receive the TTL value from the accepted connection
	ttlChan := make(chan int, 1)
	errChan := make(chan error, 1)

	go func() {
		conn, err := ttlLn.Accept()
		if err != nil {
			errChan <- err
			return
		}
		defer conn.Close()

		// Read the TTL value from the connection
		tcpConn, ok := conn.(*net.TCPConn)
		if !ok {
			errChan <- nil
			ttlChan <- -1
			return
		}

		rawConn, err := tcpConn.SyscallConn()
		if err != nil {
			errChan <- err
			return
		}

		var ttl int
		var controlErr error
		rawConn.Control(func(fd uintptr) {
			ttl, controlErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL)
		})
		if controlErr != nil {
			errChan <- controlErr
			return
		}

		errChan <- nil
		ttlChan <- ttl
	}()

	// Connect to the listener
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	// Wait for the accept goroutine to finish
	if err := <-errChan; err != nil {
		t.Fatalf("accept goroutine error: %v", err)
	}

	ttl := <-ttlChan
	if ttl != 1 {
		t.Errorf("TTL = %d, want 1", ttl)
	}
}

func TestTTLListener_PassesThroughAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer ln.Close()

	ttlLn := &ttlListener{Listener: ln}

	// Addr should pass through to the underlying listener
	if ttlLn.Addr().String() != ln.Addr().String() {
		t.Errorf("Addr() = %q, want %q", ttlLn.Addr().String(), ln.Addr().String())
	}
}

func TestTTLListener_PassesThroughClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	ttlLn := &ttlListener{Listener: ln}

	// Close should pass through
	if err := ttlLn.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Verify the underlying listener is closed
	_, err = ln.Accept()
	if err == nil {
		t.Error("expected Accept to fail after Close")
	}
}
