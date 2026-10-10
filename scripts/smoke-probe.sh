#!/bin/sh
# Probes a running Compose stack from inside its network: waits until the
# dashboard reports the expected number of online workers, then checks API
# readiness, the dashboard proxy, and the dashboard page. Finally it runs two
# real jobs end to end, one that must complete and one that must fail. POSIX sh
# and BusyBox wget only, so it runs in a stock alpine image.
set -eu

API_URL="${API_URL:-http://server:8080}"
DASHBOARD_URL="${DASHBOARD_URL:-http://frontend:3000}"
EXPECTED_WORKERS="${EXPECTED_WORKERS:-2}"
ATTEMPTS="${ATTEMPTS:-60}"
JOB_ATTEMPTS="${JOB_ATTEMPTS:-120}"
JOB_REPOSITORY="${JOB_REPOSITORY:-https://github.com/octocat/Hello-World}"
JOB_IMAGE="${JOB_IMAGE:-golang:1.27}"

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

# submit COMMAND prints the new job's id.
submit() {
  body=$(printf '{"repository":"%s","revision":"master","command":"%s","acceptance_criteria":["smoke"],"runtime":{"image":"%s"},"timeout_seconds":120}' \
    "$JOB_REPOSITORY" "$1" "$JOB_IMAGE")
  wget -qO- --header 'Content-Type: application/json' --post-data "$body" "$API_URL/api/v1/workloads" |
    sed -n 's/^{"id":"\([0-9a-f-]*\)".*/\1/p'
}

# expect_state ID STATE waits for the job to reach a final state and fails
# unless it is STATE.
expect_state() {
  i=0
  state=""
  while [ "$i" -lt "$JOB_ATTEMPTS" ]; do
    state=$(wget -qO- "$API_URL/api/v1/jobs/$1" | grep -o '"state":"[A-Z]*"' | head -n 1 | cut -d'"' -f4)
    case "$state" in
      COMPLETED | FAILED | CANCELLED) break ;;
    esac
    i=$((i + 1))
    sleep 2
  done
  echo "job $1: $state"
  if [ "$state" != "$2" ]; then
    echo "expected job $1 to be $2, got ${state:-no state}" >&2
    wget -qO- "$API_URL/api/v1/jobs/$1" >&2 || true
    exit 1
  fi
}

passing=$(submit 'test -f README && go version')
failing=$(submit 'echo failing on purpose >&2; exit 3')
if [ -z "$passing" ] || [ -z "$failing" ]; then
  echo "could not submit smoke jobs" >&2
  exit 1
fi
expect_state "$passing" COMPLETED
expect_state "$failing" FAILED
echo "jobs ran end to end"
