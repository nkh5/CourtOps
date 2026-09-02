-- Historical start times and uninterrupted attendance cannot be reconstructed.
ALTER TABLE events ADD COLUMN started_at TIMESTAMPTZ;
ALTER TABLE events ADD COLUMN finished_at TIMESTAMPTZ;
ALTER TABLE event_players ADD COLUMN stayed_whole_event BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX match_players_player_history ON match_players (player_id, match_id);
