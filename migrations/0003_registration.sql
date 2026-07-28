-- Optional answers belong to an event registration, not a shared identity.
-- NULL preserves the difference between "No" and an unanswered question.
ALTER TABLE event_players
    ADD COLUMN estimated_arrival TEXT NOT NULL DEFAULT ''
        CHECK (octet_length(estimated_arrival) <= 120),
    ADD COLUMN needs_paddle BOOLEAN;
