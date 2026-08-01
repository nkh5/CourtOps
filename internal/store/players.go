package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/nicholashu/courtops/internal/roster"
)

// AddPlayer registers one player during setup or a live event. Existing shared
// identities keep their display name and rating so registering them cannot
// alter a match in another event. A duplicate membership is refused unchanged.
func (s *Store) AddPlayer(ctx context.Context, eventID int64, entry roster.Entry, checkIn bool) (EventPlayer, error) {
	paddle := ""
	if entry.NeedsPaddle != nil {
		paddle = "no"
		if *entry.NeedsPaddle {
			paddle = "yes"
		}
	}
	entry, err := roster.NewEntry(entry.DisplayName, entry.PrivateRef, entry.Band, entry.EstimatedArrival, paddle)
	if err != nil {
		return EventPlayer{}, invalid("%s", err)
	}
	var out EventPlayer
	err = s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableEvent(ctx, tx, eventID); err != nil {
			return err
		}
		var playerID int64
		err := tx.QueryRow(ctx, `
			INSERT INTO players (display_name, private_ref, skill_seed, rating)
			VALUES ($1, $2, $3::int, $3::int)
			ON CONFLICT (private_ref) DO UPDATE SET private_ref = players.private_ref
			RETURNING id`, entry.DisplayName, entry.PrivateRef, entry.Seed).Scan(&playerID)
		if err != nil {
			return constraintConflict(fmt.Errorf("register player identity: %w", err))
		}

		// statement_timestamp is taken after the event lock was acquired, unlike
		// now() which could predate other check-ins while this transaction waited.
		tag, err := tx.Exec(ctx, `
			INSERT INTO event_players (event_id, player_id, state, ready_since, checked_in_at, estimated_arrival, needs_paddle)
			VALUES ($1, $2, CASE WHEN $3 THEN 'ready' ELSE 'resting' END,
				CASE WHEN $3 THEN statement_timestamp() ELSE NULL END,
				CASE WHEN $3 THEN statement_timestamp() ELSE NULL END, $4, $5)
			ON CONFLICT (event_id, player_id) DO NOTHING`,
			eventID, playerID, checkIn, entry.EstimatedArrival, entry.NeedsPaddle)
		if err != nil {
			return fmt.Errorf("register player for event: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return conflict("this player is already on the event roster; use their check-in control instead")
		}
		out, err = member(ctx, tx, eventID, playerID)
		if err != nil {
			return err
		}
		return touchEvent(ctx, tx, eventID)
	})
	if err != nil {
		return EventPlayer{}, err
	}
	return out, nil
}

// RegistrationDetails must only be used by organizer handlers. Keeping these
// answers out of the public snapshot avoids accidental disclosure in polling.
func (s *Store) RegistrationDetails(ctx context.Context, eventID int64) (map[int64]Registration, error) {
	rows, err := s.pool.Query(ctx, `SELECT player_id, estimated_arrival, needs_paddle
		FROM event_players WHERE event_id = $1`, eventID)
	if err != nil {
		return nil, fmt.Errorf("read registration details: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]Registration)
	for rows.Next() {
		var id int64
		var registration Registration
		if err := rows.Scan(&id, &registration.EstimatedArrival, &registration.NeedsPaddle); err != nil {
			return nil, fmt.Errorf("scan registration details: %w", err)
		}
		out[id] = registration
	}
	return out, rows.Err()
}

// SetPlayerState changes one player's state in one event.
//
// expected, when non-empty, is the state the organizer believed the player was
// in. If someone else has moved the player since the page was rendered, the
// write is refused with a conflict instead of quietly overwriting the newer
// state. A repeat of the same command is a no-op, which is what makes the
// button safe to press twice.
func (s *Store) SetPlayerState(ctx context.Context, eventID, playerID int64, want, expected string) (EventPlayer, error) {
	switch want {
	case StateReady, StateResting, StateLeft:
	case StatePlaying:
		return EventPlayer{}, invalid("players are put on court by assigning a match, not by setting a state")
	default:
		return EventPlayer{}, invalid("unknown player state %q", want)
	}

	var out EventPlayer
	err := s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableEvent(ctx, tx, eventID); err != nil {
			return err
		}

		current, err := memberForUpdate(ctx, tx, eventID, playerID)
		if err != nil {
			return err
		}
		if current.State == StatePlaying {
			return precondition("%s is on court; complete or cancel that match first", current.DisplayName)
		}
		if current.State == want {
			// Already there. Returning the current row keeps a double-click and
			// a retried request indistinguishable from a single press, and the
			// existing ready_since is preserved so the queue does not reshuffle.
			out = current
			return nil
		}
		if expected != "" && current.State != expected {
			return conflict("%s is now %s, not %s; the page you acted on was out of date",
				current.DisplayName, current.State, expected)
		}

		const q = `
			UPDATE event_players SET
				state = $3,
				ready_since   = CASE WHEN $3 = 'ready' THEN statement_timestamp() ELSE NULL END,
				checked_in_at = CASE WHEN $3 = 'ready' THEN COALESCE(checked_in_at, statement_timestamp()) ELSE checked_in_at END
			WHERE event_id = $1 AND player_id = $2`
		if _, err := tx.Exec(ctx, q, eventID, playerID, want); err != nil {
			return fmt.Errorf("update player state: %w", err)
		}

		if out, err = member(ctx, tx, eventID, playerID); err != nil {
			return err
		}
		return touchEvent(ctx, tx, eventID)
	})
	return out, err
}

const memberColumns = `
	SELECT p.id, p.display_name, ep.state, ep.ready_since, ep.checked_in_at, p.rating, p.rated_games
	FROM event_players ep JOIN players p ON p.id = ep.player_id
	WHERE ep.event_id = $1 AND ep.player_id = $2`

func member(ctx context.Context, tx pgx.Tx, eventID, playerID int64) (EventPlayer, error) {
	return scanMember(tx.QueryRow(ctx, memberColumns, eventID, playerID), playerID, eventID)
}

func memberForUpdate(ctx context.Context, tx pgx.Tx, eventID, playerID int64) (EventPlayer, error) {
	// The event row lock already serializes writers; locking the membership row
	// as well keeps the read consistent if a future version ever widens that.
	return scanMember(tx.QueryRow(ctx, memberColumns+` FOR UPDATE OF ep`, eventID, playerID), playerID, eventID)
}

func scanMember(row pgx.Row, playerID, eventID int64) (EventPlayer, error) {
	var p EventPlayer
	err := row.Scan(&p.PlayerID, &p.DisplayName, &p.State, &p.ReadySince, &p.CheckedInAt, &p.Rating, &p.RatedGames)
	if err == pgx.ErrNoRows {
		return EventPlayer{}, notFound("player %d is not on event %d's roster", playerID, eventID)
	}
	if err != nil {
		return EventPlayer{}, fmt.Errorf("read event player: %w", err)
	}
	return p, nil
}

// CheckInAll marks every resting player ready. It is a convenience for the
// start of an evening, and it deliberately does not touch anyone who has left
// or is on court.
func (s *Store) CheckInAll(ctx context.Context, eventID int64) (int, error) {
	var n int
	err := s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableEvent(ctx, tx, eventID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE event_players
			SET state = 'ready', ready_since = statement_timestamp(), checked_in_at = COALESCE(checked_in_at, statement_timestamp())
			WHERE event_id = $1 AND state = 'resting'`, eventID)
		if err != nil {
			return fmt.Errorf("check in all: %w", err)
		}
		n = int(tag.RowsAffected())
		return touchEvent(ctx, tx, eventID)
	})
	return n, err
}
