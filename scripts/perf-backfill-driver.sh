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
#     minutes and never renders — retry until it lands on the reference tier.
#
#     Under v2 an off-tier attempt no longer OVERWRITES the previous one: snapshot
#     filenames carry a machine slug, so each tier keeps its own file. A retry adds
#     a file rather than replacing one, and the question is whether ANY file for
#     this commit is on the reference tier.
#
# Usage: backfill.sh <sha>...
# Env:   REPO=mparrett/paserati  REF_CPU='EPYC 7763'  MAX_TRIES=3  FORCE=1

set -uo pipefail

REPO="${REPO:-mparrett/paserati}"
REF_CPU="${REF_CPU:-EPYC 7763}"
MAX_TRIES="${MAX_TRIES:-3}"
WF="perf-timeline.yml"
POLL=30

[ $# -gt 0 ] || { echo "usage: $0 <sha>..." >&2; exit 2; }

log() { printf '%s  %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }

# Snapshot filenames start with the COMMIT stamp and sha, so the prefix is
# predictable locally. NOT the full name: v2 appends a machine slug
# ("<stamp>-<sha>-<machine-slug>.json"), so matching on an exact v1-shaped name
# finds nothing and every target looks like a failed dispatch.
snap_prefix() {
  local sha="$1" epoch
  epoch="$(git show -s --format=%ct "$sha")" || return 1
  printf '%s-%s' "$(date -u -r "$epoch" +%Y%m%dT%H%M%SZ)" "$(git rev-parse --short=12 "$sha")"
}

# Every cpu_model recorded for this commit, one per line, empty if none. Reads
# both layouts: v1 carries .machine at the top level, v2 nests one profile per
# machine under .machines.
snap_cpus() {
  local prefix="$1" f
  git fetch -q origin perf-data 2>/dev/null
  git ls-tree --name-only "origin/perf-data" timeline/ 2>/dev/null \
    | grep -E "^timeline/${prefix}(-|\.)" \
    | while IFS= read -r f; do
        git show "origin/perf-data:$f" 2>/dev/null \
          | jq -r '(.machines[]? // .) | .machine.cpu_model // empty'
      done
}

# Refuse to start while another perf-data writer is active or pending.
#
# Serializing our OWN dispatches is not enough. A dispatch from a second driver
# (or a hand-run of either writer workflow) evicts whatever this one has pending,
# and stopping the other driver afterwards does not bring the evicted run back —
# that cost a snapshot once already. The group allows exactly one pending run, so
# the only safe number of concurrent drivers is one.
assert_sole_writer() {
  local busy
  busy="$(gh api "/repos/${REPO}/actions/runs?per_page=100" --jq '
    [.workflow_runs[]
     | select(.path == ".github/workflows/perf-timeline.yml"
           or .path == ".github/workflows/perf-test262-backfill.yml")
     | select(.status == "queued" or .status == "in_progress")
     | "\(.status) \(.html_url)"] | .[]' 2>/dev/null || true)"
  [ -z "$busy" ] && return 0
  echo "another perf-data writer is already running or pending:" >&2
  printf '%s\n' "$busy" | sed 's/^/  /' >&2
  echo "Dispatching now would evict its pending run. Wait for it, or set FORCE=1." >&2
  [ -n "${FORCE:-}" ]
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

assert_sole_writer || exit 1

total=$#; n=0; ok=0; failed=()
for sha in "$@"; do
  n=$((n + 1))
  prefix="$(snap_prefix "$sha")" || { log "[$n/$total] ${sha:0:12} UNKNOWN COMMIT — skipped"; failed+=("$sha"); continue; }
  log "[$n/$total] ${sha:0:12}  $(git show -s --format=%s "$sha" | cut -c1-60)"

  landed=""
  for try in $(seq 1 "$MAX_TRIES"); do
    [ "$try" -gt 1 ] && log "  retry $try/$MAX_TRIES (last attempt was off-tier)"
    dispatch_and_wait "$sha" || { log "  ! workflow failed"; continue; }
    cpus="$(snap_cpus "$prefix")"
    if [ -z "$cpus" ]; then
      log "  ! no snapshot matching timeline/${prefix}*"
      continue
    fi
    log "  cpu(s): $(printf '%s' "$cpus" | paste -sd'; ' -)"
    case "$cpus" in *"$REF_CPU"*) landed="$REF_CPU"; break ;; esac
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
