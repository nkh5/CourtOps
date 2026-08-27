#!/usr/bin/env bash
# External health check: validates TLS, HTTP status and the database health body.
set -euo pipefail
url="${1:?usage: check-health.sh <https://host/healthz> [4|6|auto]}"
family="${2:-auto}"
case "$url" in https://*|http://localhost:*|http://127.0.0.1:*) ;; *) echo 'expected HTTPS URL (HTTP is allowed on loopback only)' >&2; exit 2;; esac
args=()
case "$family" in 4) args=(-4);; 6) args=(-6);; auto) ;; *) echo 'family must be 4, 6 or auto' >&2; exit 2;; esac
body="$(mktemp)"
trap 'rm -f "$body"' EXIT
curl "${args[@]}" --fail --silent --show-error --connect-timeout 10 --max-time 20 \
  --retry 2 --retry-delay 3 --output "$body" "$url"
python3 - "$body" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    body = json.load(f)
if body.get('status') != 'ok' or body.get('database') != 'ok':
    raise SystemExit('health response is not healthy')
print('Application and PostgreSQL are healthy.')
PY
