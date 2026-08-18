package web

import (
	"bytes"

	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"

	"strings"

	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/config"
	"github.com/nicholashu/courtops/internal/roster"
)

func reviewServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(config.Config{Env: config.EnvDevelopment, BaseURL: "http://localhost", SessionSecret: []byte(strings.Repeat("s", 32)), AllowedOrganizers: []string{"test@example.com"}}, nil, slog.New(slog.DiscardHandler), "test")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func signedForm(t *testing.T, s *Server, path, contentType string, body io.Reader) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, body)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Accept", "application/json")
	session, err := encodeSession(s.cfg.SessionSecret, Session{Subject: "test", Email: "test@example.com", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: strings.Repeat("c", 64)})
	return r
}

func TestOversizedMultipartIsRejectedBeforeCSRF(t *testing.T) {
	s := reviewServer(t)
	var b bytes.Buffer
	form := multipart.NewWriter(&b)
	file, err := form.CreateFormFile("file", "roster.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(file, strings.NewReader(strings.Repeat("x", 2*roster.MaxBytes))); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	r := signedForm(t, s, "/events/1/import", form.FormDataContentType(), &b)
	w := httptest.NewRecorder()
	s.organizerOnly(func(http.ResponseWriter, *http.Request) { t.Error("oversized upload reached handler") }).ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
