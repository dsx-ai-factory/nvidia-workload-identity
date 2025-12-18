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

	// Create a minimal server for testing request validation
	// Note: This will fail at pod lookup, but we're testing request validation
	server := &MetadataServer{
		k8sClient:          nil, // Will cause pod lookup to fail
		exchangeServiceURL: "http://localhost:8080",
		httpClient:         http.DefaultClient,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/metadata/identity", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			req.RemoteAddr = "10.0.0.1:12345"
			rr := httptest.NewRecorder()

			server.handleIdentity(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d, body: %s", rr.Code, tt.wantStatus, rr.Body.String())
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
		Token:    "test-token",
		SpiffeID: "spiffe://example.org/test",
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
	if parsed.SpiffeID != resp.SpiffeID {
		t.Errorf("spiffe_id = %q, want %q", parsed.SpiffeID, resp.SpiffeID)
	}
}

func TestExchangeRequest_JSONMarshaling(t *testing.T) {
	req := ExchangeRequest{
		JWT: "eyJhbGciOiJFUzI1NiIsInR5cCI6IkpXVCJ9.test",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed ExchangeRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if parsed.JWT != req.JWT {
		t.Errorf("jwt = %q, want %q", parsed.JWT, req.JWT)
	}
}

func TestExchangeResponse_JSONParsing(t *testing.T) {
	jsonData := `{
		"token": "new-token",
		"expires_at": "2025-12-18T12:00:00Z",
		"spiffe_id": "spiffe://tenant.example.org/workload"
	}`

	var resp ExchangeResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Token != "new-token" {
		t.Errorf("token = %q, want %q", resp.Token, "new-token")
	}
	if resp.SpiffeID != "spiffe://tenant.example.org/workload" {
		t.Errorf("spiffe_id = %q, want %q", resp.SpiffeID, "spiffe://tenant.example.org/workload")
	}
	if resp.ExpiresAt.IsZero() {
		t.Error("expires_at should not be zero")
	}
}
