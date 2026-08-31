#!/usr/bin/env bash
# Integrate a perf-session's snapshots into the perf-data corpus.
#
# Usage: scripts/perf-session-publish.sh [options] <session-dir>
#
#   --remote NAME   remote holding perf-data (default: origin)
#   --commit        actually commit (default: stage and show the diff only)
#   --push          push to perf-data (implies --commit; never the default)
#   --force         overwrite snapshots that already exist for these commits
#
# WHAT LANDS, AND WHY IT DOES NOT DISTURB THE EXISTING SERIES
#
# A session's snapshots carry this host's machine key, so they form their OWN
# tier series alongside the CI tiers rather than mixing with them. That is
# correct rather than a compromise: normalizing across CPU models leaves a
# ~10.6% residual, so tiers must never be compared
# (project-docs/docs/paserati/perf-micro-noise-and-anchor-results.md). What the
# session tier gives you that no CI tier can is that its points were measured
# against each other on one machine, so WITHIN it small deltas are real.
#
# Across sessions on different hardware, ratio_to_anchor is what carries — the
# same document shows the anchor absorbing 100% of a 22.4% hardware split
# inside a single CPU model, which is exactly this case.
#
# This script never pushes unless asked. perf-data is the corpus; a bad write
# is recoverable only from a tag, and the tags are made by hand.
set -euo pipefail

REMOTE=origin
DO_COMMIT=0
DO_PUSH=0
FORCE=0
SESSION=""

die() { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --remote) REMOTE="${2:?}"; shift 2;;
    --commit) DO_COMMIT=1; shift;;
    --push)   DO_PUSH=1; DO_COMMIT=1; shift;;
    --force)  FORCE=1; shift;;
    -h|--help) sed -n '2,26p' "$0"; exit 0;;
    -*) die "unknown option: $1";;
    *) SESSION="$1"; shift;;
  esac
done

[ -n "$SESSION" ] || die "give a session directory"
[ -d "$SESSION/snapshots" ] || die "$SESSION has no snapshots/ — is it a session dir?"
[ -f "$SESSION/session.json" ] || die "$SESSION has no session.json"
SESSION="$(cd "$SESSION" && pwd)"

repo="$(git rev-parse --show-toplevel)"; cd "$repo"

mkey="$(jq -r '.session.machine_key' "$SESSION/session.json")"
rounds="$(jq -r '.session.rounds' "$SESSION/session.json")"
drift="$(jq -r '.session.anchor_drift_pct' "$SESSION/session.json")"
note "session: key=${mkey} rounds=${rounds} anchor_drift=${drift}%"

# A session whose anchor wandered did not hold still, and its whole value was
# holding still. Publishing it puts a point on the chart that looks as
# authoritative as a good one. Require an explicit override.
awk -v d="$drift" -v f="$FORCE" 'BEGIN{ if (d+0 > 2 && f+0 == 0) exit 1 }' \
  || die "anchor drifted ${drift}% across rounds (>2%) — this session did not hold still. Re-run it, or pass --force if you have a reason."

shopt -s nullglob
snaps=("$SESSION"/snapshots/*.json)
[ "${#snaps[@]}" -gt 0 ] || die "no snapshots in $SESSION/snapshots"

# Every snapshot must be v2 and must carry THIS session's key. A v1 file here
# means perf-migrate did not run; a foreign key means the directory was mixed.
for f in "${snaps[@]}"; do
  k="$(jq -r 'if .machines then (.machines|keys[0]) else "V1" end' "$f")"
  [ "$k" = "$mkey" ] || die "$(basename "$f"): machine key '$k' != session key '$mkey'"
done
note "${#snaps[@]} snapshot(s), all v2, all ${mkey}"

wt="$(mktemp -d)/perf-data"
cleanup() { rm -rf "$(dirname "$wt")"; }
trap cleanup EXIT
note "fetching ${REMOTE}/perf-data"
git fetch --no-tags --quiet "$REMOTE" perf-data || die "cannot fetch ${REMOTE}/perf-data"
git worktree add --detach --quiet "$wt" FETCH_HEAD || die "cannot create perf-data worktree"
trap 'git worktree remove --force "$wt" 2>/dev/null || true; git worktree prune; cleanup' EXIT
note "perf-data at $(git -C "$wt" rev-parse --short HEAD)"

for f in "${snaps[@]}"; do
  dest="$wt/timeline/$(basename "$f")"
  if [ -e "$dest" ] && [ "$FORCE" -eq 0 ]; then
    die "$(basename "$f") already exists on perf-data. Re-measuring the same commit on the same machine is a legitimate thing to want, but it OVERWRITES the earlier session. Pass --force if that is the intent."
  fi
  cp "$f" "$dest"
done
note "staged ${#snaps[@]} file(s) into timeline/"

# The same gate the CI publishes behind. It holds each entry to the anchor it
# was measured against, which is what makes an off-run measurement checkable.
note "running perf-fixratio -verify"
go run ./cmd/perf-fixratio -dir "$wt/timeline" -verify -v || die "perf-fixratio -verify failed — nothing committed"

git -C "$wt" add timeline
if git -C "$wt" diff --cached --quiet; then
  note "nothing changed — these snapshots are already present and identical"
  exit 0
fi
echo
git -C "$wt" diff --cached --stat | tail -20
echo

if [ "$DO_COMMIT" -eq 0 ]; then
  note "dry run. Re-run with --commit to commit, --push to publish."
  exit 0
fi

shas="$(jq -r '.session.commits | join(" ")' "$SESSION/session.json")"
git -C "$wt" commit -q -F - <<EOF
perf: session snapshots for ${#snaps[@]} commit(s) on ${mkey}

Measured in one session on one machine, ${rounds} rounds interleaved, combined
by median-of-round-medians. Anchor drift across rounds ${drift}%.

Commits: ${shas}

These share a host, so deltas BETWEEN them are real at a resolution the CI
tiers cannot reach — the between-host term that dominates those cancels here.
They form their own tier series and must not be compared against another
tier's points.
EOF
note "committed $(git -C "$wt" rev-parse --short HEAD) on a detached perf-data"

if [ "$DO_PUSH" -eq 1 ]; then
  git -C "$wt" push "$REMOTE" HEAD:perf-data

  # A write nobody redeploys is a write nobody sees. pages.yml builds the page
  # from a checkout of perf-data, and it wakes on exactly three things: a push to
  # main touching the page itself, a workflow_run of the two workflows that write
  # timeline points, and a manual dispatch. A session publish is none of them —
  # it is this script pushing straight to perf-data — so without the dispatch
  # below the snapshots land in the corpus and the published page keeps serving
  # the previous series indefinitely.
  #
  # The backfill hit this same gap on 2026-08-09 (three points invisible until
  # someone dispatched by hand) and was fixed by adding it to workflow_run. That
  # route is not open here, and `on.push` cannot express "main with these paths OR
  # perf-data with any path", so ask for the deploy explicitly.
  #
  # Never fatal: the snapshots are already pushed and correct at this point, and
  # a page that lags is a smaller problem than a publish that reports failure
  # after having succeeded.
  slug="$(git remote get-url "$REMOTE" 2>/dev/null \
          | sed -E 's#(git@|https://)github\.com[:/]##; s#\.git$##')"
  if ! command -v gh >/dev/null; then
    note "pushed to ${REMOTE}/perf-data"
    note "gh not on PATH — the page will not refresh until: gh workflow run 'Perf Page'"
  elif gh workflow run "Perf Page" --repo "$slug" >/dev/null 2>&1; then
    note "pushed to ${REMOTE}/perf-data; requested a Perf Page deploy"
  else
    note "pushed to ${REMOTE}/perf-data"
    note "could not dispatch Perf Page — run: gh workflow run 'Perf Page' --repo ${slug}"
  fi
else
  note "NOT pushed. To publish:  git -C <worktree> push ${REMOTE} HEAD:perf-data"
  note "worktree is temporary — re-run with --push instead."
fi
