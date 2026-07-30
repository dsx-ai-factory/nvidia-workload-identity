package dpu

import "testing"

func TestValidateTrustDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		td      string
		want    string
		wantErr bool
	}{
		{name: "valid domain", td: "example.org", want: "example.org"},
		{name: "trims whitespace", td: "  example.org  ", want: "example.org"},
		{name: "empty rejected", td: "", wantErr: true},
		{name: "whitespace-only rejected", td: "   ", wantErr: true},
		{name: "slash rejected", td: "example.org/foo", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateTrustDomain(tc.td)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateTrustDomain(%q) = %q, want error", tc.td, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateTrustDomain(%q) unexpected error: %v", tc.td, err)
			}
			if got != tc.want {
				t.Fatalf("ValidateTrustDomain(%q) = %q, want %q", tc.td, got, tc.want)
			}
		})
	}
}
