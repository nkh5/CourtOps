#!/usr/bin/env bash
# External health check: validates TLS, HTTP status and the database health body.
set -euo pipefail
url="${1:?usage: check-health.sh <https://host/healthz> [4|6|auto] [wait-seconds]}"
family="${2:-auto}"
wait_seconds="${3:-90}"
case "$url" in https://*|http://localhost:*|http://127.0.0.1:*) ;; *) echo 'expected HTTPS URL (HTTP is allowed on loopback only)' >&2; exit 2;; esac
args=(--fail --silent --show-error --connect-timeout 10)
case "$family" in 4) args+=(-4);; 6) args+=(-6);; auto) ;; *) echo 'family must be 4, 6 or auto' >&2; exit 2;; esac
[[ "$wait_seconds" =~ ^[1-9][0-9]*$ ]] || { echo 'wait-seconds must be a positive integer' >&2; exit 2; }
body="$(mktemp)"
trap 'rm -f "$body"' EXIT

# Caddy can take one health-check interval to accept a replacement container.
# Give the public endpoint a bounded recovery window, returning as soon as it
# reports both the application and database healthy.
deadline=$((SECONDS + wait_seconds))
while (( SECONDS < deadline )); do
  remaining=$((deadline - SECONDS))
  request_timeout=20
  if (( remaining < request_timeout )); then request_timeout=$remaining; fi
  : > "$body"
  if curl "${args[@]}" --max-time "$request_timeout" --output "$body" "$url" &&
    python3 - "$body" <<'PY'
import json, sys
try:
    with open(sys.argv[1]) as f:
        body = json.load(f)
except (OSError, ValueError):
    raise SystemExit('health response is not valid JSON')
if not isinstance(body, dict) or body.get('status') != 'ok' or body.get('database') != 'ok':
    raise SystemExit('health response is not healthy')
print('Application and PostgreSQL are healthy.')
PY
  then
    exit 0
  fi

  remaining=$((deadline - SECONDS))
  if (( remaining <= 0 )); then break; fi
  echo "Health check not ready; retrying ($remaining seconds remaining)." >&2
  retry_delay=3
  if (( remaining < retry_delay )); then retry_delay=$remaining; fi
  sleep "$retry_delay"
done

echo "Health check failed after waiting up to $wait_seconds seconds." >&2
exit 1
