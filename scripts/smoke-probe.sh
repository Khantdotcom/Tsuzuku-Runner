#!/bin/sh
# Probes a running Compose stack from inside its network: waits until the
# dashboard reports the expected number of online workers, then checks API
# readiness, the dashboard proxy, and the dashboard page. POSIX sh and BusyBox
# wget only, so it runs in a stock alpine image.
set -eu

API_URL="${API_URL:-http://server:8080}"
DASHBOARD_URL="${DASHBOARD_URL:-http://frontend:3000}"
EXPECTED_WORKERS="${EXPECTED_WORKERS:-2}"
ATTEMPTS="${ATTEMPTS:-60}"

online=0
i=0
while [ "$i" -lt "$ATTEMPTS" ]; do
  body=$(wget -qO- "$DASHBOARD_URL/api/v1/workers" 2>/dev/null || true)
  online=$(printf '%s' "$body" | grep -o '"status":"online"' | wc -l | tr -d ' ')
  if [ "$online" -ge "$EXPECTED_WORKERS" ]; then
    break
  fi
  i=$((i + 1))
  sleep 2
done

echo "workers online: $online"
if [ "$online" -lt "$EXPECTED_WORKERS" ]; then
  echo "expected $EXPECTED_WORKERS workers online, got $online" >&2
  exit 1
fi

wget -qO- "$API_URL/readyz"
echo
wget -qO- "$DASHBOARD_URL/api/readyz"
echo
wget -qO /dev/null "$DASHBOARD_URL/"
echo "stack is healthy"
