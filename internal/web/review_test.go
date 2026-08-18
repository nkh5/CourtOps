package web

import (
	"bytes"

	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestMalformedAndOversizedActionFormsCannotMutate(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
	}{
		{"malformed escaping", "available=true&bad=%zz", http.StatusBadRequest},
		{"oversized", "available=true&padding=" + strings.Repeat("x", 70<<10), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := reviewServer(t)
			r := signedForm(t, s, "/events/1/courts/1/availability", "application/x-www-form-urlencoded", strings.NewReader(tt.body))
			r.Header.Set(csrfHeader, strings.Repeat("c", 64))
			w := httptest.NewRecorder()
			s.organizerOnly(func(http.ResponseWriter, *http.Request) { t.Error("invalid form reached handler") }).ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestImportConfirmationAllowsEncodingExpansionAndPreservesLines(t *testing.T) {
	s := reviewServer(t)
	raw := "\nName,Email,Skill,Notes\nAna,a@example.com,Beginner," + strings.Repeat("界", 200000) + "\n"
	form := url.Values{"csrf_token": {strings.Repeat("c", 64)}, "csv": {raw}, "confirm": {"1"}}
	encoded := form.Encode()
	if len(raw) >= roster.MaxBytes || len(encoded) <= roster.MaxBytes {
		t.Fatal("fixture does not exercise encoding expansion")
	}
	r := signedForm(t, s, "/events/1/import", "application/x-www-form-urlencoded", strings.NewReader(encoded))
	w := httptest.NewRecorder()
	called := false
	s.organizerOnly(func(w http.ResponseWriter, r *http.Request) {
		called = true
		got, err := s.readUpload(r)
		if err != nil {
			t.Fatal(err)
		}
		if got != raw {
			t.Fatal("upload changed the CSV bytes or line positions")
		}
		parsed, err := roster.Parse(strings.NewReader(got))
		if err != nil || !parsed.OK() || parsed.Entries[0].Row != 3 {
			t.Fatalf("parsed=%+v err=%v", parsed, err)
		}
	}).ServeHTTP(w, r)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
