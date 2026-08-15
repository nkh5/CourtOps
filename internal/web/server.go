// Package web serves the organizer controls, the public court board and the
// small JSON API behind them.
//
// There are two audiences and they are kept apart on purpose: anything under
// /events requires a signed-in organizer, and anything under /board is public
// and is rendered only from data that contains no private identifiers.
package web

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nicholashu/courtops/internal/config"
	"github.com/nicholashu/courtops/internal/store"
	"github.com/nicholashu/courtops/static"
	"github.com/nicholashu/courtops/templates"
)

// Server holds everything the handlers need. It is built once at start-up.
type Server struct {
	cfg     config.Config
	store   *store.Store
	log     *slog.Logger
	tmpl    *template.Template
	auth    *googleAuth
	version string
	handler http.Handler
	started time.Time
}

// New parses the templates and wires the routes.
func New(cfg config.Config, st *store.Store, log *slog.Logger, version string) (*Server, error) {
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseFS(templates.FS, "*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	s := &Server{
		cfg:     cfg,
		store:   st,
		log:     log,
		tmpl:    tmpl,
		version: version,
		started: time.Now(),
	}
	if cfg.Google.Configured() {
		s.auth = &googleAuth{
			clientID:     cfg.Google.ClientID,
			clientSecret: cfg.Google.ClientSecret,
			redirectURL:  cfg.Google.RedirectURL,
		}
	}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /board/{eventID}", s.handleBoard)
	mux.HandleFunc("GET /board/{eventID}/state.json", s.handleBoardJSON)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static.FS)))

	// Sign-in.
	mux.HandleFunc("GET /auth/google/login", s.handleLogin)
	mux.HandleFunc("GET /auth/google/callback", s.handleCallback)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /auth/dev/login", s.handleDevLogin)

	// Organizer pages and actions. organizerOnly also enforces CSRF and the
	// read-only switch on every non-GET request.
	organizer := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.organizerOnly(h))
	}
	organizer("POST /events", s.handleCreateEvent)
	organizer("GET /events/{eventID}", s.handleOrganizer)
	organizer("GET /events/{eventID}/delete", s.handleDeleteConfirmation)
	organizer("POST /events/{eventID}/delete", s.handleDeleteEvent)
	organizer("GET /events/{eventID}/players/new", s.handleNewPlayer)
	organizer("POST /events/{eventID}/players", s.handleAddPlayer)
	organizer("GET /events/{eventID}/state.json", s.handleOrganizerJSON)
	organizer("POST /events/{eventID}/state", s.handleSetEventState)
	organizer("POST /events/{eventID}/import", s.handleImport)
	organizer("POST /events/{eventID}/checkin-all", s.handleCheckInAll)
	organizer("POST /events/{eventID}/courts", s.handleAddCourt)
	organizer("POST /events/{eventID}/courts/{courtID}/availability", s.handleCourtAvailability)
	organizer("POST /events/{eventID}/courts/{courtID}/next", s.handleAssignNext)
	organizer("POST /events/{eventID}/players/{playerID}/state", s.handleSetPlayerState)
	organizer("GET /events/{eventID}/matches/{matchID}/confirm", s.handleConfirmResult)
	organizer("POST /events/{eventID}/matches/{matchID}/complete", s.handleCompleteMatch)
	organizer("POST /events/{eventID}/matches/{matchID}/cancel", s.handleCancelMatch)

	return s.recoverPanics(s.logRequests(s.withTimeout(mux)))
}

// handleHealth is the only endpoint a load balancer or a phone check needs.
// It reports the database as part of health because an app that cannot reach
// PostgreSQL cannot do anything useful.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	body := map[string]any{
		"status":  "ok",
		"version": s.version,
		"uptime":  time.Since(s.started).Round(time.Second).String(),
	}
	status := http.StatusOK
	if err := s.store.Ping(ctx); err != nil {
		body["status"] = "degraded"
		body["database"] = "unreachable"
		status = http.StatusServiceUnavailable
		s.log.Error("health check: database unreachable", "error", err)
	} else {
		body["database"] = "ok"
	}
	writeJSON(w, status, body)
}

// pathInt reads a {name} path value as an int64.
func pathInt(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive number, got %q", name, raw)
	}
	return id, nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"waited": func(since *time.Time, now time.Time) string {
			if since == nil {
				return "—"
			}
			d := now.Sub(*since)
			if d < 0 {
				d = 0
			}
			if d < time.Minute {
				return fmt.Sprintf("%ds", int(d.Seconds()))
			}
			return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
		},
		// clock accepts time.Time or *time.Time so templates can print an
		// optional ended_at without a nil check at every call site.
		"clock": func(v any) string {
			switch t := v.(type) {
			case time.Time:
				return t.Local().Format("15:04:05")
			case *time.Time:
				if t == nil {
					return "—"
				}
				return t.Local().Format("15:04:05")
			default:
				return "—"
			}
		},
		"add":        func(a, b int) int { return a + b },
		"eventStats": eventStats,
		"paddle": func(v *bool) string {
			if v == nil {
				return "—"
			}
			if *v {
				return "Yes"
			}
			return "No"
		},
		// dict builds a map so a partial can take named arguments.
		"dict": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("dict needs an even number of arguments, got %d", len(pairs))
			}
			out := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs); i += 2 {
				key, ok := pairs[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict key %d is not a string", i)
				}
				out[key] = pairs[i+1]
			}
			return out, nil
		},
		"date":   displayDate,
		"rating": func(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) },
		"pdelta": func(p store.MatchPlayer) string {
			d := p.Delta()
			if d == nil {
				return ""
			}
			return fmt.Sprintf("%+.0f", *d)
		},
		"deref": func(v *int) int {
			if v == nil {
				return 0
			}
			return *v
		},
		"title": func(s string) string {
			if s == "" {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"teamOf": func(m store.Match, n int) []store.MatchPlayer { return m.Team(n) },
	}
}

// displayDate keeps club dates readable without changing their calendar day.
func displayDate(t time.Time) string {
	day := t.Day()
	suffix := "th"
	if day < 11 || day > 13 {
		switch day % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%s %d%s %d", t.Month(), day, suffix, t.Year())
}
