package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

func setupTestServer(t *testing.T) *Server {
	server, err := NewServer()
	if err != nil {
		t.Fatalf("Failed to create test server: %v", err)
	}
	return server
}

func TestJWKSEndpoint(t *testing.T) {
	server := setupTestServer(t)

	req, err := http.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	// Check content type
	expected := "application/json"
	if ct := rr.Header().Get("Content-Type"); ct != expected {
		t.Errorf("handler returned wrong content type: got %v want %v", ct, expected)
	}

	// Check cache control header
	expectedCache := "public, max-age=3600"
	if cc := rr.Header().Get("Cache-Control"); cc != expectedCache {
		t.Errorf("handler returned wrong cache control: got %v want %v", cc, expectedCache)
	}

	// Verify JWKS by creating a keyfunc from it and testing token verification
	jwksJSON := rr.Body.Bytes()

	// Test that we can create a keyfunc from the JWKS response
	testKeyfunc, err := keyfunc.NewJWKSetJSON(json.RawMessage(jwksJSON))
	if err != nil {
		t.Fatalf("Failed to create keyfunc from JWKS response: %v", err)
	}

	// Create a test token using the server
	tokenReq := TokenRequest{
		IssuerID:      "spiffe://test.example.com/issuer",
		Region:        "us-west-1",
		CloudTenantID: "test-tenant",
		Domain:        "com",
		ProductName:   "test-product",
		Audiences:     []string{"spiffe://test.example.com/audience"},
		TTL:           "1h",
	}

	reqBody, _ := json.Marshal(tokenReq)
	tokenReqHTTP, _ := http.NewRequest("POST", "/token", bytes.NewBuffer(reqBody))
	tokenReqHTTP.Header.Set("Content-Type", "application/json")

	tokenRR := httptest.NewRecorder()
	server.ServeHTTP(tokenRR, tokenReqHTTP)

	if tokenRR.Code != http.StatusOK {
		t.Fatalf("Failed to create test token: %v", tokenRR.Code)
	}

	var tokenResp TokenResponse
	json.Unmarshal(tokenRR.Body.Bytes(), &tokenResp)

	// Now verify the JWKS can validate the token
	keyFunc := func(token *jwt.Token) (interface{}, error) {
		return testKeyfunc.Keyfunc(token)
	}

	parsedToken, err := jwt.Parse(tokenResp.AccessToken, keyFunc)
	if err != nil {
		t.Fatalf("JWKS failed to validate token: %v", err)
	}

	if !parsedToken.Valid {
		t.Fatal("Token should be valid when verified with JWKS")
	}
}

func TestJWKSEndpointMethodNotAllowed(t *testing.T) {
	server := setupTestServer(t)

	req, err := http.NewRequest(http.MethodPost, "/.well-known/jwks.json", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusMethodNotAllowed {
		t.Errorf("handler returned wrong status code: got %v want %v (response: %s)",
			status, http.StatusMethodNotAllowed, rr.Body.String())
	}
}

func TestTokenEndpoint(t *testing.T) {
	server := setupTestServer(t)

	tokenReq := TokenRequest{
		IssuerID:      "spiffe://test.example.com/issuer",
		Region:        "us-west-1",
		CloudTenantID: "test-tenant",
		Domain:        "com",
		ProductName:   "test-product",
		ResourceName:  "test-uuid-123",
		Audiences:     []string{"spiffe://test.example.com/audience"},
		TTL:           "1h",
		CustomClaims: map[string]interface{}{
			"role": "test-role",
			"env":  "testing",
		},
	}

	reqBody, err := json.Marshal(tokenReq)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, "/token", bytes.NewBuffer(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
		t.Errorf("Response body: %s", rr.Body.String())
	}

	// Parse response
	var response TokenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse token response: %v", err)
	}

	// Verify response structure
	if response.TokenType != "Bearer" {
		t.Errorf("Expected token_type 'Bearer', got %v", response.TokenType)
	}

	if response.AccessToken == "" {
		t.Fatal("access_token should not be empty")
	}

	// Verify JWT structure (without validating signature for now)
	parts := strings.Split(response.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT should have 3 parts, got %d", len(parts))
	}

	// Verify issuer in response matches request
	if response.Issuer != tokenReq.IssuerID {
		t.Errorf("Expected issuer %v, got %v", tokenReq.IssuerID, response.Issuer)
	}

	// Verify subject SPIFFE ID format
	subject := response.Subject

	expectedSubject := "spiffe://us-west-1.test-tenant.nvidia.com/test-product/vm/test-uuid-123"
	if subject != expectedSubject {
		t.Errorf("Expected subject %v, got %v", expectedSubject, subject)
	}
}

func TestTokenEndpointMissingFields(t *testing.T) {
	server := setupTestServer(t)

	testCases := []struct {
		name        string
		request     TokenRequest
		expectedErr string
	}{
		{
			name: "missing issuer_id",
			request: TokenRequest{
				Region: "us-west-1",
			},
			expectedErr: "issuer_id is required",
		},
		{
			name: "missing region",
			request: TokenRequest{
				IssuerID: "spiffe://test.example.com/issuer",
			},
			expectedErr: "region is required",
		},
		{
			name: "missing cloud_tenant_id",
			request: TokenRequest{
				IssuerID: "spiffe://test.example.com/issuer",
				Region:   "us-west-1",
			},
			expectedErr: "cloud_tenant_id is required",
		},
		{
			name: "missing audiences",
			request: TokenRequest{
				IssuerID:      "spiffe://test.example.com/issuer",
				Region:        "us-west-1",
				CloudTenantID: "test-tenant",
				Domain:        "com",
				ProductName:   "test-product",
				TTL:           "1h",
			},
			expectedErr: "audiences is required",
		},
		{
			name: "invalid ttl",
			request: TokenRequest{
				IssuerID:      "spiffe://test.example.com/issuer",
				Region:        "us-west-1",
				CloudTenantID: "test-tenant",
				Domain:        "com",
				ProductName:   "test-product",
				Audiences:     []string{"spiffe://test.example.com/audience"},
				TTL:           "invalid",
			},
			expectedErr: "Invalid TTL format",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reqBody, err := json.Marshal(tc.request)
			if err != nil {
				t.Fatal(err)
			}

			req, err := http.NewRequest(http.MethodPost, "/token", bytes.NewBuffer(reqBody))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			server.ServeHTTP(rr, req)

			if status := rr.Code; status != http.StatusBadRequest {
				t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
			}

			if !strings.Contains(rr.Body.String(), tc.expectedErr) {
				t.Errorf("Expected error message containing '%s', got '%s'", tc.expectedErr, rr.Body.String())
			}
		})
	}
}

func TestValidateEndpoint(t *testing.T) {
	server := setupTestServer(t)

	// First, create a token
	tokenReq := TokenRequest{
		IssuerID:      "spiffe://test.example.com/issuer",
		Region:        "us-west-1",
		CloudTenantID: "test-tenant",
		Domain:        "com",
		ProductName:   "test-product",
		Audiences:     []string{"spiffe://test.example.com/audience"},
		TTL:           "1h",
		CustomClaims: map[string]interface{}{
			"role": "test-role",
		},
	}

	reqBody, err := json.Marshal(tokenReq)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, "/token", bytes.NewBuffer(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("Failed to create token: %v", status)
	}

	var tokenResponse TokenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &tokenResponse); err != nil {
		t.Fatalf("Failed to parse token response: %v", err)
	}

	// Now validate the token
	validateReq := ValidateRequest{
		Token: tokenResponse.AccessToken,
	}

	reqBody, err = json.Marshal(validateReq)
	if err != nil {
		t.Fatal(err)
	}

	req, err = http.NewRequest("POST", "/validate", bytes.NewBuffer(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
		t.Errorf("Response body: %s", rr.Body.String())
	}

	var validateResponse ValidateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &validateResponse); err != nil {
		t.Fatalf("Failed to parse validate response: %v", err)
	}

	if !validateResponse.Valid {
		t.Errorf("Expected valid=true, got %v", validateResponse.Valid)
	}

	if validateResponse.Issuer != tokenReq.IssuerID {
		t.Errorf("Expected issuer %v, got %v", tokenReq.IssuerID, validateResponse.Issuer)
	}
}

func TestValidateEndpointInvalidToken(t *testing.T) {
	server := setupTestServer(t)

	validateReq := ValidateRequest{
		Token: "invalid.jwt.token",
	}

	reqBody, err := json.Marshal(validateReq)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, "/validate", bytes.NewBuffer(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusUnauthorized {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusUnauthorized)
	}

	var response ValidateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Valid {
		t.Errorf("Expected valid=false, got %v", response.Valid)
	}
}

func TestValidateEndpointMissingToken(t *testing.T) {
	server := setupTestServer(t)

	validateReq := ValidateRequest{}

	reqBody, err := json.Marshal(validateReq)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, "/validate", bytes.NewBuffer(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
	}
}

func TestHealthEndpoint(t *testing.T) {
	server := setupTestServer(t)

	req, err := http.NewRequest(http.MethodGet, "/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}
}

func TestGenerateSPIFFEID(t *testing.T) {
	testCases := []struct {
		name          string
		region        string
		cloudTenantID string
		domain        string
		productName   string
		vmUUID        string
		expected      string
	}{
		{
			name:          "US domain",
			region:        "US-WEST-1",
			cloudTenantID: "Test-Tenant",
			domain:        "COM",
			productName:   "Test-Product",
			vmUUID:        "test-uuid",
			expected:      "spiffe://us-west-1.test-tenant.nvidia.com/test-product/vm/test-uuid",
		},
		{
			name:          "China domain",
			region:        "BJS-1",
			cloudTenantID: "prod-cluster",
			domain:        "cn",
			productName:   "storage",
			vmUUID:        "uuid-123",
			expected:      "spiffe://bjs-1.prod-cluster.nvidia.cn/storage/vm/uuid-123",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := GenerateSPIFFEID(tc.region, tc.cloudTenantID, tc.domain, tc.productName, tc.vmUUID)
			if err != nil {
				t.Fatalf("GenerateSPIFFEID failed: %v", err)
			}

			if result.String() != tc.expected {
				t.Errorf("Expected %s, got %s", tc.expected, result.String())
			}
		})
	}
}

func TestEndToEndTokenFlow(t *testing.T) {
	server := setupTestServer(t)

	// 1. Create a token
	tokenReq := TokenRequest{
		IssuerID:      "spiffe://e2e.example.com/issuer",
		Region:        "us-east-1",
		CloudTenantID: "e2e-tenant",
		Domain:        "com",
		ProductName:   "e2e-product",
		Audiences:     []string{"spiffe://e2e.example.com/service"},
		TTL:           "30m",
		CustomClaims: map[string]interface{}{
			"test": "e2e",
		},
	}

	reqBody, _ := json.Marshal(tokenReq)
	req, _ := http.NewRequest(http.MethodPost, "/token", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Token creation failed: %v", rr.Code)
	}

	var tokenResponse TokenResponse
	json.Unmarshal(rr.Body.Bytes(), &tokenResponse)
	token := tokenResponse.AccessToken

	// 2. Validate the token
	validateReq := ValidateRequest{Token: token}
	reqBody, _ = json.Marshal(validateReq)
	req, _ = http.NewRequest(http.MethodPost, "/validate", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")

	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Token validation failed: %v", rr.Code)
	}

	var validateResponse ValidateResponse
	json.Unmarshal(rr.Body.Bytes(), &validateResponse)

	// 3. Verify the claims match
	if !validateResponse.Valid {
		t.Error("Token should be valid")
	}
	if validateResponse.Issuer != tokenReq.IssuerID {
		t.Error("Issuer mismatch")
	}

	// 4. Verify custom claims
	if validateResponse.Claims["test"] != "e2e" {
		t.Error("Custom claims mismatch")
	}

	// 5. Get JWKS and verify it can validate the token
	req, _ = http.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rr = httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("JWKS retrieval failed: %v", rr.Code)
	}

	// Verify JWKS can validate the token we created
	jwksKeyfunc, err := keyfunc.NewJWKSetJSON(json.RawMessage(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("Failed to create keyfunc from JWKS: %v", err)
	}

	keyFunc := func(token *jwt.Token) (interface{}, error) {
		return jwksKeyfunc.Keyfunc(token)
	}

	verifiedToken, err := jwt.Parse(token, keyFunc)
	if err != nil {
		t.Fatalf("JWKS failed to verify token: %v", err)
	}

	if !verifiedToken.Valid {
		t.Error("Token should be valid when verified with JWKS")
	}
}
