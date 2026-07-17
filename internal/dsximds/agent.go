package dsximds

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Bound the response before converting it to an in-memory attestation payload.
const maxTokenSize = 1 << 20

// FetchHostToken obtains host attestation data through the shared tokenless
// DSX IMDS v1 endpoint.
func FetchHostToken(ctx context.Context, client *http.Client, endpoint, audience string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("HTTP client is required")
	}
	if audience == "" {
		return "", fmt.Errorf("audience is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create IMDS request: %w", err)
	}
	req.URL.Path = "/latest/meta-data/identity"
	query := req.URL.Query()
	query.Set("aud", audience)
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Metadata", "true")
	// Node attestation sends only the compact JWT. The server verifies its signed expiry.
	req.Header.Set("Accept", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request host identity: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request host identity: unexpected HTTP status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenSize+1))
	if err != nil {
		return "", fmt.Errorf("read host identity: %w", err)
	}
	if len(body) == 0 {
		return "", fmt.Errorf("host identity response is empty")
	}
	if len(body) > maxTokenSize {
		return "", fmt.Errorf("host identity response exceeds %d bytes", maxTokenSize)
	}
	return string(body), nil
}
