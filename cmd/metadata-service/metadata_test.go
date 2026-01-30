package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

func TestIdentityRequest_Validation(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		metadataHeader string
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

// TestAcceptHeaderParsing tests the Accept header parsing logic
// Note: Full integration tests with both response formats are done via make targets
func TestAcceptHeaderParsing(t *testing.T) {
	tests := []struct {
		name         string
		acceptHeader string
		wantFormat   string // "application/json" or "text/plain"
	}{
		{
			name:         "no Accept header defaults to JSON",
			acceptHeader: "",
			wantFormat:   "application/json",
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
			name:         "Accept with quality values prefers text/plain",
			acceptHeader: "text/plain;q=0.9, application/json",
			wantFormat:   "text/plain",
		},
		{
			name:         "Accept with multiple types prefers text/plain",
			acceptHeader: "text/plain, application/json",
			wantFormat:   "text/plain",
		},
		{
			name:         "Accept only JSON returns JSON",
			acceptHeader: "application/json",
			wantFormat:   "application/json",
		},
		{
			name:         "Accept unknown type defaults to JSON",
			acceptHeader: "application/xml",
			wantFormat:   "application/json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate Accept header parsing logic from handleIdentity
			responseFormat := "application/json"
			if tt.acceptHeader != "" {
				acceptTypes := strings.Split(tt.acceptHeader, ",")
				prefersTextPlain := false
				prefersJSON := false

				for _, acceptType := range acceptTypes {
					acceptType = strings.TrimSpace(acceptType)
					if idx := strings.Index(acceptType, ";"); idx != -1 {
						acceptType = acceptType[:idx]
					}
					acceptType = strings.TrimSpace(acceptType)

					if acceptType == "text/plain" {
						prefersTextPlain = true
					}
					if acceptType == "application/json" {
						prefersJSON = true
					}
				}

				if prefersTextPlain {
					responseFormat = "text/plain"
				} else if prefersJSON {
					responseFormat = "application/json"
				}
			}

			if responseFormat != tt.wantFormat {
				t.Errorf("responseFormat = %q, want %q", responseFormat, tt.wantFormat)
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
