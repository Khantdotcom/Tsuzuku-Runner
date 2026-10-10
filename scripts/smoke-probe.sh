#!/bin/sh
# Probes a running Compose stack from inside its network: waits until the
# dashboard reports the expected number of online workers, then checks API
# readiness, the dashboard proxy, and the dashboard page. Finally it runs real
# jobs end to end: one that completes and passes verification, one whose
# command fails, one whose verification fails, and one that is cancelled while
# it runs. It also downloads a captured log. POSIX sh and BusyBox wget only, so
# it runs in a stock alpine image.
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

# submit COMMAND [VERIFICATION] prints the new job's id.
submit() {
  verification=""
  if [ $# -gt 1 ]; then
    verification=$(printf ',"verification":{"command":"%s"}' "$2")
  fi
  body=$(printf '{"repository":"%s","revision":"master","command":"%s","acceptance_criteria":["smoke"],"runtime":{"image":"%s"},"timeout_seconds":120%s}' \
    "$JOB_REPOSITORY" "$1" "$JOB_IMAGE" "$verification")
  wget -qO- --header 'Content-Type: application/json' --post-data "$body" "$API_URL/api/v1/workloads" |
    sed -n 's/^{"id":"\([0-9a-f-]*\)".*/\1/p'
}

state_of() {
  wget -qO- "$API_URL/api/v1/jobs/$1" | grep -o '"state":"[A-Z]*"' | head -n 1 | cut -d'"' -f4
}

# expect_state ID STATE waits for the job to reach a final state and fails
# unless it is STATE.
expect_state() {
  i=0
  state=""
  while [ "$i" -lt "$JOB_ATTEMPTS" ]; do
    state=$(state_of "$1")
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

# wait_running ID waits until the job's command is running.
wait_running() {
  i=0
  while [ "$i" -lt "$JOB_ATTEMPTS" ]; do
    if [ "$(state_of "$1")" = EXECUTING ]; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "job $1 never started executing" >&2
  exit 1
}

# expect_evidence ID PATTERN fails unless the job's evidence matches PATTERN.
expect_evidence() {
  if ! wget -qO- "$API_URL/api/v1/jobs/$1/evidence" | grep -q "$2"; then
    echo "evidence of job $1 does not contain $2" >&2
    wget -qO- "$API_URL/api/v1/jobs/$1/evidence" >&2 || true
    exit 1
  fi
}

passing=$(submit 'test -f README && echo smoke-output && go version' 'grep -q Hello README')
failing=$(submit 'echo failing on purpose >&2; exit 3')
unverified=$(submit 'true' 'echo verification failing on purpose; exit 4')
sleeper=$(submit 'echo sleeping; sleep 300')
if [ -z "$passing" ] || [ -z "$failing" ] || [ -z "$unverified" ] || [ -z "$sleeper" ]; then
  echo "could not submit smoke jobs" >&2
  exit 1
fi

wait_running "$sleeper"
wget -qO /dev/null --post-data '' "$API_URL/api/v1/jobs/$sleeper/cancel"
echo "cancellation requested for job $sleeper"

expect_state "$passing" COMPLETED
expect_state "$failing" FAILED
expect_state "$unverified" FAILED
expect_state "$sleeper" CANCELLED

expect_evidence "$passing" '"status":"PASSED"'
expect_evidence "$failing" '"category":"TEST"'
expect_evidence "$unverified" 'verification failing on purpose'

stdout_url=$(wget -qO- "$API_URL/api/v1/jobs/$passing/evidence" | tr '{' '\n' |
  grep '"name":"stdout.log"' | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
if [ -z "$stdout_url" ]; then
  echo "job $passing has no stdout.log artifact" >&2
  exit 1
fi
if ! wget -qO- "$API_URL$stdout_url" | grep -q smoke-output; then
  echo "stdout.log of job $passing does not contain the command output" >&2
  exit 1
fi
echo "evidence recorded and logs downloadable"
echo "jobs ran end to end"
