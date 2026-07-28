#!/usr/bin/env bash
# Run the Test262 macro-benchmark N times and reduce the reps into the benchmark
# entries the timeline stores. Emits a {name: entry} object to merge into a
# snapshot's `benchmarks`.
#
# Usage: scripts/macro-test262-reduce.sh <anchor_ns> [entries-out.json]
#
# Emits on stdout (and to <entries-out.json> if given). Everything else — driver
# output, per-rep lines, annotations — goes to stderr.
#
# Exit codes:
#   0  entries written
#   2  the reps disagree about WHICH tests they timed; see below
#   3  no passing tests, so the mean is undefined and there is nothing to record
#   1  anything else (usage, driver failure)
#
# ONE ENTRY PER SERIES, not just the total. cmd/bench-test262 emits test262/total
# plus one record per top-level suite, and its doc comment says outright that the
# per-suite rows exist "for the page's Breakdown toggle" — but the timeline folded
# in only the total, so the macro side of the corpus was one series where it should
# be three, and the page's Test262 Breakdown toggle was a no-op with nothing to
# break down. The suites are not redundant with the total: 17009 built-ins and
# 23222 language tests sum to the 40231 the total reports, and a conformance or
# speed change confined to one of them is invisible in the sum. Note for any
# consumer aggregating these: total IS built-ins + language, so counting all three
# double-counts the macro.
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
# comparable to its neighbours. A missing macro point is detectable — it is what
# section [2] of scripts/perf-gaps.sh reports — where a silently mis-composed one
# is not. Note that tool lives on the `tools/perf-runbook` branch and is NOT on
# main, so on main that detection is a thing you can run, not a thing that runs.
#
# Env:
#   MACRO_COUNT          reps to run and reduce over (default 3). Both callers used
#                        to spell this default themselves and pass it positionally.
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

anchor_ns="${1:?usage: macro-test262-reduce.sh <anchor_ns> [entries-out.json]}"
entries_out="${2:-}"
count="${MACRO_COUNT:-3}"

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
  jq -sc --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --slurpfile s "${work}/t262.stats.json" '{
       captured_at: $at,
       stats: ($s[0] | {
         total: .Total, passed: .Passed, failed: .Failed,
         timeouts: .Timeouts, skipped: .Skipped, duration_ns: .Duration
       }),
       per_test_timeout: ($s[0].PerTestTimeout // ""),
       series: (map({
         key: ("test262." + .name),
         value: { ns: .ns_per_op, passed: .iterations, set_hash: (.set_hash // "") }
       }) | from_entries)
     }' "${work}/t262.jsonl" >> "$reps"
  echo "  macro rep ${rep}/${count}: $(jq -c '{stats: .stats, series: (.series | map_values(.passed))}' <<<"$(tail -n 1 "$reps")")" >&2
done

# Each guard refuses the whole reduction rather than reduce across reps that
# measured different things, and dumps the reps first: by this point ~9 minutes of
# suite time has produced exactly the evidence needed to diagnose the disagreement,
# and it dies with the runner otherwise.
refuse() {
  echo "::error::macro reps disagree on $1 — refusing to reduce across them" >&2
  cat "$reps" >&2
  exit 2
}
# Distinct values of a jq expression across the reps. The idiom was written out
# three times with slight variation, which is the copy-paste-with-drift this script
# exists to remove from the workflows.
distinct() { jq -r "$1" "$reps" | sort -u | wc -l | tr -d ' '; }

# Zero passing is a real outcome for an old or broken engine, not a fault here.
# Checked over EVERY series rather than just the total: the reduction divides per
# series (ns / passed), so a suite passing nothing divides by zero and jq dies with
# an internals message instead of taking the graceful exit this code exists for.
min_passed="$(jq -s '[.[].series[].passed // 0] | min' "$reps")"
[ "${min_passed:-0}" -gt 0 ] || { echo "no passing tests over ${count} reps" >&2; exit 3; }

# A suite present in one rep and not another means the shard enumeration was
# truncated; the median would then be over a different number of observations per
# series without saying so.
[ "$(distinct '.series | keys | join(",")')" -eq 1 ] || refuse "which suites ran"

# Total is the corpus size, so a disagreement means the reps ran different corpora
# — a different .test262-rev, or a truncated enumeration. The pass rate that would
# produce is not a rate of anything.
[ "$(distinct '.stats.total')" -eq 1 ] || refuse "the corpus size"

# Composition, PER SERIES rather than on the total alone: a drift in built-ins
# offset by one in language leaves the total's count and hash unchanged while both
# suite series move — exactly the case the per-suite records were added to expose.
# Where no rep carries a set_hash (a tree predating SetHash) the pass count is the
# weaker fallback, and a weaker check is still worth making. One jq over all series
# rather than three per series.
bad="$(jq -sr '
  [ (.[0].series | keys[]) as $k
    | ([.[] | .series[$k].set_hash] | unique) as $h
    | ([.[] | .series[$k].passed]   | unique) as $p
    | select(($h | length) > 1
             or (($h | map(select(. != "")) | length) == 0 and ($p | length) > 1))
    | $k ]
  | join(", ")' "$reps")"
[ -z "$bad" ] || refuse "the passing set of ${bad}"

# The metric is the MEAN per-test ns (summed_ns / passing_count), not the sum: the
# sum conflates per-test speed with pass-count (#26). Reduce over the per-rep means
# rather than the per-rep sums — identical while the pass count is stable, which the
# check above has just established, and correct rather than incidentally correct.
entry="$(jq -s --argjson a "$anchor_ns" --argjson stamp "${MACRO_RECORD_ANCHOR:-0}" '
  # $obs: one observation per rep for a single series. $withstats: whether the
  # whole-run conformance counts belong on this entry (the total only).
  #
  # One sort serves both the median and the rep the conformance counts come from.
  # They used to come from two independent sorts, with a comment explaining that for
  # an odd count they pick the same rep — now they do so by construction. That is
  # the property being relied on: the number and the set it was measured over
  # describe one run rather than a composite. total/passed are identical across reps
  # by the checks above; the failed/timeout split can wobble when a slow failure
  # lands either side of the bound, which is why per_test_timeout travels with it.
  def build($obs; $ptt; $withstats):
    ($obs | sort_by(.mean))                as $ranked
    | ($ranked | length)                   as $n
    | ($ranked[(($n - 1) / 2 | floor)])    as $mid
    | (if $n % 2 == 1 then $mid.mean
       else ($mid.mean + $ranked[$n / 2].mean) / 2 end) as $med
    | {
        ns_per_op: $med,
        allocs_per_op: 0,
        bytes_per_op: 0,
        ratio_to_anchor: ($med / $a),
        method: ({ reducer: "median", count: $n }
                 + (if $ptt == "" then {} else { per_test_timeout: $ptt } end))
      }
    # Stats are whole-run counts, so they belong to the total and would be a lie on
    # a suite series — the driver reports Total/Passed across both chapters, not per
    # suite. A per-suite rate needs a per-suite denominator the stats file does not
    # carry; each suite entry carries only its own numerator, as `iterations`.
    + (if $withstats then { stats: $mid.stats } else {} end)
    + { samples: ($obs | map({
          iterations: .passed,
          ns_per_op: .mean,
          ratio_to_anchor: (.mean / $a),
          captured_at: .captured_at
        })) }
    + (if $obs[0].set_hash == "" then {} else { set_hash: $obs[0].set_hash } end)
    + (if $stamp == 1 then { anchor_ns_per_op: $a } else {} end)
    | if $stamp == 1 then .samples |= map(. + { anchor_ns_per_op: $a }) else . end;

  . as $reps
  | ($reps[0].series | keys_unsorted)   as $names
  | ($reps[0].per_test_timeout)         as $ptt
  | reduce $names[] as $name ({};
      . + { ($name): build(
              [$reps[] | {
                 mean: (.series[$name].ns / .series[$name].passed),
                 passed: .series[$name].passed,
                 set_hash: .series[$name].set_hash,
                 captured_at: .captured_at,
                 stats: .stats
               }];
              $ptt;
              $name == "test262.total") })
' "$reps")"

# Written to the file when one is named, stdout otherwise. Both callers pass a path
# and redirect stdout away, so emitting to both was writing to /dev/null every time.
if [ -n "$entries_out" ]; then
  printf '%s\n' "$entry" > "$entries_out"
else
  printf '%s\n' "$entry"
fi

jq -r 'to_entries[] |
       "\(.key): median \(.value.ns_per_op) ns/test over \(.value.samples[0].iterations) passing " +
       "(set \(.value.set_hash // "none"), \(.value.samples | length) reps " +
       "\(.value.samples | map(.ns_per_op) | min) .. \(.value.samples | map(.ns_per_op) | max)), " +
       "ratio \(.value.ratio_to_anchor)"' <<<"$entry" >&2
jq -r '.["test262.total"] | select(.stats) |
       "conformance: \(.stats.passed)/\(.stats.total) passing " +
       "(\((.stats.passed / .stats.total * 10000 | round) / 100)%), " +
       "\(.stats.failed) failed, \(.stats.timeouts) timed out at \(.method.per_test_timeout // "?")"' \
  <<<"$entry" >&2
