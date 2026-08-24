package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNearestRank(t *testing.T) {
	values := make([]time.Duration, 100)
	for i := range values {
		values[i] = time.Duration(i+1) * time.Millisecond
	}
	for _, tc := range []struct {
		p    float64
		want time.Duration
	}{{.5, 50 * time.Millisecond}, {.95, 95 * time.Millisecond}, {.99, 99 * time.Millisecond}, {1, 100 * time.Millisecond}} {
		if got := percentile(values, tc.p); got != tc.want {
			t.Errorf("p%g=%s, want %s", tc.p, got, tc.want)
		}
	}
	if percentile(nil, .95) != 0 {
		t.Fatal("empty sample")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }
func TestFailuresAreNotSuccessfulLatencySamples(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    io.ReadCloser
		err     error
		success int
	}{
		{"ok", 200, io.NopCloser(strings.NewReader("board")), nil, 1},
		{"server error", 500, io.NopCloser(strings.NewReader("error")), nil, 0},
		{"redirect", 302, io.NopCloser(strings.NewReader("")), nil, 0},
		{"truncated body", 200, brokenBody{}, nil, 0},
		{"connection failed", 0, nil, errors.New("offline"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &collector{}
			client := &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: tc.body}, tc.err
			})}
			poll(context.Background(), client, "http://example.test/board/1", c)
			r := c.report(time.Second)
			if r.Requests != 1 || r.Successes != tc.success || r.Errors != 1-tc.success {
				t.Fatalf("unexpected report: %+v", r)
			}
		})
	}
}
func TestValidationRejectsInvalidWorkloads(t *testing.T) {
	base := options{mode: "http", target: "http://localhost/board/1", viewers: 1, interval: time.Second, duration: time.Second, timeout: time.Second}
	for _, change := range []func(*options){func(o *options) { o.interval = 0 }, func(o *options) { o.duration = -1 }, func(o *options) { o.timeout = 0 }, func(o *options) { o.viewers = 0 }, func(o *options) { o.maxErrorRate = 1.1 }, func(o *options) { o.target = "ftp://localhost" }, func(o *options) { o.target = "http://user:secret@localhost" }, func(o *options) { o.mode = "typo" }} {
		o := base
		change(&o)
		if o.validate() == nil {
			t.Errorf("accepted %+v", o)
		}
	}
}
