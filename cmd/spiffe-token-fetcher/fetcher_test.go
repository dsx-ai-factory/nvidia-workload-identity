package main

import (
	"testing"
	"time"
)

func TestParseSocketPath(t *testing.T) {
	tests := []struct {
		name       string
		socketPath string
		wantPath   string
		wantErr    bool
	}{
		{
			name:       "valid unix socket",
			socketPath: "unix:///var/run/spire.sock",
			wantPath:   "/var/run/spire.sock",
			wantErr:    false,
		},
		{
			name:       "valid unix socket nested path",
			socketPath: "unix:///var/run/secrets/spire.io/spire-agent.sock",
			wantPath:   "/var/run/secrets/spire.io/spire-agent.sock",
			wantErr:    false,
		},
		{
			name:       "tcp scheme not supported",
			socketPath: "tcp://127.0.0.1:8080",
			wantPath:   "",
			wantErr:    true,
		},
		{
			name:       "http scheme not supported",
			socketPath: "http://localhost",
			wantPath:   "",
			wantErr:    true,
		},
		{
			name:       "empty path",
			socketPath: "unix://",
			wantPath:   "",
			wantErr:    true,
		},
		{
			name:       "invalid url",
			socketPath: "://invalid",
			wantPath:   "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := parseSocketPath(tt.socketPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func TestCalculateRefreshTime(t *testing.T) {
	tests := []struct {
		name      string
		issuedAt  time.Time
		expiresAt time.Time
		wantMin   time.Duration
		wantMax   time.Duration
	}{
		{
			name:      "1 hour lifetime",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(1 * time.Hour),
			wantMin:   47 * time.Minute, // ~80% of 1 hour
			wantMax:   49 * time.Minute,
		},
		{
			name:      "5 minute lifetime",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(5 * time.Minute),
			wantMin:   3*time.Minute + 50*time.Second, // ~80% of 5 min
			wantMax:   4*time.Minute + 10*time.Second,
		},
		{
			name:      "short lifetime uses minimum",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(20 * time.Second),
			wantMin:   30 * time.Second, // minimum is 30s
			wantMax:   30 * time.Second,
		},
		{
			name:      "very short lifetime uses minimum",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(10 * time.Second),
			wantMin:   30 * time.Second,
			wantMax:   30 * time.Second,
		},
		{
			name:      "24 hour lifetime",
			issuedAt:  time.Now(),
			expiresAt: time.Now().Add(24 * time.Hour),
			wantMin:   19*time.Hour + 10*time.Minute, // ~80% of 24 hours
			wantMax:   19*time.Hour + 20*time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateRefreshTime(tt.issuedAt, tt.expiresAt)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("got = %v, want between %v and %v", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestCalculateRefreshTime_Exact80Percent(t *testing.T) {
	issuedAt := time.Now()
	expiresAt := issuedAt.Add(100 * time.Second)

	got := calculateRefreshTime(issuedAt, expiresAt)

	// 80% of 100s = 80s, which is > 30s minimum
	expected := 80 * time.Second
	if got != expected {
		t.Errorf("got = %v, want %v", got, expected)
	}
}

func TestCalculateRefreshTime_MinimumEnforced(t *testing.T) {
	issuedAt := time.Now()
	expiresAt := issuedAt.Add(35 * time.Second)

	got := calculateRefreshTime(issuedAt, expiresAt)

	// 80% of 35s = 28s, but minimum is 30s
	expected := 30 * time.Second
	if got != expected {
		t.Errorf("got = %v, want %v (minimum enforced)", got, expected)
	}
}
