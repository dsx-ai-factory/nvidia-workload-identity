// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dsximds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/go-jose/go-jose/v4"
)

type remoteKeySet struct {
	url        string
	client     *http.Client
	algorithms []jose.SignatureAlgorithm
	keys       atomic.Pointer[jose.JSONWebKeySet]
	refresh    chan struct{}
}

func (r *remoteKeySet) VerifySignature(ctx context.Context, rawToken string) ([]byte, error) {
	token, err := jose.ParseSigned(rawToken, r.algorithms)
	if err != nil {
		return nil, err
	}
	cached := r.keys.Load()
	if payload, err := verifyWithKeys(token, cached); err == nil {
		return payload, nil
	}

	// Waiting callers can cancel without interrupting the active fetch.
	select {
	case r.refresh <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-r.refresh }()
	if current := r.keys.Load(); current != cached {
		return verifyWithKeys(token, current)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS endpoint returned %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read JWKS: %w", err)
	}
	var keys jose.JSONWebKeySet
	if err := json.Unmarshal(body, &keys); err != nil {
		return nil, fmt.Errorf("parse JWKS: %w", err)
	}
	// Publish the immutable snapshot before returning or releasing waiting callers.
	r.keys.Store(&keys)
	return verifyWithKeys(token, &keys)
}

func verifyWithKeys(token *jose.JSONWebSignature, keys *jose.JSONWebKeySet) ([]byte, error) {
	if keys == nil || len(token.Signatures) != 1 {
		return nil, jose.ErrCryptoFailure
	}
	kid := token.Signatures[0].Header.KeyID
	for _, key := range keys.Keys {
		if kid == "" || key.KeyID == kid {
			if payload, err := token.Verify(&key); err == nil {
				return payload, nil
			}
		}
	}
	return nil, jose.ErrCryptoFailure
}
