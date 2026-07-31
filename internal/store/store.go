// Package store owns every read and write of the CourtOps database.
//
// It is deliberately not a generic repository. Each exported method is one
// organizer action, and every action that changes an event follows the same
// rule: open a transaction, lock the event row with SELECT ... FOR UPDATE, read
// the current state, validate, write, commit. Because the lock is taken first,
// two organizers pressing the same button at the same time are serialized by
// PostgreSQL instead of racing each other.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is a handle on the database plus the small amount of policy that
// depends on deployment (whether ratings are being persisted yet).
type Store struct {
	pool *pgxpool.Pool

	// RatingsEnabled controls whether completing a match writes rating changes.
	// It is off for the first pilot so that a confusing evening cannot bake bad
	// numbers into player history.
	RatingsEnabled bool
}

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// A single small host serving one club: a wide pool would only queue work
	// in a different place.
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases every pooled connection.
func (s *Store) Close() { s.pool.Close() }

// Ping is used by the health check.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Pool exposes the pool for migrations and tests in this package's own module.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// inTx runs fn inside one transaction on one connection.
//
// The deferred rollback is a no-op once Commit has succeeded, so the happy path
// costs nothing and every early return still releases the locks.
func (s *Store) inTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	return s.inTxOptions(ctx, pgx.TxOptions{}, fn)
}

func (s *Store) inTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(context.Context, pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		// Use a fresh context: if the request was cancelled, the rollback still
		// needs to run.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// lockEditableEvent also prevents stale organizer pages from changing a
// finished evening's attendance or court configuration.
func lockEditableEvent(ctx context.Context, tx pgx.Tx, eventID int64) (Event, error) {
	event, err := lockEvent(ctx, tx, eventID)
	if err != nil {
		return Event{}, err
	}
	if event.State == EventFinished {
		return Event{}, precondition("this event is finished; its players and courts cannot be changed")
	}
	return event, nil
}

// lockEvent takes the event's row lock for the rest of the transaction and
// returns the event as it exists right now.
//
// Every mutating method starts here. Holding one short lock per event is what
// makes "read current state, then decide" safe: another writer waits rather
// than reading the same stale state.
func lockEvent(ctx context.Context, tx pgx.Tx, eventID int64) (Event, error) {
	var e Event
	err := tx.QueryRow(ctx, `
		SELECT id, title, event_date, state, created_at, updated_at
		FROM events
		WHERE id = $1
		FOR UPDATE`, eventID).
		Scan(&e.ID, &e.Title, &e.Date, &e.State, &e.CreatedAt, &e.UpdatedAt)
	if err == pgx.ErrNoRows {
		return Event{}, notFound("event %d does not exist", eventID)
	}
	if err != nil {
		return Event{}, fmt.Errorf("lock event %d: %w", eventID, err)
	}
	return e, nil
}

// touchEvent bumps updated_at so clients can tell that something changed.
func touchEvent(ctx context.Context, tx pgx.Tx, eventID int64) error {
	_, err := tx.Exec(ctx, `UPDATE events SET updated_at = now() WHERE id = $1`, eventID)
	if err != nil {
		return fmt.Errorf("touch event: %w", err)
	}
	return nil
}
