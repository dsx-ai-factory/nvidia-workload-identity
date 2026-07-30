package dpu

import (
	"context"
	"strings"
	"testing"
)

func TestPlaintextVerifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload []byte
		want    string
		wantErr bool
	}{
		{name: "valid payload", payload: []byte("mt2152x00abc"), want: "mt2152x00abc"},
		{name: "empty payload rejected", payload: []byte{}, wantErr: true},
		{name: "oversized payload rejected", payload: []byte(strings.Repeat("a", MaxSerialLen+1)), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := (PlaintextVerifier{}).VerifyAndExtractSerial(context.Background(), tc.payload)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("VerifyAndExtractSerial() = %q, want error", got)
				}
				if got != "" {
					t.Fatalf("VerifyAndExtractSerial() returned %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyAndExtractSerial() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("VerifyAndExtractSerial() = %q, want %q", got, tc.want)
			}
		})
	}
}
