package postgres

import (
	"testing"
)

func TestConfig_DSN(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		expected string
	}{
		{
			name: "explicit sslmode",
			cfg: Config{
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				User:     "testuser",
				Password: "testpass",
				SSLMode:  "require",
			},
			expected: "host=localhost port=5432 dbname=testdb user=testuser password=testpass sslmode=require",
		},
		{
			name: "default sslmode",
			cfg: Config{
				Host:     "localhost",
				Port:     5432,
				Database: "testdb",
				User:     "testuser",
				Password: "testpass",
			},
			expected: "host=localhost port=5432 dbname=testdb user=testuser password=testpass sslmode=disable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.DSN(); got != tc.expected {
				t.Errorf("DSN() = %q, want %q", got, tc.expected)
			}
		})
	}
}
