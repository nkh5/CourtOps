package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DeleteEvent permanently removes a finished event and its event-specific
// records. Shared player identities, ratings and other events are untouched.
//
// A rated result cannot be erased safely: later results may already depend on
// the ratings it produced. Retain those events until a separate rating-history
// retention or replay policy is implemented, even if ratings are now disabled.
func (s *Store) DeleteEvent(ctx context.Context, eventID int64) error {
	return s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		event, err := lockEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if event.State != EventFinished {
			return precondition("only finished events can be deleted; finish this event first")
		}

		var active, rated bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM matches WHERE event_id = $1 AND state = 'active'),
			       EXISTS (SELECT 1 FROM matches m JOIN match_players mp ON mp.match_id = m.id
			               WHERE m.event_id = $1 AND mp.rating_after IS NOT NULL)`, eventID).
			Scan(&active, &rated); err != nil {
			return fmt.Errorf("check event deletion: %w", err)
		}
		if active {
			return precondition("this event still has a live match and cannot be deleted")
		}
		if rated {
			return precondition("this event has saved Elo results and cannot be deleted because later ratings may depend on them")
		}

		// Match players cascade from matches. Remove matches before courts since
		// matches.court_id deliberately does not cascade. Keeping all four
		// deletes in this transaction also means any constraint failure restores
		// the complete event, rather than leaving a partially deleted history.
		for _, table := range []string{"matches", "event_players", "courts"} {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE event_id = $1`, eventID); err != nil {
				return fmt.Errorf("delete event %s: %w", table, err)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM events WHERE id = $1`, eventID); err != nil {
			return fmt.Errorf("delete event: %w", err)
		}
		return nil
	})
}
