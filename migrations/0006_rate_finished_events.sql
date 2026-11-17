-- 0006_rate_finished_events: give Elo to finished events that were played
-- before their matches stored ratings.
--
-- Each completed doubles match in a finished event with no rating_after (an
-- unrated match still records rating_before when it is assigned) is replayed
-- in the order it ended, using the same rule as the app (internal/ratings):
-- teams compared by mean rating, K = 32, the same change for both partners.
-- A match is skipped if any of its four players already has a rated game that
-- ended after it, so an earned history is never rewritten out of order.
-- Matches that already have ratings are never touched, so this is safe on a
-- database where everything is already rated.
DO $$
DECLARE
    m          RECORD;
    team_a     DOUBLE PRECISION;
    team_b     DOUBLE PRECISION;
    expected_a DOUBLE PRECISION;
    delta_a    DOUBLE PRECISION;
BEGIN
    FOR m IN
        SELECT mt.id, mt.winning_team, mt.ended_at
        FROM matches mt
        JOIN events e ON e.id = mt.event_id
        WHERE e.state = 'finished'
          AND mt.state = 'completed'
          AND NOT EXISTS (SELECT 1 FROM match_players mp
                          WHERE mp.match_id = mt.id AND mp.rating_after IS NOT NULL)
          AND (SELECT count(*) FILTER (WHERE mp.team = 1) = 2 AND count(*) FILTER (WHERE mp.team = 2) = 2
               FROM match_players mp WHERE mp.match_id = mt.id)
        ORDER BY mt.ended_at, mt.id
    LOOP
        IF EXISTS (
            SELECT 1
            FROM match_players mine
            JOIN match_players later ON later.player_id = mine.player_id AND later.match_id <> mine.match_id
            JOIN matches lm ON lm.id = later.match_id
            WHERE mine.match_id = m.id
              AND later.rating_after IS NOT NULL
              AND lm.ended_at > m.ended_at
        ) THEN
            CONTINUE;
        END IF;

        SELECT avg(p.rating) FILTER (WHERE mp.team = 1),
               avg(p.rating) FILTER (WHERE mp.team = 2)
        INTO team_a, team_b
        FROM match_players mp
        JOIN players p ON p.id = mp.player_id
        WHERE mp.match_id = m.id;

        expected_a := 1 / (1 + power(10::double precision, (team_b - team_a) / 400));
        delta_a := 32 * ((CASE WHEN m.winning_team = 1 THEN 1 ELSE 0 END) - expected_a);

        UPDATE match_players mp
        SET rating_before = p.rating,
            rating_after  = p.rating + CASE WHEN mp.team = 1 THEN delta_a ELSE -delta_a END
        FROM players p
        WHERE p.id = mp.player_id AND mp.match_id = m.id;

        UPDATE players p
        SET rating = mp.rating_after,
            rated_games = p.rated_games + 1
        FROM match_players mp
        WHERE mp.match_id = m.id AND mp.player_id = p.id;
    END LOOP;
END $$;
