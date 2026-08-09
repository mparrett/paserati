#!/usr/bin/env bash
# Backfill the test262 macro into ONE target's snapshot. Called once per target
# by perf-test262-backfill.yml.
#
# WHY A SEPARATE PROCESS, NOT A SHELL FUNCTION
#
# The workflow measures several targets per run, and one target failing must not
# kill the batch — the others are unaffected and the runner is already won. That
# needs `set -e` to abort THIS target and return, which neither a function nor a
# subshell gives you: bash suppresses -e for any compound command that is part of
# an AND-OR list, so `( set -e; body ) || rc=$?` runs straight past its own
# failures. Verified, not assumed. A child process keeps its own -e, so the
# caller gets a real exit status and the batch continues.
#
# Exit codes are the interface:
#   0  measured and pushed
#   3  measured, nothing to write (entry already present and identical)
#   1  this target failed; the message says why
set -euo pipefail

TARGET="${1:?usage: perf-backfill-one.sh <target-sha>}"
TOOL="${TOOL_WT:?TOOL_WT (tool worktree) must be set}"
WT="${PERF_DATA_WT:?PERF_DATA_WT (perf-data clone) must be set}"
KEY="${THIS_KEY:?THIS_KEY (machine key) must be set}"
REMOTE="${PERF_DATA_REMOTE:?PERF_DATA_REMOTE must be set}"
REDUCE="${REDUCE_SH:?REDUCE_SH must be set}"

fail() { echo "::error::${TARGET}: $*" >&2; exit 1; }

# Orphan fork-CI commits are not reachable from any remote ref, so checkout@v4
# never fetched them and `git checkout` dies with an opaque "unable to read tree
# <sha>". 14 of the 47 existing snapshots sit on such commits. The fix is a
# temporary `backfill/*` ref; say so.
if git rev-parse --verify -q "${TARGET}^{commit}" >/dev/null; then
  git checkout --force "$TARGET"
else
  git fetch --no-tags origin "$TARGET" >/dev/null 2>&1 || true
  git rev-parse --verify -q 'FETCH_HEAD^{commit}' >/dev/null \
    || fail "not reachable from any ref on origin. Publish a temporary ref and re-dispatch: git push origin ${TARGET}:refs/heads/backfill/${TARGET}"
  git checkout --force FETCH_HEAD
fi

# Per-target because --force just discarded the previous target's copy.
#
# The driver is a shell script with no build graph, so copying it into the target
# tree is safe in a way that copying Go source is not — it shells out to two
# binaries whose provenance we control.
#
# mkdir first: targets old enough to need this workflow can predate the scripts/
# directory entirely. ccf2da20f14f (2026-06-27) has no scripts/ at all, so the
# copy failed on the destination rather than the source — a distinction the error
# message ("No such file or directory") does not make, and which cost a run.
mkdir -p ./scripts
cp "$TOOL/scripts/macro-test262.sh" ./scripts/macro-test262.sh
chmod +x ./scripts/macro-test262.sh

./setup-test262.sh
go build -o ./paserati-test262 ./cmd/paserati-test262

# An old engine's -json output has to be readable by today's post-processor. It
# normally is, but "normally" is how the ratio bug survived a month. Prove it on
# one cheap directory before spending 30 minutes on the full corpus: schema drift
# would otherwise surface as a plausible-looking number, not as an error.
#
# The field names are the JSON TAGS (lowercase). pkg/test262.Result tags them
# path/passed/failed/timedOut/skipped/duration. Probing for .Passed/.Duration —
# the Go field names, which is what cmd/bench-test262 reads because encoding/json
# maps them — reports every engine as mute, and would condemn every historical
# commit as un-backfillable on the strength of a capital letter.
probe="${RUNNER_TEMP:-/tmp}/probe.${TARGET}.json"
./paserati-test262 -path ./test262 -timeout 1s -subpath 'built-ins/Boolean/**' -json \
  > "$probe" 2>/dev/null || true
n="$(jq '.results | length' "$probe" 2>/dev/null || echo 0)"
[ "${n:-0}" -gt 0 ] || fail "engine produced no parseable -json results"
passed="$(jq '[.results[] | select(.passed == true)] | length' "$probe")"
dur="$(jq '[.results[] | select((.duration // 0) > 0)] | length' "$probe")"
timed="$(jq '[.results[] | select(has("timedOut"))] | length' "$probe")"
echo "probe: ${n} results, ${passed} passed, ${dur} with a duration, ${timed} carry timedOut"
if [ "$passed" -eq 0 ] || [ "$dur" -eq 0 ]; then
  # Print a record before dying. Whether the cause is a real schema change or a
  # wrong path in the probe, the sample answers it in one glance — which the
  # counts alone conspicuously did not.
  echo "sample result: $(jq -c '.results[0]' "$probe")" >&2
  [ "$passed" -gt 0 ] || fail "probe produced no passing tests; the metric would be empty"
  fail "probe results carry no duration; the metric would be zero"
fi
[ "$timed" -gt 0 ] || echo "::warning::${TARGET}: results have no timedOut field — timed-out tests cannot be excluded from the sum"

short_sha="$(git rev-parse --short=12 HEAD)"

# Fresh anchor on THIS runner, in the same run as the macro. It is NOT the anchor
# stored on the target's snapshot — that one calibrated a different run on a
# different host — so the reducer records it alongside the measurement as
# anchor_ns_per_op rather than leaving it implicit.
anchor_ns="$(go test -run '^$' -bench '^BenchmarkRatchetAnchor$' -count 3 ./pkg/vm 2>/dev/null \
  | awk '/BenchmarkRatchetAnchor/ { sum += $3; n++ } END { if (n) printf "%.6f", sum/n }')"
[ -n "$anchor_ns" ] || fail "failed to measure anchor"

# The SAME reducer the forward timeline runs, so a backfilled point and a forward
# point on one series cannot have been reduced differently.
#
# MACRO_RECORD_ANCHOR=1 is the one difference: this macro is measured in a
# different run from the profile's micro benchmarks, so it cannot be normalized
# against the profile's anchor and carries its own divisor instead.
# perf-fixratio -verify reads that field and holds each entry to the anchor it was
# actually measured against, which is what makes the off-run measurement checkable
# rather than merely defensible.
entries="${RUNNER_TEMP:-/tmp}/t262.entries.${TARGET}.json"
MACRO_RECORD_ANCHOR=1 "$REDUCE" "$anchor_ns" "$entries" >/dev/null
echo "target ${short_sha}: $(jq -c 'map_values({ns_per_op, ratio_to_anchor, set_hash})' "$entries")"

# Find the snapshot for THIS RUNNER'S TIER, not an arbitrary one.
#
# A commit holds one FILE per tier under v2 — ccf2da20f14f has four — so
# `find | head -1` selected a machine at random and then refused because it wasn't
# ours, while the file we should have written sat beside it. Ratios normalize
# within a CPU model, so writing this runner's number into another tier's profile
# merges two things the format exists to keep apart.
snap=""; seen=""
while IFS= read -r f; do
  [ -n "$f" ] || continue
  if jq -e '.machines' "$f" >/dev/null 2>&1; then
    fk="$(jq -r '.machines | keys[0]' "$f")"
  else
    fk="$(jq -r '.machine.arch + "/" + .machine.cpu_model' "$f")"
  fi
  seen="${seen}${seen:+, }${fk}"
  if [ "$fk" = "$KEY" ]; then snap="$f"; break; fi
done < <(find "$WT/timeline" \( -name "*-${short_sha}.json" -o -name "*-${short_sha}-*.json" \) | sort)

[ -n "$seen" ] || fail "no snapshot at all for ${short_sha} — run the timeline first"
[ -n "$snap" ] || fail "no snapshot on ${KEY} (tiers present: ${seen}). The selector passed this target, so perf-data moved under us; re-dispatch."
echo "target snapshot: $(basename "$snap")"

if jq -e '.machines' "$snap" >/dev/null 2>&1; then
  # shellcheck disable=SC2016  # $k is jq's parameter, bound by --arg below
  jqtarget='.machines[$k].benchmarks'
else
  jqtarget='.benchmarks'
fi

# The entries arrive already stamped with anchor_ns_per_op; this only merges them
# in. Merge rather than assign: it is one entry per series, and an assignment to a
# fixed key would drop the per-suite records on the floor.
jq --arg k "$KEY" --slurpfile e "$entries" "${jqtarget} += \$e[0]" "$snap" > "$snap.tmp"
mv "$snap.tmp" "$snap"

# Same gate the forward timeline publishes behind, run from the workflow's commit
# for the same reason: the target tree predates the tool.
( cd "$TOOL" && go run ./cmd/perf-fixratio -dir "$WT/timeline" -verify -v )

git -C "$WT" add timeline
if git -C "$WT" diff --cached --quiet; then
  echo "nothing changed — entry already present and identical"
  exit 3
fi
git -C "$WT" commit -q -m "perf: backfill test262 macro into $(basename "$snap")"
# Push per target so a batch that times out or is cancelled keeps finished work.
git -C "$WT" push -q "$REMOTE" HEAD:perf-data
echo "pushed"
