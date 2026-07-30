package dpu

import (
	"context"
	"errors"
	"fmt"
)

// EvidenceVerifier validates the agent attestation payload and extracts the
// raw (un-normalized) DPU serial.
type EvidenceVerifier interface {
	// VerifyAndExtractSerial inspects the agent payload and returns the raw
	// serial it asserts. The returned serial is not normalized.
	VerifyAndExtractSerial(ctx context.Context, payload []byte) (rawSerial string, err error)
}

// PlaintextVerifier treats the payload as the raw serial without cryptographic
// verification. Suitable for development wiring only.
type PlaintextVerifier struct{}

// VerifyAndExtractSerial returns the payload bytes as the raw serial.
func (PlaintextVerifier) VerifyAndExtractSerial(_ context.Context, payload []byte) (string, error) {
	if len(payload) == 0 {
		return "", errors.New("attestation payload is empty")
	}
	if len(payload) > MaxSerialLen {
		return "", fmt.Errorf("attestation payload length %d exceeds maximum %d", len(payload), MaxSerialLen)
	}
	return string(payload), nil
}
