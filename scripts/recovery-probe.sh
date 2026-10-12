#!/bin/sh
# Proves the v1.0 kill-worker recovery path against a two-worker Compose stack:
# submit a long job, stop the worker that claimed it, wait for lease recovery
# and a second attempt (or reassignment), then cancel the job.
# Needs wget, docker CLI, and access to the Compose Docker socket + API network.
set -eu

API_URL="${API_URL:-http://server:8080}"
COMPOSE_PROJECT="${COMPOSE_PROJECT:-tsuzuku-recovery}"
EXPECTED_WORKERS="${EXPECTED_WORKERS:-2}"
ATTEMPTS="${ATTEMPTS:-60}"
JOB_ATTEMPTS="${JOB_ATTEMPTS:-120}"
JOB_REPOSITORY="${JOB_REPOSITORY:-https://github.com/octocat/Hello-World}"
JOB_IMAGE="${JOB_IMAGE:-golang:1.27}"
LEASE_WAIT="${LEASE_WAIT:-90}"

online=0
i=0
while [ "$i" -lt "$ATTEMPTS" ]; do
  body=$(wget -qO- "$API_URL/api/v1/workers" 2>/dev/null || true)
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

submit() {
  body=$(printf '{"repository":"%s","revision":"master","command":"%s","acceptance_criteria":["recovery"],"runtime":{"image":"%s"},"timeout_seconds":300}' \
    "$JOB_REPOSITORY" "$1" "$JOB_IMAGE")
  wget -qO- --header 'Content-Type: application/json' --post-data "$body" "$API_URL/api/v1/workloads" |
    sed -n 's/^{"id":"\([0-9a-f-]*\)".*/\1/p'
}

state_of() {
  wget -qO- "$API_URL/api/v1/jobs/$1" | grep -o '"state":"[A-Z]*"' | head -n 1 | cut -d'"' -f4
}

job_json() {
  wget -qO- "$API_URL/api/v1/jobs/$1"
}

assigned_worker_id() {
  job_json "$1" | sed -n 's/.*"assigned_worker_id":"\([0-9a-f-]*\)".*/\1/p' | head -n 1
}

worker_name_for() {
  wid="$1"
  wget -qO- "$API_URL/api/v1/workers" | tr '}' '\n' |
    grep "\"id\":\"$wid\"" | sed -n 's/.*"name":"\([^"]*\)".*/\1/p' | head -n 1
}

max_attempt_number() {
  wget -qO- "$API_URL/api/v1/jobs/$1/attempts" |
    grep -o '"attempt_number":[0-9]*' | cut -d: -f2 | sort -n | tail -n 1
}

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

sleeper=$(submit 'echo recovery-demo; sleep 300')
if [ -z "$sleeper" ]; then
  echo "could not submit recovery job" >&2
  exit 1
fi
echo "submitted job $sleeper"

wait_running "$sleeper"
worker_id=$(assigned_worker_id "$sleeper")
if [ -z "$worker_id" ]; then
  echo "job $sleeper has no assigned worker while EXECUTING" >&2
  exit 1
fi
worker_name=$(worker_name_for "$worker_id")
if [ -z "$worker_name" ]; then
  echo "could not resolve worker name for $worker_id" >&2
  exit 1
fi
echo "job $sleeper executing on $worker_name ($worker_id)"

cid=$(docker ps -q \
  --filter "label=com.docker.compose.project=${COMPOSE_PROJECT}" \
  --filter "label=com.docker.compose.service=${worker_name}" | head -n 1)
if [ -z "$cid" ]; then
  echo "no running container for compose service ${worker_name} in project ${COMPOSE_PROJECT}" >&2
  docker ps --filter "label=com.docker.compose.project=${COMPOSE_PROJECT}" >&2 || true
  exit 1
fi

echo "stopping worker container $cid ($worker_name)"
docker stop "$cid" >/dev/null

first_attempt=$(max_attempt_number "$sleeper")
if [ -z "$first_attempt" ]; then
  first_attempt=1
fi
echo "waiting for lease recovery (attempt was $first_attempt, lease TTL ~30s)…"

recovered=0
i=0
while [ "$i" -lt "$LEASE_WAIT" ]; do
  attempts=$(max_attempt_number "$sleeper")
  state=$(state_of "$sleeper")
  new_worker=$(assigned_worker_id "$sleeper" || true)
  if [ -n "$attempts" ] && [ "$attempts" -gt "$first_attempt" ]; then
    recovered=1
    echo "recovered: attempt_number=$attempts state=$state worker=${new_worker:-none}"
    break
  fi
  if [ -n "$new_worker" ] && [ "$new_worker" != "$worker_id" ]; then
    recovered=1
    echo "recovered: reassigned to $new_worker state=$state attempt=${attempts:-?}"
    break
  fi
  i=$((i + 1))
  sleep 1
done

if [ "$recovered" -ne 1 ]; then
  echo "job $sleeper did not recover after killing $worker_name" >&2
  echo "state=$(state_of "$sleeper") attempts=$(max_attempt_number "$sleeper")" >&2
  job_json "$sleeper" >&2 || true
  wget -qO- "$API_URL/api/v1/jobs/$sleeper/attempts" >&2 || true
  exit 1
fi

wget -qO /dev/null --post-data '' "$API_URL/api/v1/jobs/$sleeper/cancel" || true
echo "cancellation requested for job $sleeper"

i=0
state=""
while [ "$i" -lt "$JOB_ATTEMPTS" ]; do
  state=$(state_of "$sleeper")
  case "$state" in
    CANCELLED | COMPLETED | FAILED | BLOCKED) break ;;
  esac
  i=$((i + 1))
  sleep 2
done
echo "job $sleeper final state: $state"
echo "v1.0 recovery demo passed"
