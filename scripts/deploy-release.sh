#!/usr/bin/env bash
# Run from the existing checkout on EC2. It changes only the app image.
# Backward-incompatible migrations require a separately planned release.
set -euo pipefail
image="${1:?usage: deploy-release.sh ghcr.io/owner/repo@sha256:digest}"
[[ "$image" =~ ^ghcr\.io/[a-z0-9._/-]+@sha256:[a-f0-9]{64}$ ]] || { echo 'use an immutable GHCR image digest' >&2; exit 2; }
cd "$(dirname "$0")/.."
[[ -f deploy/.env ]] || { echo 'deploy/.env is required; see README.md and your deployment guide' >&2; exit 1; }
# A persistent Compose project name preserves the existing named volumes.
[[ -n "${COMPOSE_PROJECT_NAME:-}" ]] || { echo 'export the existing COMPOSE_PROJECT_NAME before running this script' >&2; exit 1; }
mkdir -p backups
mkdir backups/.release-lock 2>/dev/null || { echo 'another release is running (or a stale release lock needs investigation)' >&2; exit 1; }
trap 'rmdir backups/.release-lock' EXIT
export COURTOPS_ENV_FILE="$PWD/deploy/.env"
export COMPOSE_FILE=deploy/compose.yaml
# Explicit opt-in is read as plain data, never sourced as shell code.
if [[ "$(sed -n 's/^COURTOPS_DUAL_STACK=//p' deploy/.env | tr -d '\r')" == true ]]; then
  export COMPOSE_FILE=deploy/compose.yaml:deploy/compose.ipv6.yaml
fi
compose=(docker compose --env-file deploy/.env)
if [[ -f deploy/release.env ]]; then compose=(docker compose --env-file deploy/.env --env-file deploy/release.env); fi
"${compose[@]}" config --quiet
app_id="$("${compose[@]}" ps -q app)"
[[ -n "$app_id" ]] || { echo 'existing app not found; use first-time deployment instructions' >&2; exit 1; }
previous="$(docker inspect --format '{{.Config.Image}}' "$app_id")"
# Prevent deleting or transforming event history before a verified backup exists.
backup_dir="$(mktemp -d "$PWD/backups/pre-release.XXXXXX")"
BACKUP_DIR="$backup_dir" ./scripts/backup.sh
backup_files=("$backup_dir"/*.sql.gz.enc)
./scripts/restore-check.sh "${backup_files[0]}"
# Pull before changing the running service. No credentials are printed.
docker pull "$image"
printf '%s\n' "$previous" > deploy/previous-image.txt
chmod 600 deploy/previous-image.txt
# The app runs transactional migrations at startup. --wait verifies /healthz.
if ! COURTOPS_IMAGE="$image" "${compose[@]}" up -d --no-deps --wait --wait-timeout 120 app; then
  echo 'Release failed health checks. Previous image is in deploy/previous-image.txt.' >&2
  echo 'Inspect app logs and migration compatibility before rolling back. Database was not restored.' >&2
  exit 1
fi
umask 077
printf 'COURTOPS_IMAGE=%s\n' "$image" > deploy/release.env.tmp
mv deploy/release.env.tmp deploy/release.env
if [[ -n "${HEALTH_URL:-}" ]]; then ./scripts/check-health.sh "$HEALTH_URL"; fi
printf 'Release healthy. Verified backup: %s\n' "${backup_files[0]}"
echo 'For subsequent Compose commands include --env-file deploy/.env --env-file deploy/release.env.'
