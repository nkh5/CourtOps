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

func TestDevelopmentIsPermissive(t *testing.T) {
	cfg := Config{
		Env:          EnvDevelopment,
		DatabaseURL:  "postgres://localhost/courtops",
		DevOrganizer: "dev@example.com",
		BaseURL:      "http://localhost:8080",
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("local development config was rejected: %v", err)
	}
}

func TestGoogleLoginWithoutOrganizersIsRefused(t *testing.T) {
	cfg := base()
	cfg.Env = EnvDevelopment
	cfg.AllowedOrganizers = nil

	err := cfg.validate()
	if err == nil {
		t.Fatal("expected a refusal: sign-in would be configured but nobody could use it")
	}
	if !strings.Contains(err.Error(), "nobody could sign in") {
		t.Errorf("error = %q", err)
	}
}

func TestAllowsOrganizerIgnoresCaseAndSpace(t *testing.T) {
	cfg := Config{AllowedOrganizers: []string{"organizer@example.com"}}
	for _, email := range []string{"organizer@example.com", "Organizer@Example.com", "  organizer@example.com  "} {
		if !cfg.AllowsOrganizer(email) {
			t.Errorf("AllowsOrganizer(%q) = false, want true", email)
		}
	}
	if cfg.AllowsOrganizer("someone.else@example.com") {
		t.Error("an unlisted account was allowed")
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" A@example.com ,b@example.com,, ")
	want := []string{"a@example.com", "b@example.com"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

func TestInvalidSafetyFlagsAreRefused(t *testing.T) {
	for _, key := range []string{"COURTOPS_READ_ONLY", "COURTOPS_RATINGS"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("COURTOPS_READ_ONLY", "")
			t.Setenv("COURTOPS_RATINGS", "")
			t.Setenv(key, "tru")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("invalid %s was not reported: %v", key, err)
			}
		})
	}
}
