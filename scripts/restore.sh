#!/usr/bin/env bash
# Restore a backup over the LIVE database. Destructive.
#
# Use this only after deciding that the current database is unusable. Run
# restore-check.sh on the same file first, so the file is known-good before the
# live data is replaced.

set -euo pipefail

BACKUP_FILE="${1:?usage: restore.sh <backup file>}"
COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose.yaml}"
DB_SERVICE="${DB_SERVICE:-db}"
APP_SERVICE="${APP_SERVICE:-app}"
DB_USER="${DB_USER:-courtops}"
DB_NAME="${DB_NAME:-courtops}"
PASSPHRASE_FILE="${BACKUP_PASSPHRASE_FILE:-/etc/courtops/backup.key}"

echo "This will replace the contents of $DB_NAME with $BACKUP_FILE."
read -r -p "Type RESTORE to continue: " CONFIRM
[[ "$CONFIRM" == "RESTORE" ]] || { echo "aborted"; exit 1; }

echo "==> stopping the app so nothing writes during the restore"
docker compose -f "$COMPOSE_FILE" stop "$APP_SERVICE"

echo "==> restoring"
openssl enc -d -aes-256-cbc -pbkdf2 -pass "file:$PASSPHRASE_FILE" -in "$BACKUP_FILE" \
  | gunzip \
  | docker compose -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" \
      psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME"

echo "==> starting the app"
docker compose -f "$COMPOSE_FILE" start "$APP_SERVICE"

echo "==> done. Open /healthz and the event page before telling anyone it is back."
