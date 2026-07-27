-- 0002_ratings: provisional local Elo (Session 15/16).
--
-- Existing players keep playing with their imported skill seed as the starting
-- rating. Nothing is backfilled from matches played before this migration; a
-- rating only moves when a match is completed after it is applied.

ALTER TABLE players
    ADD COLUMN rating      DOUBLE PRECISION NOT NULL DEFAULT 1000,
    ADD COLUMN rated_games INTEGER          NOT NULL DEFAULT 0 CHECK (rated_games >= 0);

UPDATE players SET rating = skill_seed;

-- Per-membership history, so a result can be explained after the fact without
-- replaying anything.
ALTER TABLE match_players
    ADD COLUMN rating_before DOUBLE PRECISION,
    ADD COLUMN rating_after  DOUBLE PRECISION;
