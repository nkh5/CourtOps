-- Adopt the new band scale only for identities still at their provisional seed.
-- An earned rating and its match history must stay on their original scale.
UPDATE players
SET skill_seed = CASE skill_seed WHEN 800 THEN 400 WHEN 1000 THEN 800 END,
    rating = CASE skill_seed WHEN 800 THEN 400 WHEN 1000 THEN 800 END
WHERE rated_games = 0 AND rating = skill_seed AND skill_seed IN (800, 1000);

ALTER TABLE players ALTER COLUMN rating SET DEFAULT 400;
