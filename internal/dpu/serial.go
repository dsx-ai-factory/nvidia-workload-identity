// Package dpu implements the server half of the DPU SPIRE NodeAttestor
// (plugin name dpu_hw) and the serial / agent-ID helpers it needs.
package dpu

import (
	"fmt"
	"strings"
)

// MaxSerialLen is the maximum normalized serial length.
const MaxSerialLen = 64

// NormalizeSerial trims whitespace, lowercases, and validates that the serial
// is non-empty, at most MaxSerialLen, and contains only RFC 3986 unreserved
// characters (a-z, 0-9, "-._~"). Invalid characters are rejected rather than
// stripped. This matches the DPF dpu_hw NodeAttestor serial policy.
func NormalizeSerial(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", fmt.Errorf("serial is empty after trimming")
	}
	if len(s) > MaxSerialLen {
		return "", fmt.Errorf("serial length %d exceeds maximum %d", len(s), MaxSerialLen)
	}
	for _, r := range s {
		if !isUnreservedSerialRune(r) {
			return "", fmt.Errorf("serial contains invalid character %q (allowed: RFC 3986 unreserved)", r)
		}
	}
	return s, nil
}

func isUnreservedSerialRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '.' || r == '_' || r == '~'
}
