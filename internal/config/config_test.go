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
