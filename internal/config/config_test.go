package config

import (
	"strings"
	"testing"
)

// base is a configuration that would run in production, so each test can change
// exactly one thing and see whether validate still accepts it.
func base() Config {
	return Config{
		Env:               EnvProduction,
		Addr:              ":8080",
		BaseURL:           "https://courtops.example.com",
		DatabaseURL:       "postgres://courtops:pw@db:5432/courtops",
		SessionSecret:     []byte(strings.Repeat("k", 32)),
		AllowedOrganizers: []string{"organizer@example.com"},
		Google: GoogleConfig{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RedirectURL:  "https://courtops.example.com/auth/google/callback",
		},
	}
}

func TestValidProductionConfig(t *testing.T) {
	if err := base().validate(); err != nil {
		t.Fatalf("a complete production config was rejected: %v", err)
	}
}

// The rule this protects is from the build plan: never put organizer controls
// on the public internet without real sign-in.
func TestProductionRefusesUnsafeCombinations(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{
			name:   "no Google client and not read-only",
			change: func(c *Config) { c.Google = GoogleConfig{} },
			want:   "GOOGLE_CLIENT_ID",
		},
		{
			name:   "development sign-in left on",
			change: func(c *Config) { c.DevOrganizer = "me@example.com" },
			want:   "COURTOPS_DEV_ORGANIZER",
		},
		{
			name:   "short session secret",
			change: func(c *Config) { c.SessionSecret = []byte("too-short") },
			want:   "SESSION_SECRET",
		},
		{
			name:   "no organizers listed",
			change: func(c *Config) { c.AllowedOrganizers = nil },
			want:   "COURTOPS_ORGANIZERS",
		},
		{
			name:   "plain http base url",
			change: func(c *Config) { c.BaseURL = "http://courtops.example.com" },
			want:   "https",
		},
		{
			name:   "no database",
			change: func(c *Config) { c.DatabaseURL = "" },
			want:   "DATABASE_URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.change(&cfg)
			err := cfg.validate()
			if err == nil {
				t.Fatal("expected this configuration to be refused")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A read-only demo is the one production deployment allowed without sign-in,
// because it cannot be made to write anything.
func TestReadOnlyDemoNeedsNoLogin(t *testing.T) {
	cfg := base()
	cfg.Google = GoogleConfig{}
	cfg.AllowedOrganizers = nil
	cfg.SessionSecret = nil
	cfg.ReadOnly = true

	if err := cfg.validate(); err != nil {
		t.Fatalf("a read-only demo was rejected: %v", err)
	}
}
