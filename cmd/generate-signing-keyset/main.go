package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/go-jose/go-jose/v4"

	"dsx-identity/internal/signingkey"
	"dsx-identity/internal/telemetry"
)

type generatedKeyset struct {
	ActiveKID string                  `json:"active_kid"`
	Algorithm jose.SignatureAlgorithm `json:"alg"`
	Keys      []string                `json:"keys"`
}

type thumbprintDocument struct {
	Thumbprints []string `json:"thumbprints"`
}

func generateKey() (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate P-256 key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshal EC private key: %w", err)
	}
	encoded := string(pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: der,
	}))
	parsed, err := signingkey.ParsePrivatePEM(encoded)
	if err != nil {
		return "", "", err
	}
	return encoded, parsed.KeyID, nil
}

func generate() (generatedKeyset, error) {
	encoded, kid, err := generateKey()
	if err != nil {
		return generatedKeyset{}, err
	}
	return generatedKeyset{ActiveKID: kid, Algorithm: jose.ES256, Keys: []string{encoded}}, nil
}

func decode(input io.Reader) (generatedKeyset, error) {
	var document generatedKeyset
	decoder := json.NewDecoder(input)
	if err := decoder.Decode(&document); err != nil {
		return generatedKeyset{}, fmt.Errorf("read signing keyset: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return generatedKeyset{}, fmt.Errorf("read signing keyset: trailing JSON data")
	}
	return document, nil
}

func thumbprints(keys []string) ([]string, error) {
	result := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for i, encoded := range keys {
		key, err := signingkey.ParsePrivatePEM(encoded)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		if _, ok := seen[key.KeyID]; ok {
			return nil, fmt.Errorf("duplicate kid %q", key.KeyID)
		}
		seen[key.KeyID] = struct{}{}
		result = append(result, key.KeyID)
	}
	return result, nil
}

func add(input io.Reader) (generatedKeyset, error) {
	document, err := decode(input)
	if err != nil {
		return generatedKeyset{}, err
	}
	if document.Algorithm != jose.ES256 {
		return generatedKeyset{}, fmt.Errorf("algorithm %q is not supported for generated keys", document.Algorithm)
	}
	ids, err := thumbprints(document.Keys)
	if err != nil {
		return generatedKeyset{}, err
	}
	if len(ids) == 0 {
		return generatedKeyset{}, fmt.Errorf("signing keyset contains no keys")
	}
	foundActive := false
	for i, encoded := range document.Keys {
		key, err := signingkey.ParsePrivatePEM(encoded)
		if err != nil {
			return generatedKeyset{}, fmt.Errorf("key %d: %w", i, err)
		}
		if _, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key.Key}, nil); err != nil {
			return generatedKeyset{}, fmt.Errorf("key %d is incompatible with ES256: %w", i, err)
		}
		foundActive = foundActive || ids[i] == document.ActiveKID
	}
	if !foundActive {
		return generatedKeyset{}, fmt.Errorf("active kid %q not found in signing keyset", document.ActiveKID)
	}
	encoded, _, err := generateKey()
	if err != nil {
		return generatedKeyset{}, err
	}
	document.Keys = append(document.Keys, encoded)
	return document, nil
}

func execute(command string, input io.Reader) (any, error) {
	switch command {
	case "generate":
		return generate()
	case "add":
		return add(input)
	case "thumbprints":
		document, err := decode(input)
		if err != nil {
			return nil, err
		}
		ids, err := thumbprints(document.Keys)
		return thumbprintDocument{Thumbprints: ids}, err
	default:
		return nil, fmt.Errorf("unknown command %q", command)
	}
}

func run(args []string, input io.Reader, output, errors io.Writer) error {
	var logLevel slog.Level
	flags := flag.NewFlagSet("generate-signing-keyset", flag.ContinueOnError)
	flags.SetOutput(errors)
	flags.TextVar(&logLevel, "log-level", slog.LevelInfo, "log level (debug, info, warn, error)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("usage: generate-signing-keyset [--log-level level] <generate|add|thumbprints>")
	}

	if _, err := telemetry.Setup(context.Background(), "generate-signing-keyset", errors, logLevel); err != nil {
		return fmt.Errorf("configure OpenTelemetry: %w", err)
	}

	result, err := execute(flags.Arg(0), input)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return fmt.Errorf("write signing keyset: %w", err)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		slog.Error("Run signing keyset generator", "error", err)
		os.Exit(1)
	}
}
