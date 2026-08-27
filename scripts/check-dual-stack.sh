#!/usr/bin/env bash
# Run from a host with working IPv4 AND IPv6 Internet connectivity.
set -euo pipefail
domain="${1:?usage: check-dual-stack.sh your-domain}"
[[ "$domain" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*[a-zA-Z0-9]$ ]] || { echo 'invalid hostname' >&2; exit 2; }
cd "$(dirname "$0")/.."
for family in 4 6; do
  echo "Checking HTTPS over IPv$family"
  ./scripts/check-health.sh "https://$domain/healthz" "$family"
  curl "-$family" --fail --silent --show-error --connect-timeout 10 --max-time 20 \
    --output /dev/null "https://$domain/"
done
echo 'Both protocols passed. Check security-group/host-firewall rules also exclude 5432 and 8080.'
