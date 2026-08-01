package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Snapshot reads one consistent version of the event. Repeatable read keeps a
// concurrent assignment or completion from appearing in only half the board.
// The read-only transaction does not block organizer writes.
func (s *Store) Snapshot(ctx context.Context, eventID int64) (Snapshot, error) {
	var snap Snapshot
	err := s.inTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if snap.Event, err = getEvent(ctx, tx, eventID); err != nil {
			return err
		}
		snap.FetchedAt = time.Now().UTC()
		if snap.Roster, err = rosterInTx(ctx, tx, eventID); err != nil {
			return err
		}
		for _, p := range snap.Roster {
			if p.State == StateReady {
				snap.Queue = append(snap.Queue, p)
			}
		}
		if snap.Courts, err = courtsInTx(ctx, tx, eventID); err != nil {
			return err
		}
		snap.Recent, err = matchesWhere(ctx, tx,
			`m.event_id = $1 AND m.state <> 'active' ORDER BY m.ended_at DESC, m.id DESC LIMIT 8`, eventID)
		return err
	})
	if err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// roster returns every member of the event. Ready players come first in queue
// order so the organizer page and the matcher agree on who is next.
func rosterInTx(ctx context.Context, tx pgx.Tx, eventID int64) ([]EventPlayer, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.display_name, ep.state, ep.ready_since, ep.checked_in_at,
		       p.rating, p.rated_games
		FROM event_players ep
		JOIN players p ON p.id = ep.player_id
		WHERE ep.event_id = $1
		ORDER BY
			CASE ep.state WHEN 'ready' THEN 0 WHEN 'playing' THEN 1 WHEN 'resting' THEN 2 ELSE 3 END,
			ep.ready_since NULLS LAST,
			p.id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("read roster: %w", err)
	}
	defer rows.Close()

	var out []EventPlayer
	for rows.Next() {
		var p EventPlayer
		if err := rows.Scan(&p.PlayerID, &p.DisplayName, &p.State, &p.ReadySince,
			&p.CheckedInAt, &p.Rating, &p.RatedGames); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func courtsInTx(ctx context.Context, tx pgx.Tx, eventID int64) ([]Court, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, event_id, label, available
		FROM courts WHERE event_id = $1 ORDER BY label`, eventID)
	if err != nil {
		return nil, fmt.Errorf("read courts: %w", err)
	}
	defer rows.Close()

	var courts []Court
	byID := map[int64]int{}
	for rows.Next() {
		var c Court
		if err := rows.Scan(&c.ID, &c.EventID, &c.Label, &c.Available); err != nil {
			return nil, err
		}
		byID[c.ID] = len(courts)
		courts = append(courts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	live, err := matchesWhere(ctx, tx, `m.event_id = $1 AND m.state = 'active'`, eventID)
	if err != nil {
		return nil, err
	}
	for i := range live {
		if idx, ok := byID[live[i].CourtID]; ok {
			courts[idx].ActiveMatch = &live[i]
		}
	}
	return courts, nil
}

// matchesWhere loads matches plus their players with two queries.
func matchesWhere(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]Match, error) {
	rows, err := tx.Query(ctx, `
		SELECT m.id, m.event_id, m.court_id, c.label, m.state, m.winning_team, m.started_at, m.ended_at
		FROM matches m
		JOIN courts c ON c.id = m.court_id
		WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read matches: %w", err)
	}
	defer rows.Close()

	var matches []Match
	index := map[int64]int{}
	var ids []int64
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.ID, &m.EventID, &m.CourtID, &m.CourtLabel, &m.State,
			&m.WinningTeam, &m.StartedAt, &m.EndedAt); err != nil {
			return nil, err
		}
		index[m.ID] = len(matches)
		ids = append(ids, m.ID)
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}

	playerRows, err := tx.Query(ctx, `
		SELECT mp.match_id, mp.player_id, p.display_name, mp.team, mp.active,
		       mp.rating_before, mp.rating_after
		FROM match_players mp
		JOIN players p ON p.id = mp.player_id
		WHERE mp.match_id = ANY($1)
		ORDER BY mp.team, p.display_name`, ids)
	if err != nil {
		return nil, fmt.Errorf("read match players: %w", err)
	}
	defer playerRows.Close()

	for playerRows.Next() {
		var matchID int64
		var mp MatchPlayer
		if err := playerRows.Scan(&matchID, &mp.PlayerID, &mp.DisplayName, &mp.Team,
			&mp.Active, &mp.RatingBefore, &mp.RatingAfter); err != nil {
			return nil, err
		}
		if i, ok := index[matchID]; ok {
			matches[i].Players = append(matches[i].Players, mp)
		}
	}
	return matches, playerRows.Err()
}

// GetMatch reads one match with its players, for the result confirmation page.
func (s *Store) GetMatch(ctx context.Context, eventID, matchID int64) (Match, error) {
	var match Match
	err := s.inTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		match, err = matchInTx(ctx, tx, eventID, matchID)
		return err
	})
	if err != nil {
		return Match{}, err
	}
	return match, nil
}
