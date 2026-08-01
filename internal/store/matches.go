package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/nicholashu/courtops/internal/matchmaking"
	"github.com/nicholashu/courtops/internal/ratings"
)

// AssignNext puts the four longest-waiting ready players on a court.
//
// created is false when the court already had a live match. That is the whole
// idempotency story for this button: an organizer who presses "Next match"
// twice, or whose phone retries a request it never saw the answer to, gets the
// same match back instead of a second one.
//
// The event row is locked before eligibility is read, so two requests for the
// same court cannot both decide the court is free. The partial unique index on
// matches is the backstop if that lock is ever bypassed.
func (s *Store) AssignNext(ctx context.Context, eventID, courtID int64) (match Match, created bool, err error) {
	err = s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		event, err := lockEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if !event.IsLive() {
			return precondition("this event is %s; start it before assigning matches", event.State)
		}

		court, err := courtInEvent(ctx, tx, eventID, courtID)
		if err != nil {
			return err
		}

		if existing, err := activeMatchOnCourt(ctx, tx, courtID); err != nil {
			return err
		} else if existing != nil {
			match, created = *existing, false
			return nil
		}

		if !court.Available {
			return precondition("court %s is closed", court.Label)
		}

		candidates, err := readyCandidates(ctx, tx, eventID)
		if err != nil {
			return err
		}

		assignment, err := matchmaking.SelectNext(candidates)
		if errors.Is(err, matchmaking.ErrNotEnoughPlayers) {
			return precondition("only %d player(s) are ready; a doubles match needs %d",
				len(candidates), matchmaking.PlayersPerMatch)
		}
		if err != nil {
			return fmt.Errorf("select next match: %w", err)
		}

		var matchID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO matches (event_id, court_id, state)
			VALUES ($1, $2, 'active') RETURNING id`, eventID, courtID).Scan(&matchID); err != nil {
			return constraintConflict(fmt.Errorf("insert match: %w", err))
		}

		chosen := assignment.Players()
		for i, c := range chosen {
			team := 1
			if i >= 2 {
				team = 2
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO match_players (match_id, player_id, team, active, rating_before)
				VALUES ($1, $2, $3, TRUE, $4)`, matchID, c.PlayerID, team, c.Rating); err != nil {
				return constraintConflict(fmt.Errorf("add %s to match: %w", c.Name, err))
			}

			// Same transaction as the match row: a player is "playing" if and
			// only if there is a live match membership.
			tag, err := tx.Exec(ctx, `
				UPDATE event_players SET state = 'playing', ready_since = NULL
				WHERE event_id = $1 AND player_id = $2 AND state = 'ready'`, eventID, c.PlayerID)
			if err != nil {
				return fmt.Errorf("mark %s playing: %w", c.Name, err)
			}
			if tag.RowsAffected() != 1 {
				// Cannot happen while the event lock is held; if it ever does,
				// fail the whole assignment rather than start a bad match.
				return conflict("%s stopped being available while the match was being made", c.Name)
			}
		}

		if err := touchEvent(ctx, tx, eventID); err != nil {
			return err
		}

		full, err := matchInTx(ctx, tx, eventID, matchID)
		if err != nil {
			return err
		}
		match, created = full, true
		return nil
	})
	if err != nil {
		return Match{}, false, err
	}
	return match, created, nil
}

// CompleteMatch records the winner and, when ratings are enabled, applies the
// rating change for all four players in the same transaction.
//
// changed is false when the identical result was already saved. A different
// winner for a completed match is a conflict: this version does not edit a
// result once it has been confirmed, and pretending otherwise would mean
// replaying rating history.
func (s *Store) CompleteMatch(ctx context.Context, eventID, matchID int64, winningTeam int) (match Match, changed bool, err error) {
	if winningTeam != 1 && winningTeam != 2 {
		return Match{}, false, invalid("the winning team must be 1 or 2")
	}

	err = s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEvent(ctx, tx, eventID); err != nil {
			return err
		}

		current, err := matchForUpdate(ctx, tx, eventID, matchID)
		if err != nil {
			return err
		}

		switch current.State {
		case MatchCompleted:
			if current.WinningTeam != nil && *current.WinningTeam == winningTeam {
				match, changed = current, false // replay of the same result
				return nil
			}
			return conflict("this match was already recorded as a win for team %d; results cannot be edited in this version",
				*current.WinningTeam)
		case MatchCancelled:
			return conflict("this match was cancelled, so it has no result")
		}

		if _, err := tx.Exec(ctx, `
			UPDATE matches SET state = 'completed', winning_team = $2, ended_at = now()
			WHERE id = $1`, matchID, winningTeam); err != nil {
			return fmt.Errorf("complete match: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE match_players SET active = FALSE WHERE match_id = $1`, matchID); err != nil {
			return fmt.Errorf("release match players: %w", err)
		}

		if s.RatingsEnabled {
			if err := applyRatings(ctx, tx, current, winningTeam); err != nil {
				return err
			}
		}

		// Back into the queue at the end of the line.
		if err := releasePlayers(ctx, tx, eventID, current); err != nil {
			return err
		}
		if err := touchEvent(ctx, tx, eventID); err != nil {
			return err
		}

		full, err := matchInTx(ctx, tx, eventID, matchID)
		if err != nil {
			return err
		}
		match, changed = full, true
		return nil
	})
	if err != nil {
		return Match{}, false, err
	}
	return match, changed, nil
}

// CancelMatch releases the court and the players without recording a result.
// Nothing about ratings changes, because nothing was played.
func (s *Store) CancelMatch(ctx context.Context, eventID, matchID int64) (match Match, changed bool, err error) {
	err = s.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEvent(ctx, tx, eventID); err != nil {
			return err
		}

		current, err := matchForUpdate(ctx, tx, eventID, matchID)
		if err != nil {
			return err
		}
		switch current.State {
		case MatchCancelled:
			match, changed = current, false
			return nil
		case MatchCompleted:
			return conflict("this match already has a result and cannot be cancelled")
		}

		if _, err := tx.Exec(ctx,
			`UPDATE matches SET state = 'cancelled', ended_at = now() WHERE id = $1`, matchID); err != nil {
			return fmt.Errorf("cancel match: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE match_players SET active = FALSE WHERE match_id = $1`, matchID); err != nil {
			return fmt.Errorf("release match players: %w", err)
		}
		if err := releasePlayers(ctx, tx, eventID, current); err != nil {
			return err
		}
		if err := touchEvent(ctx, tx, eventID); err != nil {
			return err
		}

		full, err := matchInTx(ctx, tx, eventID, matchID)
		if err != nil {
			return err
		}
		match, changed = full, true
		return nil
	})
	if err != nil {
		return Match{}, false, err
	}
	return match, changed, nil
}

// applyRatings updates the four players and stores before/after on the
// membership rows. Player rows are locked in ID order so two events finishing
// matches that share a player cannot deadlock.
func applyRatings(ctx context.Context, tx pgx.Tx, m Match, winningTeam int) error {
	teamA := m.Team(1)
	teamB := m.Team(2)
	if len(teamA) != 2 || len(teamB) != 2 {
		return fmt.Errorf("match %d has %d/%d players per team, expected 2/2", m.ID, len(teamA), len(teamB))
	}

	ids := []int64{teamA[0].PlayerID, teamA[1].PlayerID, teamB[0].PlayerID, teamB[1].PlayerID}
	ordered := append([]int64(nil), ids...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })

	before := map[int64]float64{}
	rows, err := tx.Query(ctx,
		`SELECT id, rating FROM players WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ordered)
	if err != nil {
		return fmt.Errorf("lock players for rating: %w", err)
	}
	for rows.Next() {
		var id int64
		var rating float64
		if err := rows.Scan(&id, &rating); err != nil {
			rows.Close()
			return err
		}
		before[id] = rating
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(before) != 4 {
		return fmt.Errorf("match %d: found %d of 4 players for rating", m.ID, len(before))
	}

	a := [2]float64{before[ids[0]], before[ids[1]]}
	b := [2]float64{before[ids[2]], before[ids[3]]}
	update := ratings.TeamUpdate(a, b, winningTeam == 1)
	after := ratings.Apply(a, b, update)

	for i, id := range ids {
		if _, err := tx.Exec(ctx,
			`UPDATE players SET rating = $2, rated_games = rated_games + 1 WHERE id = $1`,
			id, after[i]); err != nil {
			return fmt.Errorf("update rating for player %d: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE match_players SET rating_before = $3, rating_after = $4
			WHERE match_id = $1 AND player_id = $2`,
			m.ID, id, before[id], after[i]); err != nil {
			return fmt.Errorf("record rating history for player %d: %w", id, err)
		}
	}
	return nil
}

// releasePlayers returns everyone who was on court to the ready queue, unless
// the organizer has already marked them as having left.
func releasePlayers(ctx context.Context, tx pgx.Tx, eventID int64, m Match) error {
	for _, p := range m.Players {
		if _, err := tx.Exec(ctx, `
			UPDATE event_players SET state = 'ready', ready_since = now()
			WHERE event_id = $1 AND player_id = $2 AND state = 'playing'`,
			eventID, p.PlayerID); err != nil {
			return fmt.Errorf("return %s to the queue: %w", p.DisplayName, err)
		}
	}
	return nil
}

// readyCandidates reads the queue inside the transaction that holds the event
// lock, so the matcher sees the state it is about to act on.
func readyCandidates(ctx context.Context, tx pgx.Tx, eventID int64) ([]matchmaking.Candidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.display_name, p.rating, ep.ready_since
		FROM event_players ep
		JOIN players p ON p.id = ep.player_id
		WHERE ep.event_id = $1 AND ep.state = 'ready'
		ORDER BY ep.ready_since, p.id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("read ready players: %w", err)
	}
	defer rows.Close()

	var out []matchmaking.Candidate
	for rows.Next() {
		var c matchmaking.Candidate
		if err := rows.Scan(&c.PlayerID, &c.Name, &c.Rating, &c.ReadySince); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func activeMatchOnCourt(ctx context.Context, tx pgx.Tx, courtID int64) (*Match, error) {
	var id, eventID int64
	err := tx.QueryRow(ctx,
		`SELECT id, event_id FROM matches WHERE court_id = $1 AND state = 'active'`, courtID).
		Scan(&id, &eventID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check court: %w", err)
	}
	m, err := matchInTx(ctx, tx, eventID, id)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func matchForUpdate(ctx context.Context, tx pgx.Tx, eventID, matchID int64) (Match, error) {
	var m Match
	err := tx.QueryRow(ctx, `
		SELECT id, event_id, court_id, state, winning_team, started_at, ended_at
		FROM matches WHERE id = $1 AND event_id = $2 FOR UPDATE`, matchID, eventID).
		Scan(&m.ID, &m.EventID, &m.CourtID, &m.State, &m.WinningTeam, &m.StartedAt, &m.EndedAt)
	if err == pgx.ErrNoRows {
		return Match{}, notFound("match %d does not exist in event %d", matchID, eventID)
	}
	if err != nil {
		return Match{}, fmt.Errorf("lock match: %w", err)
	}
	if m.Players, err = matchPlayersInTx(ctx, tx, matchID); err != nil {
		return Match{}, err
	}
	return m, nil
}

// matchInTx re-reads a match on the transaction's own connection, so callers
// never see a version of the row from outside the transaction.
func matchInTx(ctx context.Context, tx pgx.Tx, eventID, matchID int64) (Match, error) {
	var m Match
	err := tx.QueryRow(ctx, `
		SELECT m.id, m.event_id, m.court_id, c.label, m.state, m.winning_team, m.started_at, m.ended_at
		FROM matches m JOIN courts c ON c.id = m.court_id
		WHERE m.id = $1 AND m.event_id = $2`, matchID, eventID).
		Scan(&m.ID, &m.EventID, &m.CourtID, &m.CourtLabel, &m.State, &m.WinningTeam, &m.StartedAt, &m.EndedAt)
	if err == pgx.ErrNoRows {
		return Match{}, notFound("match %d does not exist in event %d", matchID, eventID)
	}
	if err != nil {
		return Match{}, fmt.Errorf("read match: %w", err)
	}
	if m.Players, err = matchPlayersInTx(ctx, tx, matchID); err != nil {
		return Match{}, err
	}
	return m, nil
}

func matchPlayersInTx(ctx context.Context, tx pgx.Tx, matchID int64) ([]MatchPlayer, error) {
	rows, err := tx.Query(ctx, `
		SELECT mp.player_id, p.display_name, mp.team, mp.active, mp.rating_before, mp.rating_after
		FROM match_players mp JOIN players p ON p.id = mp.player_id
		WHERE mp.match_id = $1
		ORDER BY mp.team, p.display_name`, matchID)
	if err != nil {
		return nil, fmt.Errorf("read match players: %w", err)
	}
	defer rows.Close()

	var out []MatchPlayer
	for rows.Next() {
		var p MatchPlayer
		if err := rows.Scan(&p.PlayerID, &p.DisplayName, &p.Team, &p.Active, &p.RatingBefore, &p.RatingAfter); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
