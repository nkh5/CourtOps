package store

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nicholashu/courtops/migrations"
)

// Migration is one versioned SQL file.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// LoadMigrations reads the embedded SQL files and returns them in version order.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || path.Ext(name) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q must be named NNNN_description.sql", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version: %w", name, err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", other, name, version)
		}
		seen[version] = name

		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", name, err)
		}
		out = append(out, Migration{Version: version, Name: name, SQL: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migrate applies every migration that has not been applied yet and returns
// the names of the ones it ran.
//
// Each migration runs in its own transaction together with the row that records
// it, so a failure halfway through a file leaves no partial version marker.
// Migrating an empty database and migrating a current one are the same code
// path; the second one simply has nothing to do.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	all, err := LoadMigrations()
	if err != nil {
		return nil, err
	}

	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER     PRIMARY KEY,
			name       TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}

	var ran []string
	for _, m := range all {
		if applied[m.Version] {
			continue
		}
		if err := s.applyOne(ctx, m); err != nil {
			return ran, err
		}
		ran = append(ran, m.Name)
	}
	return ran, nil
}

func (s *Store) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func (s *Store) applyOne(ctx context.Context, m Migration) error {
	return s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// Take an advisory lock so two instances starting at once cannot both
		// try to apply the same file.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4827301)`); err != nil {
			return fmt.Errorf("migration lock: %w", err)
		}

		var already bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, m.Version).
			Scan(&already); err != nil {
			return fmt.Errorf("check migration %s: %w", m.Name, err)
		}
		if already {
			return nil
		}

		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.Name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name); err != nil {
			return fmt.Errorf("record migration %s: %w", m.Name, err)
		}
		return nil
	})
}
