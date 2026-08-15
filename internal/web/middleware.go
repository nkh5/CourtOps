package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/nicholashu/courtops/internal/roster"
	"github.com/nicholashu/courtops/internal/store"
)

type ctxKey int

const sessionKey ctxKey = 1

// organizerOnly guards every organizer route.
//
// The order matters: identity first, then the read-only switch, then CSRF. A
// signed-out visitor should be told to sign in rather than being told their
// token is wrong.
func (s *Server) organizerOnly(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessionFrom(r)
		if !ok {
			s.requireSignIn(w, r)
			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if s.cfg.ReadOnly {
				s.respondError(w, r, http.StatusServiceUnavailable,
					"This deployment is read-only: it is a demonstration with synthetic data.")
				return
			}
			if !s.parseActionForm(w, r) {
				return
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			if err := s.checkCSRF(r); err != nil {
				s.log.Warn("csrf check failed", "path", r.URL.Path, "error", err)
				s.respondError(w, r, http.StatusForbidden, err.Error())
				return
			}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

// Bound the body before CSRF reads form fields. A limit in the upload handler
// is too late: PostFormValue can already have parsed the entire multipart body.
func (s *Server) parseActionForm(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, "Submit a form with a valid Content-Type.")
		return false
	}
	limit := int64(64 << 10)
	if strings.HasSuffix(r.URL.Path, "/import") {
		limit = roster.MaxBytes + 4096
		if mediaType == "application/x-www-form-urlencoded" {
			// A byte can expand to three characters when the preview's CSV is
			// percent-encoded. The decoded file still has its own 1 MB limit.
			limit = 3*roster.MaxBytes + 4096
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	switch mediaType {
	case "multipart/form-data":
		err = r.ParseMultipartForm(roster.MaxBytes)
	case "application/x-www-form-urlencoded":
		err = r.ParseForm()
	default:
		s.respondError(w, r, http.StatusUnsupportedMediaType, "Submit a URL-encoded or multipart form.")
		return false
	}
	if err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.respondError(w, r, http.StatusRequestEntityTooLarge, "The submitted form is too large.")
		} else {
			s.respondError(w, r, http.StatusBadRequest, "The submitted form could not be read.")
		}
		return false
	}
	return true
}

// sessionOf returns the signed-in organizer, if any.
func sessionOf(r *http.Request) (Session, bool) {
	sess, ok := r.Context().Value(sessionKey).(Session)
	return sess, ok
}

func (s *Server) requireSignIn(w http.ResponseWriter, r *http.Request) {
	if wantsJSON(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in as an organizer"})
		return
	}
	if r.Method == http.MethodGet {
		http.Redirect(w, r, "/?return="+r.URL.Path, http.StatusSeeOther)
		return
	}
	s.respondError(w, r, http.StatusUnauthorized, "Your session has expired. Sign in again and retry.")
}

// logRequests writes one structured line per request: method, route, status,
// duration. Roster data never appears in a log line, so a log file is not a
// second copy of the members' email addresses.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		level := slogLevelFor(rec.status)
		s.log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", float64(time.Since(start).Microseconds())/1000,
		)
	})
}

// withTimeout bounds every request. The database calls inherit this context, so
// a slow query is cancelled rather than piling up connections.
func (s *Server) withTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverPanics keeps one broken handler from taking down an event.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic in handler",
					"path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				s.respondError(w, r, http.StatusInternalServerError,
					"Something went wrong. The organizer page will show the current state if you reload.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func slogLevelFor(status int) slogLevel {
	switch {
	case status >= 500:
		return levelError
	case status >= 400:
		return levelWarn
	default:
		return levelInfo
	}
}

// storeError maps a store error to the right status code and a message the
// organizer can act on. Anything unrecognised is logged and reported as a 500
// without leaking internals to the page.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch store.KindOf(err) {
	case store.KindInvalid:
		s.respondError(w, r, http.StatusBadRequest, store.Message(err))
	case store.KindNotFound:
		s.respondError(w, r, http.StatusNotFound, store.Message(err))
	case store.KindConflict:
		// 409 is the honest answer to "someone else changed this first".
		s.respondError(w, r, http.StatusConflict, store.Message(err))
	case store.KindPrecondition:
		s.respondError(w, r, http.StatusUnprocessableEntity, store.Message(err))
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "error", err)
	s.respondError(w, r, http.StatusInternalServerError,
		"Something went wrong on the server. Reload the page to see the current state.")
}

// respondError answers in whichever form the caller asked for.
func (s *Server) respondError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if wantsJSON(r) {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	s.renderError(w, r, status, msg)
}

func wantsJSON(r *http.Request) bool {
	if strings.HasSuffix(r.URL.Path, ".json") {
		return true
	}
	if r.Header.Get(csrfHeader) != "" && r.Header.Get("Accept") == "" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent; there is nothing useful left to do.
		return
	}
}
