package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreateEvent inserts an event and its courts in one transaction.
func (s *Store) CreateEvent(ctx context.Context, title string, date time.Time, courtLabels []string) (Event, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Event{}, invalid("an event needs a title")
	}
	if date.IsZero() {
		return Event{}, invalid("an event needs a date")
	}

	labels, err := cleanCourtLabels(courtLabels)
	if err != nil {
		return Event{}, err
	}

	var event Event
	err = s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO events (title, event_date)
			VALUES ($1, $2)
			RETURNING id, title, event_date, state, created_at, updated_at`,
			title, date).
			Scan(&event.ID, &event.Title, &event.Date, &event.State, &event.CreatedAt, &event.UpdatedAt)
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}

		for _, label := range labels {
			if _, err := tx.Exec(ctx,
				`INSERT INTO courts (event_id, label) VALUES ($1, $2)`, event.ID, label); err != nil {
				return constraintConflict(fmt.Errorf("insert court %q: %w", label, err))
			}
		}
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	return event, nil
}

func cleanCourtLabels(labels []string) ([]string, error) {
	out := make([]string, 0, len(labels))
	seen := map[string]bool{}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if seen[strings.ToLower(l)] {
			return nil, invalid("court %q is listed twice", l)
		}
		seen[strings.ToLower(l)] = true
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, invalid("an event needs at least one court")
	}
	if len(out) > 8 {
		return nil, invalid("this version supports at most 8 courts")
	}
	return out, nil
}

// GetEvent reads one event without locking it.
func (s *Store) GetEvent(ctx context.Context, id int64) (Event, error) {
	return getEvent(ctx, s.pool, id)
}

type eventReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getEvent(ctx context.Context, reader eventReader, id int64) (Event, error) {
	var e Event
	err := reader.QueryRow(ctx, `
		SELECT id, title, event_date, state, created_at, updated_at
		FROM events WHERE id = $1`, id).
		Scan(&e.ID, &e.Title, &e.Date, &e.State, &e.CreatedAt, &e.UpdatedAt)
	if err == pgx.ErrNoRows {
		return Event{}, notFound("event %d does not exist", id)
	}
	if err != nil {
		return Event{}, fmt.Errorf("get event: %w", err)
	}
	return e, nil
}

// ListEvents returns events newest first, for the small index page.
func (s *Store) ListEvents(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, title, event_date, state, created_at, updated_at
		FROM events ORDER BY event_date DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Title, &e.Date, &e.State, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetEventState moves an event between setup, live and finished.
//
// Finishing an event with a live match is refused rather than silently
// cancelling games that are still being played.
func (s *Store) SetEventState(ctx context.Context, eventID int64, want string) (Event, error) {
	switch want {
	case EventSetup, EventLive, EventFinished:
	default:
		return Event{}, invalid("unknown event state %q", want)
	}

	var out Event
	err := s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		event, err := lockEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if event.State == want {
			out = event // already there; repeating the command is harmless
			return nil
		}
		if event.State == EventFinished && want != EventFinished {
			return conflict("this event is finished; reopening it is not supported in this version")
		}
		if event.State == EventLive && want == EventSetup {
			return conflict("this event has started; it cannot return to setup")
		}

		if want != EventLive {
			var live int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM matches WHERE event_id = $1 AND state = 'active'`, eventID).
				Scan(&live); err != nil {
				return fmt.Errorf("count live matches: %w", err)
			}
			if live > 0 {
				return precondition("finish or cancel the %d match(es) still on court first", live)
			}
		}


		err = tx.QueryRow(ctx, `
			UPDATE events SET state = $2, updated_at = now(),
                started_at = CASE WHEN $2 = 'live' THEN statement_timestamp() ELSE started_at END,
                finished_at = CASE WHEN $2 = 'finished' THEN statement_timestamp() ELSE finished_at END
			WHERE id = $1
			RETURNING id, title, event_date, state, created_at, updated_at`, eventID, want).
			Scan(&out.ID, &out.Title, &out.Date, &out.State, &out.CreatedAt, &out.UpdatedAt)
		if err != nil {
			return fmt.Errorf("update event state: %w", err)
		}
		return nil
	})
	return out, err
}

// AddCourt adds a court to an existing event.
func (s *Store) AddCourt(ctx context.Context, eventID int64, label string) (Court, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return Court{}, invalid("a court needs a label")
	}

	var c Court
	err := s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableEvent(ctx, tx, eventID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT label FROM courts WHERE event_id = $1`, eventID)
		if err != nil {
			return fmt.Errorf("read court labels: %w", err)
		}
		labels := []string{label}
		for rows.Next() {
			var existing string
			if err := rows.Scan(&existing); err != nil {
				rows.Close()
				return err
			}
			labels = append(labels, existing)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := cleanCourtLabels(labels); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO courts (event_id, label) VALUES ($1, $2)
			RETURNING id, event_id, label, available`, eventID, label).
			Scan(&c.ID, &c.EventID, &c.Label, &c.Available)
		if err != nil {
			return constraintConflict(fmt.Errorf("insert court: %w", err))
		}
		return touchEvent(ctx, tx, eventID)
	})
	return c, err
}

// SetCourtAvailable opens or closes a court. Closing a court with a live match
// is refused; cancel the match first.
func (s *Store) SetCourtAvailable(ctx context.Context, eventID, courtID int64, available bool) (Court, error) {
	var c Court
	err := s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableEvent(ctx, tx, eventID); err != nil {
			return err
		}
		court, err := courtInEvent(ctx, tx, eventID, courtID)
		if err != nil {
			return err
		}
		if !available {
			var live int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM matches WHERE court_id = $1 AND state = 'active'`, courtID).
				Scan(&live); err != nil {
				return fmt.Errorf("count court matches: %w", err)
			}
			if live > 0 {
				return precondition("court %s has a live match; finish or cancel it first", court.Label)
			}
		}

		err = tx.QueryRow(ctx, `
			UPDATE courts SET available = $2 WHERE id = $1
			RETURNING id, event_id, label, available`, courtID, available).
			Scan(&c.ID, &c.EventID, &c.Label, &c.Available)
		if err != nil {
			return fmt.Errorf("update court: %w", err)
		}
		return touchEvent(ctx, tx, eventID)
	})
	return c, err
}

// courtInEvent loads a court and checks that it belongs to the event named in
// the URL. Foreign keys guarantee the court exists; only the service can check
// that the caller is talking about the right event.
func courtInEvent(ctx context.Context, tx pgx.Tx, eventID, courtID int64) (Court, error) {
	var c Court
	err := tx.QueryRow(ctx, `
		SELECT id, event_id, label, available FROM courts WHERE id = $1`, courtID).
		Scan(&c.ID, &c.EventID, &c.Label, &c.Available)
	if err == pgx.ErrNoRows {
		return Court{}, notFound("court %d does not exist", courtID)
	}
	if err != nil {
		return Court{}, fmt.Errorf("get court: %w", err)
	}
	if c.EventID != eventID {
		return Court{}, notFound("court %d is not part of event %d", courtID, eventID)
	}
	return c, nil
}
