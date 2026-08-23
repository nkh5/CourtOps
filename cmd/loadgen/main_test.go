package main

import (
	"io"
	"net/http"

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
