-- 0001_init: the six domain tables for one club running one event at a time.

CREATE TABLE players (
    id           BIGSERIAL PRIMARY KEY,
    display_name TEXT        NOT NULL CHECK (btrim(display_name) <> ''),
    -- private_ref is the trimmed, lower-cased email or club ID from the form.
    -- Display names are not unique; this is what identifies a returning player.
    -- It is never rendered on a public page.
    private_ref  TEXT        NOT NULL UNIQUE CHECK (btrim(private_ref) <> ''),
    -- skill_seed is the provisional rating derived from the self-reported band.
    skill_seed   INTEGER     NOT NULL CHECK (skill_seed BETWEEN 100 AND 3000),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE events (
    id         BIGSERIAL   PRIMARY KEY,
    title      TEXT        NOT NULL CHECK (btrim(title) <> ''),
    event_date DATE        NOT NULL,
    state      TEXT        NOT NULL DEFAULT 'setup'
                           CHECK (state IN ('setup', 'live', 'finished')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- event_players is the roster for one event plus the player's current state in
-- it. A player can appear in many events but only once per event.
CREATE TABLE event_players (
    event_id      BIGINT      NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    player_id     BIGINT      NOT NULL REFERENCES players(id),
    state         TEXT        NOT NULL DEFAULT 'resting'
                              CHECK (state IN ('ready', 'resting', 'playing', 'left')),
    -- ready_since drives the queue: the longest wait goes on first.
    ready_since   TIMESTAMPTZ,
    checked_in_at TIMESTAMPTZ,
    PRIMARY KEY (event_id, player_id),
    CONSTRAINT ready_players_have_a_wait_time
        CHECK (state <> 'ready' OR ready_since IS NOT NULL)
);

CREATE INDEX event_players_queue ON event_players (event_id, state, ready_since);

CREATE TABLE courts (
    id        BIGSERIAL PRIMARY KEY,
    event_id  BIGINT    NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    label     TEXT      NOT NULL CHECK (btrim(label) <> ''),
    available BOOLEAN   NOT NULL DEFAULT TRUE,
    UNIQUE (event_id, label)
);

CREATE TABLE matches (
    id           BIGSERIAL   PRIMARY KEY,
    event_id     BIGINT      NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    court_id     BIGINT      NOT NULL REFERENCES courts(id),
    state        TEXT        NOT NULL DEFAULT 'active'
                             CHECK (state IN ('active', 'completed', 'cancelled')),
    winning_team SMALLINT    CHECK (winning_team IN (1, 2)),
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at     TIMESTAMPTZ,
    CONSTRAINT winner_only_on_completed_match
        CHECK ((state = 'completed') = (winning_team IS NOT NULL)),
    CONSTRAINT active_match_has_no_end_time
        CHECK ((state = 'active') = (ended_at IS NULL))
);

-- One live match per court. The partial index is what actually enforces it:
-- two concurrent assignments cannot both commit.
CREATE UNIQUE INDEX one_active_match_per_court
    ON matches (court_id) WHERE state = 'active';

CREATE INDEX matches_by_event ON matches (event_id, state);

CREATE TABLE match_players (
    match_id  BIGINT   NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    player_id BIGINT   NOT NULL REFERENCES players(id),
    team      SMALLINT NOT NULL CHECK (team IN (1, 2)),
    -- active mirrors matches.state and is updated in the same transaction.
    -- It exists so the partial index below can enforce the per-player rule.
    active    BOOLEAN  NOT NULL DEFAULT TRUE,
    PRIMARY KEY (match_id, player_id)
);

-- A player is in at most one live match across the whole database.
CREATE UNIQUE INDEX one_active_match_per_player
    ON match_players (player_id) WHERE active;

CREATE INDEX match_players_by_player ON match_players (player_id);
