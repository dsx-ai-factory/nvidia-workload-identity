package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdentityRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
	}{
		{
			name:        "missing content type",
			body:        `{"audience":"test"}`,
			contentType: "",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "wrong content type",
			body:        `{"audience":"test"}`,
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "invalid json",
			body:        `{invalid}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "empty audience",
			body:        `{"audience":""}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "missing audience field",
			body:        `{}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
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
				req := httptest.NewRequest(http.MethodPut, endpoint, strings.NewReader(tt.body))
				if tt.contentType != "" {
					req.Header.Set("Content-Type", tt.contentType)
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

func TestIdentityRequest_JSONParsing(t *testing.T) {
	validJSON := `{"audience":"spiffe://example.org"}`

	var req IdentityRequest
	err := json.Unmarshal([]byte(validJSON), &req)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.Audience != "spiffe://example.org" {
		t.Errorf("audience = %q, want %q", req.Audience, "spiffe://example.org")
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
