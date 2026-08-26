#!/usr/bin/env bash
# Take an encrypted pg_dump and, if configured, copy it off this host.
#
# A backup that has never been restored is a guess, not a backup. Run
# scripts/restore-check.sh after this to prove the file is usable.
#
# Usage (on the EC2 host, from /opt/courtops):
#   ./scripts/backup.sh                 # local encrypted dump
#   S3_BUCKET=my-private-bucket ./scripts/backup.sh
#
# Required for encryption:
#   BACKUP_PASSPHRASE_FILE  path to a file holding the passphrase (chmod 600)

set -euo pipefail

COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose.yaml}"
# Accept the same environment file and optional overlay list as the release.
compose=(docker compose)
if [[ -n "${COURTOPS_ENV_FILE:-}" ]]; then
  compose+=(--env-file "$COURTOPS_ENV_FILE")
fi
IFS=: read -r -a compose_files <<< "$COMPOSE_FILE"
for compose_file in "${compose_files[@]}"; do compose+=(-f "$compose_file"); done
BACKUP_DIR="${BACKUP_DIR:-./backups}"
DB_SERVICE="${DB_SERVICE:-db}"
DB_USER="${DB_USER:-courtops}"
DB_NAME="${DB_NAME:-courtops}"
PASSPHRASE_FILE="${BACKUP_PASSPHRASE_FILE:-/etc/courtops/backup.key}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="${BACKUP_DIR}/courtops-${STAMP}.sql.gz.enc"

if [[ ! -r "$PASSPHRASE_FILE" ]]; then
  echo "error: no readable passphrase file at $PASSPHRASE_FILE" >&2
  echo "create one with: openssl rand -base64 48 | sudo tee $PASSPHRASE_FILE >/dev/null && sudo chmod 600 $PASSPHRASE_FILE" >&2
  exit 1
fi

mkdir -p "$BACKUP_DIR"

echo "==> dumping $DB_NAME"
# -T keeps docker from allocating a TTY, which would corrupt the byte stream.
"${compose[@]}" exec -T "$DB_SERVICE" \
  pg_dump --clean --if-exists --no-owner --no-privileges -U "$DB_USER" "$DB_NAME" \
  | gzip -9 \
  | openssl enc -aes-256-cbc -pbkdf2 -salt -pass "file:$PASSPHRASE_FILE" \
  > "$OUT"

chmod 600 "$OUT"
SIZE="$(du -h "$OUT" | cut -f1)"
echo "==> wrote $OUT ($SIZE)"

if [[ -n "${S3_BUCKET:-}" ]]; then
  # The instance role supplies the credentials; no keys live on the host.
  echo "==> copying to s3://${S3_BUCKET}/courtops/"
  aws s3 cp "$OUT" "s3://${S3_BUCKET}/courtops/" --only-show-errors
  echo "==> uploaded"
else
  echo "note: S3_BUCKET is not set, so this backup is still only on this host."
  echo "      Losing the host loses it. Copy it somewhere else before the pilot."
fi

# Keep a fortnight of local copies; the off-host bucket is the long-term store.
find "$BACKUP_DIR" -name 'courtops-*.sql.gz.enc' -mtime +14 -delete
echo "==> done. Verify it with: ./scripts/restore-check.sh $OUT"
