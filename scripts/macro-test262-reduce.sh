#!/usr/bin/env bash
# Run the Test262 macro-benchmark N times and reduce the reps into the single
# `test262.total` benchmark entry the timeline stores. Emits the entry as JSON.
#
# Usage: scripts/macro-test262-reduce.sh <count> <anchor_ns> [entry-out.json]
#
# Emits on stdout (and to <entry-out.json> if given). Everything else — driver
# output, per-rep lines, annotations — goes to stderr.
#
# Exit codes:
#   0  entry written
#   2  the reps disagree about WHICH tests they timed; see below
#   3  no passing tests, so the mean is undefined and there is nothing to record
#   1  anything else (usage, driver failure)
#
# REDUCER: MEDIAN of the per-rep means.
#
# It was min, on the same reasoning as the bench-ratchet A/B reducer (#22): suite
# noise is one-directional-slower, so the fastest rep is the least contaminated
# one. That reasoning is sound for the noise it describes and wrong for the noise
# that actually bit us. Two commits (976a3faa, c989b254) sat 25% FAST of their
# neighbours for weeks and read as a real speedup. Re-measured, both landed inside
# the band, and both carried the same set_hash as the entire modern series — so the
# passing set was never different and composition was never the cause. The variance
# was in TIMING, and min-of-3 selects the luckiest of three timings by construction.
# Min preserves a fast-side artifact; median rejects it. A commit that changed no
# engine code cannot get faster, so the reducer must be able to say so.
#
# Every rep is kept in samples[], not just the winner. Two of the three used to be
# discarded, which left `test262.total` with samples: 1 on every snapshot in the
# corpus and the macro series with no variance data at all — no point on the chart
# had a spread, and the run-to-run scatter cited in #28 had to be estimated from
# outside the corpus. Keeping them is also what makes the median computable, and
# what a later read-modify-write accumulation across runs would extend.
#
# CONFORMANCE: the entry also carries the run's Test262 stats, which the timeline
# used to discard. `iterations` was already the passing count and the pass rate's
# numerator; Total, the denominator, was written to a stats file that nothing read,
# so there has never been a conformance series. It is deliberately NOT folded into
# the speed metric — test262.total was changed from a sum to a mean precisely to
# stop conflating per-test speed with pass count (#26). Keeping them apart was
# right; giving conformance its own record is what never happened.
#
# COMPOSITION: the reps must agree on set_hash, and this refuses to write if they
# don't. The metric is a mean over the passing, non-timed-out set; if a borderline
# test times out in one rep and not another, the reps summarize different sets and
# reducing across them mixes speed with membership. The old loop asserted in a
# comment that "passing count and set_hash are run-invariant" and checked nothing.
# Empirically they are invariant — 38 reference-tier points share one set_hash — so
# this should approximately never fire, and if it does the point genuinely is not
# comparable to its neighbours. A missing macro point is visible in perf-gaps.sh
# section [2]; a silently mis-composed one is not.
#
# Env:
#   MACRO_DRIVER         driver run once per rep (default ./scripts/macro-test262.sh).
#                        The driver must come from the tree being MEASURED; this
#                        reducer is post-processing and should come from the
#                        workflow's own commit, the same split bench-test262 uses.
#   MACRO_WORK           scratch dir for rep output (default $RUNNER_TEMP, else /tmp)
#   MACRO_RECORD_ANCHOR  1 -> stamp anchor_ns_per_op on the entry and every sample.
#                        For the backfill, whose macro is measured in a different
#                        run from the profile's micro benchmarks and so cannot be
#                        normalized against the profile's own anchor. The forward
#                        timeline measures both in one run and must leave it unset.
set -euo pipefail

count="${1:?usage: macro-test262-reduce.sh <count> <anchor_ns> [entry-out.json]}"
anchor_ns="${2:?usage: macro-test262-reduce.sh <count> <anchor_ns> [entry-out.json]}"
entry_out="${3:-}"

driver="${MACRO_DRIVER:-./scripts/macro-test262.sh}"
work="${MACRO_WORK:-${RUNNER_TEMP:-/tmp}}"
[ -x "$driver" ] || { echo "::error::macro driver not found or not executable: ${driver}" >&2; exit 1; }
mkdir -p "$work"

reps="${work}/t262.reps.jsonl"
: > "$reps"

for rep in $(seq 1 "$count"); do
  "$driver" "${work}/t262.jsonl" "${work}/t262.stats.json" >&2
  # captured_at is per-rep because samples accumulated across runs each need to
  # say when they were taken; see BenchmarkSample.CapturedAt.
  #
  # The stats file is read here rather than left on the runner. The driver has
  # always written Total/Passed/Failed/Timeouts and the PR-side A/B workflow has
  # always printed them; the timeline threw the file away, three times per
  # snapshot, so the corpus had a speed series and no conformance series at all.
  # test262.total's `iterations` is the numerator (the passing count) and always
  # was — what was missing is Total, without which it cannot be made a rate.
  jq -c --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --slurpfile s "${work}/t262.stats.json" \
    'select(.name=="total") | {
       ns: .ns_per_op, passed: .iterations, set_hash: (.set_hash // ""), captured_at: $at,
       stats: ($s[0] | {
         total: .Total, passed: .Passed, failed: .Failed,
         timeouts: .Timeouts, skipped: .Skipped, duration_ns: .Duration
       }),
       per_test_timeout: ($s[0].PerTestTimeout // "")
     }' "${work}/t262.jsonl" >> "$reps"
  echo "  macro rep ${rep}/${count}: $(jq -c '{ns, passed, set_hash, stats}' <<<"$(tail -n 1 "$reps")")" >&2
done

n="$(grep -c . "$reps" || true)"
[ "$n" -eq "$count" ] || { echo "::error::expected ${count} macro reps, got ${n}" >&2; exit 1; }

# Zero passing is a real outcome for an old or broken engine, not a fault here.
max_passed="$(jq -s 'map(.passed // 0) | max' "$reps")"
[ "${max_passed:-0}" -gt 0 ] || { echo "no passing tests over ${count} reps" >&2; exit 3; }

distinct_hashes="$(jq -r '.set_hash' "$reps" | sort -u | wc -l | tr -d ' ')"
named_hashes="$(jq -r '.set_hash' "$reps" | sort -u | grep -c . || true)"
distinct_counts="$(jq -r '.passed' "$reps" | sort -u | wc -l | tr -d ' ')"
# Fall back to the pass count when no rep carries a set_hash: a tree predating
# SetHash gives a weaker check, and a weaker check is still worth making.
if [ "$distinct_hashes" -gt 1 ] || { [ "$named_hashes" -eq 0 ] && [ "$distinct_counts" -gt 1 ]; }; then
  echo "::error::macro reps disagree on the passing set — refusing to reduce across them" >&2
  jq -c '{passed, set_hash}' "$reps" >&2
  exit 2
fi

# Total is the corpus size, so a disagreement means the reps ran different suites
# — a different .test262-rev or a truncated shard enumeration. That is not noise
# and the pass rate it would produce is not a rate of anything.
if [ "$(jq -r '.stats.total' "$reps" | sort -u | wc -l | tr -d ' ')" -gt 1 ]; then
  echo "::error::macro reps disagree on the corpus size — refusing to reduce across them" >&2
  jq -c '.stats' "$reps" >&2
  exit 2
fi

# The metric is the MEAN per-test ns (summed_ns / passing_count), not the sum: the
# sum conflates per-test speed with pass-count (#26). Reduce over the per-rep means
# rather than the per-rep sums — identical while the pass count is stable, which the
# check above has just established, and correct rather than incidentally correct.
entry="$(jq -s --argjson a "$anchor_ns" --argjson stamp "${MACRO_RECORD_ANCHOR:-0}" '
  map(. + { mean: (.ns / .passed) })                                as $reps
  | ($reps | sort_by(.mean))                                        as $ranked
  | ($ranked | map(.mean))                                          as $sorted
  | ($sorted | length)                                              as $n
  | (if $n % 2 == 1 then $sorted[($n / 2 | floor)]
     else ($sorted[($n / 2 | floor) - 1] + $sorted[($n / 2 | floor)]) / 2 end) as $med
  # Conformance comes from the middle rep — for an odd count, the very rep that
  # supplied the median, so the number and the set it was measured over describe
  # one run rather than a composite. total/passed are identical across reps by the
  # checks above; the failed/timeout split can wobble when a slow failure lands
  # either side of the bound, which is why per_test_timeout travels with it.
  | ($ranked[(($n - 1) / 2 | floor)])                               as $mid
  | {
      ns_per_op: $med,
      allocs_per_op: 0,
      bytes_per_op: 0,
      ratio_to_anchor: ($med / $a),
      method: ({ reducer: "median", count: $n }
               + (if $mid.per_test_timeout == "" then {}
                  else { per_test_timeout: $mid.per_test_timeout } end)),
      stats: $mid.stats,
      samples: ($reps | map({
        iterations: .passed,
        ns_per_op: .mean,
        ratio_to_anchor: (.mean / $a),
        captured_at: .captured_at
      }))
    }
  + (if $reps[0].set_hash == "" then {} else { set_hash: $reps[0].set_hash } end)
  + (if $stamp == 1 then { anchor_ns_per_op: $a } else {} end)
  | if $stamp == 1 then .samples |= map(. + { anchor_ns_per_op: $a }) else . end
' "$reps")"

if [ -n "$entry_out" ]; then
  printf '%s\n' "$entry" > "$entry_out"
fi
printf '%s\n' "$entry"

jq -r '"macro: median \(.ns_per_op) ns/test over \(.samples[0].iterations) passing " +
       "(set \(.set_hash // "none"), \(.samples | length) reps " +
       "\(.samples | map(.ns_per_op) | min) .. \(.samples | map(.ns_per_op) | max)), " +
       "ratio \(.ratio_to_anchor)\n" +
       "conformance: \(.stats.passed)/\(.stats.total) passing " +
       "(\((.stats.passed / .stats.total * 10000 | round) / 100)%), " +
       "\(.stats.failed) failed, \(.stats.timeouts) timed out at \(.method.per_test_timeout // "?")"' \
  <<<"$entry" >&2
