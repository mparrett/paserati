#!/usr/bin/env bash
# Serial Perf Timeline backfill driver.
#
# Two hazards this exists to handle:
#
#  1. Perf Timeline serializes on the `perf-timeline-main` concurrency group with
#     cancel-in-progress: false. GitHub keeps at most ONE pending run per group, so
#     each newly queued run cancels the previously queued one — dispatching a batch
#     lands exactly one snapshot. Hence: dispatch, wait for completion, then next.
#
#  2. runs-on: ubuntu-latest is a lottery across CPU tiers, and the page filters
#     timeline points to the modal CPU (the anchor drifts across tiers, so off-tier
#     points are noise, not signal). A snapshot that lands off-tier is ~13 wasted
#     minutes and never renders. Re-running a commit overwrites its snapshot, so a
#     miss is recoverable — retry until it lands on the reference tier.
#
# Usage: backfill.sh <sha>...
# Env:   REPO=mparrett/paserati  REF_CPU='EPYC 7763'  MAX_TRIES=3

set -uo pipefail

REPO="${REPO:-mparrett/paserati}"
REF_CPU="${REF_CPU:-EPYC 7763}"
MAX_TRIES="${MAX_TRIES:-3}"
WF="perf-timeline.yml"
POLL=30

[ $# -gt 0 ] || { echo "usage: $0 <sha>..." >&2; exit 2; }

log() { printf '%s  %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }

# Snapshot filename is derived from the COMMIT stamp, so it's predictable locally.
snap_name() {
  local sha="$1" epoch
  epoch="$(git show -s --format=%ct "$sha")" || return 1
  printf '%s-%s.json' "$(date -u -r "$epoch" +%Y%m%dT%H%M%SZ)" "$(git rev-parse --short=12 "$sha")"
}

# cpu_model of a snapshot already on perf-data, empty if absent.
snap_cpu() {
  git fetch -q origin perf-data 2>/dev/null
  git show "origin/perf-data:timeline/$1" 2>/dev/null | jq -r '.machine.cpu_model // empty'
}

dispatch_and_wait() {
  local sha="$1" before after id status concl
  before="$(gh run list -R "$REPO" --workflow="$WF" --limit 1 --json databaseId -q '.[0].databaseId // 0')"

  gh workflow run "$WF" -R "$REPO" -f ref="$sha" || return 1

  # The run appears a few seconds after dispatch; identify it as "newer than before".
  for _ in $(seq 1 20); do
    sleep 5
    after="$(gh run list -R "$REPO" --workflow="$WF" --limit 1 --json databaseId -q '.[0].databaseId // 0')"
    [ "$after" != "$before" ] && { id="$after"; break; }
  done
  [ -n "${id:-}" ] || { log "  ! run never appeared"; return 1; }
  log "  run $id dispatched"

  while :; do
    read -r status concl < <(gh run view "$id" -R "$REPO" --json status,conclusion \
      -q '"\(.status) \(.conclusion // "-")"')
    [ "$status" = "completed" ] && break
    sleep "$POLL"
  done
  log "  run $id → $concl"
  [ "$concl" = "success" ]
}

total=$#; n=0; ok=0; failed=()
for sha in "$@"; do
  n=$((n + 1))
  name="$(snap_name "$sha")" || { log "[$n/$total] ${sha:0:12} UNKNOWN COMMIT — skipped"; failed+=("$sha"); continue; }
  log "[$n/$total] ${sha:0:12}  $(git show -s --format=%s "$sha" | cut -c1-60)"

  landed=""
  for try in $(seq 1 "$MAX_TRIES"); do
    [ "$try" -gt 1 ] && log "  retry $try/$MAX_TRIES (last attempt was off-tier)"
    dispatch_and_wait "$sha" || { log "  ! workflow failed"; continue; }
    cpu="$(snap_cpu "$name")"
    if [ -z "$cpu" ]; then
      log "  ! no snapshot at timeline/$name"
      continue
    fi
    log "  cpu: $cpu"
    case "$cpu" in *"$REF_CPU"*) landed="$cpu"; break ;; esac
  done

  if [ -n "$landed" ]; then
    ok=$((ok + 1)); log "  ✓ on reference tier"
  else
    failed+=("$sha"); log "  ✗ gave up after $MAX_TRIES tries"
  fi
done

log "done: $ok/$total on reference tier"
if [ ${#failed[@]} -gt 0 ]; then
  log "not landed: ${failed[*]}"
  exit 1
fi
