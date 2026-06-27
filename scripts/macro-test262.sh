#!/usr/bin/env bash
# Run the Test262 macro-benchmark for the current checkout and emit the raw
# per-series execution-time records plus the correctness stats.
#
# Usage: scripts/macro-test262.sh <records-out.jsonl> <stats-out.json>
#
# Writes:
#   <records-out>.jsonl   StreamRecords: total + per top-level suite, ns_per_op =
#                         summed execution time over passing, non-timed-out tests
#   <stats-out>.json      Test262 pass/fail/timeout counts (the correctness signal)
#
# This is the same-runner A/B half: the caller runs it once on the merge-base and
# once on the head, then diffs the raw sums. No anchor normalization here — both
# halves run on one machine, so there's no cross-runner drift to cancel, and
# normalizing each half by its own separately-measured anchor only injects the
# anchor's measurement noise. (Anchor-normalization is for the cross-runner
# timeline, not this A/B.)
#
# Env:
#   PER_TEST_TIMEOUT  per-test safety bound (default 5s; timed-out tests are
#                     excluded from the timing sum and counted separately)
#   TEST262_SUBPATH   restrict to a subpath for local spot-checks (default: whole corpus)
set -euo pipefail

records_out="${1:?usage: macro-test262.sh <records.jsonl> <stats.json>}"
stats_out="${2:?usage: macro-test262.sh <records.jsonl> <stats.json>}"
raw="${records_out%.jsonl}.raw.json"

timeout="${PER_TEST_TIMEOUT:-5s}"
subpath="${TEST262_SUBPATH:-}"

# bench-test262 -out appends; clear any stale output so reruns don't accumulate.
rm -f "$records_out" "$raw"

go build -o ./paserati-test262 ./cmd/paserati-test262
go build -o ./bench-test262 ./cmd/bench-test262

# Full-suite run; -json carries per-test durations + pass/fail/timeout flags.
# The runner exits non-zero whenever any test fails (expected — paserati fails
# plenty of Test262), so ignore its exit code; bench-test262 validates the JSON.
if [ -n "$subpath" ]; then
  ./paserati-test262 -path ./test262 -timeout "$timeout" -subpath "$subpath" -json > "$raw" || true
else
  ./paserati-test262 -path ./test262 -timeout "$timeout" -json > "$raw" || true
fi

./bench-test262 -in "$raw" -out "$records_out"
jq '.stats' "$raw" > "$stats_out"

echo "macro-test262: wrote $records_out and $stats_out"
