package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nicholashu/courtops/internal/roster"
)

// ImportRoster adds every entry to the event in one transaction.
//
// Two rules make reimporting safe:
//
//   - A player is identified by private_ref, not by display name, so the same
//     person imported into a second event is the same row and keeps their
//     history.
//   - A self-reported skill band never overwrites a rating a player has
//     actually earned. The band only seeds someone who has not played a rated
//     game yet.
//
// Import is refused once an event is live: changing the roster underneath a
// running queue is how a rotation silently loses someone.
func (s *Store) ImportRoster(ctx context.Context, eventID int64, entries []roster.Entry) (ImportResult, error) {
	if len(entries) == 0 {
		return ImportResult{}, invalid("there is nothing to import")
	}

	var res ImportResult
	err := s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		event, err := lockEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if event.State != EventSetup {
			return precondition("this event is %s; roster import is only allowed during setup", event.State)
		}

		// Reset the counters so a retried transaction cannot double-count.
		res = ImportResult{}

		for _, e := range entries {
			e.PrivateRef = strings.ToLower(strings.TrimSpace(e.PrivateRef))
			e.EstimatedArrival = strings.TrimSpace(e.EstimatedArrival)
			var (
				playerID   int64
				ratedGames int
				created    bool
			)
			// The RETURNING clause tells us whether this was an insert: xmax is
			// 0 on a freshly inserted row.
			err := tx.QueryRow(ctx, `
				INSERT INTO players (display_name, private_ref, skill_seed, rating)
				-- $3 is cast explicitly: the same value seeds an integer column and a
				-- double precision one, and PostgreSQL will not deduce both from one
				-- untyped parameter.
				VALUES ($1, $2, $3::int, $3::int)
				ON CONFLICT (private_ref) DO UPDATE SET
					display_name = EXCLUDED.display_name,
					-- Only re-seed someone who has never been rated.
					skill_seed = CASE WHEN players.rated_games = 0
					                  THEN EXCLUDED.skill_seed ELSE players.skill_seed END,
					rating     = CASE WHEN players.rated_games = 0
					                  THEN EXCLUDED.rating ELSE players.rating END
				RETURNING id, rated_games, (xmax = 0)`,
				e.DisplayName, e.PrivateRef, e.Seed).
				Scan(&playerID, &ratedGames, &created)
			if err != nil {
				return constraintConflict(fmt.Errorf("row %d (%s): %w", e.Row, e.DisplayName, err))
			}
			if created {
				res.PlayersCreated++
			} else {
				res.PlayersMatched++
			}

			tag, err := tx.Exec(ctx, `
				INSERT INTO event_players (event_id, player_id, state, estimated_arrival, needs_paddle)
				VALUES ($1, $2, 'resting', $3, $4)
				ON CONFLICT (event_id, player_id) DO NOTHING`, eventID, playerID, e.EstimatedArrival, e.NeedsPaddle)
			if err != nil {
				return fmt.Errorf("row %d (%s): add to event: %w", e.Row, e.DisplayName, err)
			}
			if tag.RowsAffected() == 1 {
				res.MembershipsAdded++
			} else {
				res.MembershipsSkipped++
				// A corrected CSV can refresh registration planning without
				// changing attendance or the player's place in the queue.
				if _, err := tx.Exec(ctx, `UPDATE event_players SET estimated_arrival = $3, needs_paddle = $4
					WHERE event_id = $1 AND player_id = $2`, eventID, playerID, e.EstimatedArrival, e.NeedsPaddle); err != nil {
					return fmt.Errorf("row %d: update registration details: %w", e.Row, err)
				}
			}
		}

		return touchEvent(ctx, tx, eventID)
	})
	if err != nil {
		return ImportResult{}, err
	}
	return res, nil
}
