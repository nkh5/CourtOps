// Package config reads the process environment once, at start-up, and refuses
// to run with a combination of settings that would be unsafe in public.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment names.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Config is the whole configurable surface of the app.
type Config struct {
	Env         string
	Addr        string
	BaseURL     string
	DatabaseURL string

	// SessionSecret signs the organizer session cookie.
	SessionSecret []byte
	SessionTTL    time.Duration

	Google GoogleConfig

	// AllowedOrganizers is the list of Google account emails permitted to
	// operate an event. Everyone else is a spectator, signed in or not.
	AllowedOrganizers []string

	// DevOrganizer, when set outside production, signs in as that email without
	// contacting Google so the app can be built and rehearsed offline.
	DevOrganizer string

	// ReadOnly serves the board and refuses every mutation. This is how the
	// first public deploy stays safe before login works.
	ReadOnly bool

	// RatingsEnabled controls whether completing a match writes Elo changes.
	RatingsEnabled bool

	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
}

// GoogleConfig holds the OAuth 2.0 web-application client details.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Configured reports whether Google sign-in can be used.
func (g GoogleConfig) Configured() bool {
	return g.ClientID != "" && g.ClientSecret != "" && g.RedirectURL != ""
}

// Load builds a Config from the environment and validates it.
func Load() (Config, error) {
	readOnly, err := boolOr("COURTOPS_READ_ONLY", false)
	if err != nil {
		return Config{}, err
	}
	ratingsEnabled, err := boolOr("COURTOPS_RATINGS", false)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Env:             envOr("COURTOPS_ENV", EnvDevelopment),
		Addr:            envOr("COURTOPS_ADDR", ":8080"),
		BaseURL:         strings.TrimRight(envOr("COURTOPS_BASE_URL", "http://localhost:8080"), "/"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		SessionSecret:   []byte(os.Getenv("COURTOPS_SESSION_SECRET")),
		SessionTTL:      durationOr("COURTOPS_SESSION_TTL", 12*time.Hour),
		DevOrganizer:    os.Getenv("COURTOPS_DEV_ORGANIZER"),
		ReadOnly:        readOnly,
		RatingsEnabled:  ratingsEnabled,
		RequestTimeout:  durationOr("COURTOPS_REQUEST_TIMEOUT", 10*time.Second),
		ShutdownTimeout: durationOr("COURTOPS_SHUTDOWN_TIMEOUT", 15*time.Second),
		Google: GoogleConfig{
			ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
			ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
			RedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		},
		AllowedOrganizers: splitList(os.Getenv("COURTOPS_ORGANIZERS")),
	}

	if cfg.Google.RedirectURL == "" && cfg.Google.ClientID != "" {
		cfg.Google.RedirectURL = cfg.BaseURL + "/auth/google/callback"
	}

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var problems []string

	if c.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	switch c.Env {
	case EnvDevelopment, EnvProduction:
	default:
		problems = append(problems, fmt.Sprintf("COURTOPS_ENV must be %q or %q", EnvDevelopment, EnvProduction))
	}

	if c.Env == EnvProduction {
		// The rule from the build plan: never put organizer controls on the
		// public internet without real sign-in. A production deploy either has
		// Google login configured, or it is read-only.
		if !c.Google.Configured() && !c.ReadOnly {
			problems = append(problems,
				"production needs GOOGLE_CLIENT_ID/GOOGLE_CLIENT_SECRET, or COURTOPS_READ_ONLY=true for a demo deploy")
		}
		if c.DevOrganizer != "" {
			problems = append(problems, "COURTOPS_DEV_ORGANIZER must not be set in production")
		}
		if len(c.SessionSecret) < 32 && (!c.ReadOnly || c.Google.Configured()) {
			problems = append(problems, "COURTOPS_SESSION_SECRET must be at least 32 bytes in production")
		}
		if len(c.AllowedOrganizers) == 0 && !c.ReadOnly {
			problems = append(problems, "COURTOPS_ORGANIZERS must list at least one allowed Google account")
		}
	}
	if !validHTTPURL(c.BaseURL, c.IsProduction()) {
		problems = append(problems, "COURTOPS_BASE_URL must be an absolute http(s) URL (https in production), without credentials, query or fragment")
	}
	if c.Google.Configured() && !validHTTPURL(c.Google.RedirectURL, c.IsProduction()) {
		problems = append(problems, "GOOGLE_REDIRECT_URL must be an absolute http(s) URL (https in production), without credentials, query or fragment")
	}

	if c.Google.Configured() && len(c.AllowedOrganizers) == 0 {
		problems = append(problems, "Google login is configured but COURTOPS_ORGANIZERS is empty, so nobody could sign in")
	}

	if len(problems) > 0 {
		return fmt.Errorf("configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// IsProduction reports whether this process is serving the public deployment.
func (c Config) IsProduction() bool { return c.Env == EnvProduction }

// AllowsOrganizer reports whether an email may operate events.
func (c Config) AllowsOrganizer(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, allowed := range c.AllowedOrganizers {
		if allowed == email {
			return true
		}
	}
	return false
}

// ErrNoAuth is returned by the web layer when no sign-in method is available.
var ErrNoAuth = errors.New("no organizer sign-in method is configured")

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func boolOr(key string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("configuration: %s must be true or false", key)
	}
	return b, nil
}

func validHTTPURL(raw string, requireHTTPS bool) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Hostname() != "" && u.User == nil && u.RawQuery == "" &&
		!u.ForceQuery && u.Fragment == "" &&
		(u.Scheme == "https" || (!requireHTTPS && u.Scheme == "http"))
}

func durationOr(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
