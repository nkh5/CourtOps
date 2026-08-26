#!/usr/bin/env bash
# Restore a backup into a separate, empty database and check that it holds a
# usable event.
#
# This never touches the live database. It answers the only question that
# matters about a backup file: would this actually come back?
#
# Usage:
#   ./scripts/restore-check.sh ./backups/courtops-20260920T010000Z.sql.gz.enc

set -euo pipefail

BACKUP_FILE="${1:?usage: restore-check.sh <backup file>}"
COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose.yaml}"
# Accept the same environment file and optional overlay list as the release.
compose=(docker compose)
if [[ -n "${COURTOPS_ENV_FILE:-}" ]]; then
  compose+=(--env-file "$COURTOPS_ENV_FILE")
fi
IFS=: read -r -a compose_files <<< "$COMPOSE_FILE"
for compose_file in "${compose_files[@]}"; do compose+=(-f "$compose_file"); done
DB_SERVICE="${DB_SERVICE:-db}"
DB_USER="${DB_USER:-courtops}"
PASSPHRASE_FILE="${BACKUP_PASSPHRASE_FILE:-/etc/courtops/backup.key}"
CHECK_DB="courtops_restore_check_$(date -u +%Y%m%d%H%M%S)_$$"

cleanup() {
  "${compose[@]}" exec -T "$DB_SERVICE" \
    psql -U "$DB_USER" -d postgres -c "DROP DATABASE IF EXISTS \"$CHECK_DB\" WITH (FORCE);" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> creating empty database $CHECK_DB"
"${compose[@]}" exec -T "$DB_SERVICE" \
  psql -U "$DB_USER" -d postgres -c "CREATE DATABASE \"$CHECK_DB\";" >/dev/null

echo "==> restoring $BACKUP_FILE"
openssl enc -d -aes-256-cbc -pbkdf2 -pass "file:$PASSPHRASE_FILE" -in "$BACKUP_FILE" \
  | gunzip \
  | "${compose[@]}" exec -T "$DB_SERVICE" \
      psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$CHECK_DB" >/dev/null

echo "==> checking the restored data"
"${compose[@]}" exec -T "$DB_SERVICE" psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$CHECK_DB" <<'SQL'
\pset pager off
SELECT 'events' AS table, count(*) FROM events
UNION ALL SELECT 'players', count(*) FROM players
UNION ALL SELECT 'event_players', count(*) FROM event_players
UNION ALL SELECT 'matches', count(*) FROM matches
UNION ALL SELECT 'match_players', count(*) FROM match_players
UNION ALL SELECT 'schema_migrations', count(*) FROM schema_migrations;

-- A read that exercises the joins the app actually uses.
SELECT e.title, c.label, m.state, count(mp.player_id) AS players
FROM matches m
JOIN events e ON e.id = m.event_id
JOIN courts c ON c.id = m.court_id
LEFT JOIN match_players mp ON mp.match_id = m.id
GROUP BY e.title, c.label, m.state
ORDER BY e.title, c.label
LIMIT 10;

-- The indexes that enforce the concurrency rules must exist after a restore.
DO $$
BEGIN
  IF (SELECT count(*) FROM pg_indexes WHERE schemaname = 'public'
      AND indexname IN ('one_active_match_per_court', 'one_active_match_per_player')) <> 2 THEN
    RAISE EXCEPTION 'required concurrency indexes are missing from restored backup';
  END IF;
END $$;
SQL

echo
echo "==> restore check passed."
echo "    Remember: anything written after this backup was taken is not in it."
