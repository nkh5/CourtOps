// Package testdb creates a throwaway PostgreSQL database for a test.
//
// It lives in its own package so the store tests and the HTTP tests can share
// one definition of "a real, empty, migrated database".
package testdb

import (
	"context"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nicholashu/courtops/internal/store"
)

// EnvVar names the server these tests may create databases on.
const EnvVar = "TEST_DATABASE_URL"

// New returns a store backed by a fresh database with every migration applied.
// The database is dropped when the test finishes.
//
// Tests are skipped when EnvVar is unset, so `go test ./...` still works on a
// machine with no PostgreSQL while CI runs the full set.
func New(t *testing.T) *store.Store {
	t.Helper()

	admin := os.Getenv(EnvVar)
	if admin == "" {
		t.Skipf("%s is not set; skipping tests that need a database", EnvVar)
	}

	ctx := context.Background()
	name := fmt.Sprintf("courtops_test_%d_%d", time.Now().UnixNano()%1e9, rand.Intn(10000))

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to the test server: %v", err)
	}
	_, err = conn.Exec(ctx, `CREATE DATABASE "`+name+`"`)
	conn.Close(ctx)
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}

	dsn, err := withDatabase(admin, name)
	if err != nil {
		t.Fatalf("build test dsn: %v", err)
	}

	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	t.Cleanup(func() {
		st.Close()
		conn, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer conn.Close(context.Background())
		_, _ = conn.Exec(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
	})

	return st
}

func withDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}
