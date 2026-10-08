// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto"
	"crypto/elliptic"
	_ "crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/go-jose/go-jose/v4"
)

func TestGenerateKeyset(t *testing.T) {
	t.Parallel()
	result, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Keys) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Algorithm != jose.ES256 {
		t.Fatalf("algorithm = %q, want %q", result.Algorithm, jose.ES256)
	}
	block, rest := pem.Decode([]byte(result.Keys[0]))
	if block == nil || len(rest) != 0 || block.Type != "EC PRIVATE KEY" {
		t.Fatalf("invalid generated PEM: %q", result.Keys[0])
	}
	privateKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if privateKey.Curve != elliptic.P256() || privateKey.D.Sign() <= 0 {
		t.Fatalf("invalid generated private key: %#v", privateKey)
	}
	public := jose.JSONWebKey{Key: &privateKey.PublicKey}
	thumbprint, err := public.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if want := base64.RawURLEncoding.EncodeToString(thumbprint); result.ActiveKID != want {
		t.Fatalf("active kid = %q, want public JWK thumbprint %q", result.ActiveKID, want)
	}
	other, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	if other.ActiveKID == result.ActiveKID {
		t.Fatal("independently generated keys reused a kid")
	}
}

func TestAddKey(t *testing.T) {
	t.Parallel()

	initial, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	result, err := add(bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Keys) != 2 || result.Keys[0] != initial.Keys[0] {
		t.Fatalf("added keyset = %#v, want original key followed by a new key", result)
	}
	if result.ActiveKID != initial.ActiveKID || result.Algorithm != jose.ES256 {
		t.Fatalf("added keyset = %#v, want original active ES256 key", result)
	}
	ids, err := thumbprints(result.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != initial.ActiveKID || ids[0] == ids[1] {
		t.Fatalf("thumbprints = %v, want original and new unique kids", ids)
	}
}

func TestRejectInvalidAddInput(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"empty":              `{}`,
		"malformed":          `{"keys":[`,
		"unsupported alg":    `{"active_kid":"kid","alg":"RS256","keys":["key"]}`,
		"missing active kid": `{"active_kid":"missing","alg":"ES256","keys":[]}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := add(bytes.NewBufferString(input)); err == nil {
				t.Fatal("invalid keyset was accepted")
			}
		})
	}
}

func TestCommands(t *testing.T) {
	t.Parallel()

	initial, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"generate", "add", "thumbprints"} {
		result, err := execute(command, bytes.NewReader(input))
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if result == nil {
			t.Fatalf("%s returned no result", command)
		}
	}
	if _, err := execute("invalid", bytes.NewReader(input)); err == nil {
		t.Fatal("unknown command was accepted")
	}
}
