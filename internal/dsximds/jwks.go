// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dsximds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/sync/singleflight"
)

type remoteKeySet struct {
	ctx        context.Context
	url        string
	client     *http.Client
	algorithms []jose.SignatureAlgorithm
	keys       atomic.Pointer[jose.JSONWebKeySet]
	fetch      singleflight.Group
	mu         sync.Mutex
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

	r.mu.Lock()
	if current := r.keys.Load(); current != cached {
		r.mu.Unlock()
		return verifyWithKeys(token, current)
	}
	result := r.fetch.DoChan("", func() (any, error) {
		keys, err := r.fetchKeys()
		r.mu.Lock()
		// Cache hits can return before waiting callers wake. Retire this fetch
		// before publishing its keys so later misses cannot join it.
		r.fetch.Forget("")
		if err == nil {
			r.keys.Store(keys)
		}
		r.mu.Unlock()
		return keys, err
	})
	r.mu.Unlock()
	// The shared fetch uses the HTTP timeout. Each caller can cancel its wait.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return nil, result.Err
		}
		return verifyWithKeys(token, result.Val.(*jose.JSONWebKeySet))
	}
}

func (r *remoteKeySet) fetchKeys() (*jose.JSONWebKeySet, error) {
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
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
	return &keys, nil
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
