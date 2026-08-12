#!/usr/bin/env bash
# Measure N commits on ONE machine in one session, and emit perf-data v2
# snapshots plus a comparison table.
#
# Usage: scripts/perf-session.sh [options] <sha>...
#
#   -o, --out DIR       session output directory
#       --tool-ref REF  tree the summarizing tools come from (see below)
#       --corpus FILE   pin the benchmark corpus (docs/perf/bench-corpus.json);
#                       without it each commit is measured with whatever
#                       benchmarks it shipped with
#       --pins FILE     b.N pin table from bench-calibrate.sh -o
#       --benchtime D   global -benchtime for anything unpinned
#       --count N       go test -count per measurement
#       --reducer NAME  min (default) or mean
#       --macro         also run the test262 macro benchmark
#       --limit N       stop after N measurements this run, then skip aggregation;
#                       re-run the same command to continue
#
# WHY THIS EXISTS, AND WHY IT IS NOT THE CI WORKFLOWS
#
# The forward timeline measures one commit per run on a runner drawn at random
# from a pool of four-plus CPU models. Characterized over 55 jobs
# (project-docs/docs/paserati/test262-estimator-characterization.md), a single
# such point carries ~2.26%: a ~1.2% bulk plus a ~6% tail of jobs that land on
# a slow host. Two points therefore need to differ by ~6.4% before the
# difference is visible, which is why the timeline's entire observed scatter is
# accounted for by measurement and shows no engine change at all.
#
# That noise is BETWEEN HOSTS. Measure every commit on one machine, back to
# back, and the term is common to all of them and cancels instead of being
# averaged down. Resolution goes to roughly the within-host rep noise. This is
# the same principle scripts/macro-test262-compare.sh already applies to a pair
# ("both halves ran on one runner, so there's nothing to normalize"),
# generalized to N commits.
#
# ROUNDS, NOT BLOCKS
#
# Commits are measured interleaved — A,B,C,A,B,C — never A,A,A,B,B,B. A session
# drifts: the machine warms, a background process wakes, the cloud neighbour
# gets busy. Under blocks that drift lands entirely on whichever commit was
# measured last and reads as a regression. Under rounds it spreads across all
# of them and shows up in the round-to-round spread, where it is visible.
#
# The order within a round is counterbalanced too — rotated, and reversed on
# even rounds. Interleaving alone still gives the first commit every cold start
# and the last every warm one, and a bias that repeats identically in every
# round is one the rounds cannot cancel.
#
# Each round is reduced independently and the rounds are combined by MEDIAN of
# per-round values — the same rule the multi-job design uses, because the
# failure it defends against (one bad round) is the same.
#
# INTEGRATING WITH perf-data
#
# Output is v2 machine-keyed snapshots, the format the corpus already uses. The
# machine key for this host becomes its own series, which is correct rather
# than a limitation: normalization across CPU models leaves a ~10.6% residual
# (project-docs/docs/paserati/perf-micro-noise-and-anchor-results.md), so tiers
# must never be compared. What DOES carry across sessions is ratio_to_anchor —
# the same document shows the anchor absorbing 100% of a 22.4% hardware split
# inside one CPU model, which is exactly the session-to-session case.
#
# Publishing is deliberately NOT done here. See scripts/perf-session-publish.sh.
set -euo pipefail

ROUNDS=3
OUT=""
MACRO=0
TOOL_REF="HEAD"
BENCHTIME=1s
COUNT=3
# Pin the protocol rather than inheriting it. bench-ratchet's defaults are ours
# to change — #22 makes `mean` the default upstream with `min` opt-in — and a
# session is the instrument that decides reducer-sensitive verdicts, so it is
# the last place that should take whatever the tool happened to default to.
# Same reasoning as the CI lanes.
REDUCER=min
MIN_ITERS=20
ITER_TOL=0.02
# Empty means no -pins, which is the unpinned global -benchtime. Passing a table
# here is what makes the b.N calibration reachable from the driver at all: the
# -pins flag landed in bench-ratchet (77d4c38d) without ever being wired in.
PINS=
# Empty means each commit is measured with the benchmarks it happens to ship
# with, which confounds benchmark drift with engine change and makes a commit
# older than a benchmark unmeasurable by it. Passing a corpus config overlays one
# pinned set of _test.go files and fixtures onto every commit, so the engine
# varies and the instrument does not. See scripts/bench-corpus.sh.
CORPUS=
# 0 means measure everything. A positive value stops after that many measurements
# in THIS invocation and skips the aggregation, so a long session can be staged:
# measure a few, look at the numbers, re-run to continue. Resume is keyed per
# (round, commit) file, so re-running picks up exactly where this left off.
#
# Staging this way rather than by shortening the commit list matters, because
# round_order derives each commit's position from the LIST LENGTH. Measure four
# commits with a four-commit list and they occupy different positions in the
# drift sequence than they would in the real run, and counterbalancing is the
# thing that stops session drift landing on whoever went last. Keep the list
# whole; limit the work instead.
LIMIT=0
MEASURED=0
SHAS=()

die() { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    -r|--rounds)    ROUNDS="${2:?}"; shift 2;;
    -o|--out)       OUT="${2:?}"; shift 2;;
    --macro)        MACRO=1; shift;;
    --tool-ref)     TOOL_REF="${2:?}"; shift 2;;
    --benchtime)    BENCHTIME="${2:?}"; shift 2;;
    --count)        COUNT="${2:?}"; shift 2;;
    --pins)         PINS="${2:?}"; shift 2;;
    --corpus)       CORPUS="${2:?}"; shift 2;;
    --limit)        LIMIT="${2:?}"; shift 2;;
    --reducer)      REDUCER="${2:?}"; shift 2;;
    -h|--help)      sed -n '2,44p' "$0"; exit 0;;
    -*)             die "unknown option: $1";;
    *)              SHAS+=("$1"); shift;;
  esac
done

[ "${#SHAS[@]}" -ge 1 ] || die "give at least one commit; two or more is the point"
[ "$ROUNDS" -ge 1 ] || die "--rounds must be >= 1"
command -v go >/dev/null || die "go not on PATH"
command -v jq >/dev/null || die "jq not on PATH"

repo="$(git rev-parse --show-toplevel)" || die "not in a git repo"
cd "$repo"

# Resolve every commit up front. Finding out on round 2 that the fourth sha is
# a typo wastes the whole session.
declare -a FULL=() SHORT=() STAMP=()
for s in "${SHAS[@]}"; do
  f="$(git rev-parse --verify -q "${s}^{commit}")" || die "not a commit: $s"
  FULL+=("$f")
  SHORT+=("$(git rev-parse --short=12 "$f")")
  STAMP+=("$(date -u -r "$(git show -s --format=%ct "$f")" +%Y%m%dT%H%M%SZ 2>/dev/null \
            || date -u -d "@$(git show -s --format=%ct "$f")" +%Y%m%dT%H%M%SZ)")
done

OUT="${OUT:-$repo/perf-session-$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$OUT/raw" "$OUT/snapshots"
OUT="$(cd "$OUT" && pwd)"

# --- the host -------------------------------------------------------------
# Burstable instances throttle on a credit balance, which means the throttling
# IS variance and a session can silently change speed partway through. That is
# the one hardware class this script cannot give a useful answer on, so say so
# rather than produce numbers.
cpu_model="$(awk -F': ' '/^model name/ {print $2; exit}' /proc/cpuinfo 2>/dev/null \
            || sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
itype="$(curl -s -m 1 -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' \
          -X PUT http://169.254.169.254/latest/api/token 2>/dev/null \
        | { read -r t 2>/dev/null || true; [ -n "${t:-}" ] && \
            curl -s -m 1 -H "X-aws-ec2-metadata-token: $t" \
              http://169.254.169.254/latest/meta-data/instance-type 2>/dev/null; } || true)"
case "${itype:-}" in
  t2.*|t3.*|t3a.*|t4g.*)
    echo "::warning::${itype} is BURSTABLE — CPU credits throttle under sustained load," >&2
    echo "  so the machine can change speed mid-session and the comparison is unsound." >&2
    echo "  Use a fixed-performance type (c7a/c7i/m7a) instead. Continuing anyway." >&2;;
esac
note "host: ${cpu_model}${itype:+ (${itype})}"
note "${#FULL[@]} commit(s) x ${ROUNDS} round(s), interleaved; macro=$([ $MACRO -eq 1 ] && echo on || echo off)"

# --- worktrees ------------------------------------------------------------
# The tool/engine split, which is load-bearing rather than stylistic: every
# step that SUMMARIZES a measurement — bench-ratchet's reducer and anchor
# normalization, perf-migrate, the macro reducer — comes from a fixed tool
# tree, so all N commits are post-processed identically. Getting this backwards
# is how a backfill reduces differently from a forward run.
#
# The engine and its benchmark functions still come from the target tree, and
# that needs no special handling: bench-ratchet shells out to `go test <import
# path>` with no explicit directory, so its cwd decides which module — and
# therefore which _test.go files — it measures. Running the tool tree's binary
# with the target worktree as cwd gets both halves at once. What it does NOT
# pick up from the target is bench-ratchet's own package scope, which has
# changed once since the tool was written; a commit that adds a benchmark
# package needs -packages here.
#
# Both live outside the working tree so the session never disturbs it.
TOOLWT="$OUT/.tool"
TARGWT="$OUT/.target"
cleanup_wt() {
  for w in "$TOOLWT" "$TARGWT"; do
    [ -n "${w:-}" ] || continue
    # Worktree REGISTRATION outlives the directory: rm -rf alone leaves it
    # registered and the next `worktree add` at that path dies with "missing
    # but already registered". Always remove, then rm, then prune.
    git worktree remove --force "$w" 2>/dev/null || true
    rm -rf "$w"
  done
  git worktree prune
}
trap cleanup_wt EXIT
cleanup_wt
git worktree add --detach "$TOOLWT" "$TOOL_REF" >/dev/null || die "cannot create tool worktree at $TOOL_REF"
git worktree add --detach "$TARGWT" "${FULL[0]}" >/dev/null || die "cannot create target worktree"
note "tooling from $(git -C "$TOOLWT" rev-parse --short HEAD) ($TOOL_REF)"

reduce_sh="$TOOLWT/scripts/macro-test262-reduce.sh"
[ $MACRO -eq 1 ] && { [ -x "$reduce_sh" ] || chmod +x "$reduce_sh" 2>/dev/null || die "no macro reducer at $reduce_sh"; }

# Built once, from the tool tree, and invoked below with the target worktree as
# cwd. Building beats `go run` per commit for a second reason: `go run` would
# recompile the tool inside the measurement loop, on the same machine whose
# spare capacity the next benchmark is about to depend on.
BENCH_RATCHET="$OUT/.bin/bench-ratchet"
mkdir -p "$OUT/.bin"
( cd "$TOOLWT" && go build -o "$BENCH_RATCHET" ./cmd/bench-ratchet ) \
  || die "cannot build bench-ratchet from tool tree"

# --- measure --------------------------------------------------------------
# Counterbalance the order WITHIN a round, not just across commits. Rounds
# already spread session-scale drift, but if every round visits the commits in
# the same order then position inside a round is perfectly confounded with
# commit: whatever runs first is always cold, whatever runs last always carries
# the round's accumulated warming. Rounds cannot cancel a bias that is identical
# in each of them, and the median across rounds cannot either.
#
# So: reverse direction on even rounds — the N-commit generalization of the
# ABBA alternation in ab_repeat.py:126 — and rotate the starting commit once per
# PAIR of rounds. The offset has to be shared by a round and its mirror; advance
# it every round instead and the rotation cancels the reversal exactly, which
# for N=2 reproduces the fixed order this is meant to fix.
#
# Over an even number of rounds every commit's mean position is then exactly
# (N-1)/2. The rotation is what the odd round left over at ROUNDS=3 rides on.
round_order() {
  local r="$1" n="${#FULL[@]}" off=$(( (r - 1) / 2 )) k
  for ((k = 0; k < n; k++)); do
    if (( r % 2 == 1 )); then
      echo $(( (k + off) % n ))
    else
      echo $(( (n - 1 - k + off) % n ))
    fi
  done
}

for r in $(seq 1 "$ROUNDS"); do
  mkdir -p "$OUT/raw/round-$r"
  for i in $(round_order "$r"); do
    sha="${FULL[$i]}"; short="${SHORT[$i]}"
    out="$OUT/raw/round-$r/${short}.json"
    [ -f "$out" ] && { note "round $r  ${short}  (already present, skipping)"; continue; }
    note "round $r  ${short}  building"
    git -C "$TARGWT" checkout --force --quiet "$sha" || die "checkout $sha failed"
    # Re-overlay after every checkout: --force discards the previous round's
    # overlay, so this has to run per commit rather than once at setup.
    if [ -n "$CORPUS" ]; then
      "$TOOLWT/scripts/bench-corpus.sh" --config "$CORPUS" --into "$TARGWT" \
        || die "corpus overlay failed at $short (round $r)"
    fi
    ( cd "$TARGWT" && "$BENCH_RATCHET" \
        -count "$COUNT" -benchtime "$BENCHTIME" -timeout 30m \
        -reducer "$REDUCER" \
        -min-iterations "$MIN_ITERS" -iteration-tolerance "$ITER_TOL" \
        ${PINS:+-pins "$PINS"} \
        -baseline "$out" snapshot >/dev/null ) || die "bench-ratchet failed at $short (round $r)"

    if [ $MACRO -eq 1 ]; then
      anchor_ns="$(jq -r '.anchor.ns_per_op' "$out")"
      ( cd "$TARGWT" && ./setup-test262.sh >/dev/null 2>&1 || true )
      ents="$OUT/raw/round-$r/${short}.t262.json"
      # PER_TEST_TIMEOUT must be pinned. Left unset the driver takes its own
      # default, and the boundary moves with it: the reducer refuses to combine
      # reps that disagree about WHICH tests timed out, so an unpinned timeout
      # turns one borderline test into a failed session. Every CI workflow pins
      # 1.2s; match it, both for determinism and so a session's numbers sit on
      # the same footing as the corpus.
      # The reference set comes from the TOOL worktree, matching perf-timeline.yml.
      # A session measuring a different set from CI is the divergence pinning
      # exists to remove, and it would be invisible: both would report a
      # test262.total, just over different tests.
      ( cd "$TARGWT" && PER_TEST_TIMEOUT="${PER_TEST_TIMEOUT:-1.2s}" MACRO_COUNT="$COUNT" \
          TEST262_REFSET="$TOOLWT/docs/perf/test262-refset.txt" \
          "$reduce_sh" "$anchor_ns" "$ents" >/dev/null ) \
        || die "macro failed at $short (round $r)"
      jq --slurpfile e "$ents" '.benchmarks += $e[0]' "$out" > "$out.tmp" && mv "$out.tmp" "$out"
    fi
    note "round $r  ${short}  anchor $(jq -r '.anchor.ns_per_op' "$out") ns/op"

    MEASURED=$(( MEASURED + 1 ))
    if [ "$LIMIT" -gt 0 ] && [ "$MEASURED" -ge "$LIMIT" ]; then
      note "--limit $LIMIT reached; stopping. Re-run the same command without --limit to continue."
      break 2
    fi
  done
done

# --- completeness ---------------------------------------------------------
# Everything below reads every round file directly, so a partial session must
# stop here rather than fail in jq. This is reached by --limit, by a killed run,
# and by a box that went away mid-session; all three are resumable by re-running
# the identical command, since the skip is per (round, commit) file.
expected=$(( ${#FULL[@]} * ROUNDS ))
present=0
for r in $(seq 1 "$ROUNDS"); do
  for s in "${SHORT[@]}"; do
    [ -f "$OUT/raw/round-$r/${s}.json" ] && present=$(( present + 1 ))
  done
done
if [ "$present" -lt "$expected" ]; then
  note "measured $present of $expected (round, commit) cells; skipping aggregation"
  note "re-run the same command to continue — completed cells are skipped"
  exit 0
fi

# --- drift check ----------------------------------------------------------
# The anchor is measured every round on the same machine, so its spread across
# rounds is a direct read on whether the session held still. A session that
# drifted is not necessarily wrong, but its cross-commit comparison is weaker
# than the round count suggests and the reader should know.
note "checking session stability"
anchor_spread="$(for r in $(seq 1 "$ROUNDS"); do
    jq -r '.anchor.ns_per_op' "$OUT/raw/round-$r/${SHORT[0]}.json"
  done | jq -s 'if length < 2 then 0 else ((max-min)/min*100) end')"
printf '    anchor drift across rounds: %.2f%%\n' "$anchor_spread" >&2
awk -v d="$anchor_spread" 'BEGIN{ if (d > 2) print "::warning::anchor drifted >2% across rounds — the machine did not hold still; treat small deltas with suspicion" }' >&2

# --- combine rounds -------------------------------------------------------
# Median of per-round values, the same rule the multi-job design uses: one bad
# round should be rejected, not averaged in. Every round's samples are kept so
# the spread stays inspectable.
note "combining ${ROUNDS} round(s) per commit"
for i in "${!FULL[@]}"; do
  short="${SHORT[$i]}"
  files=(); for r in $(seq 1 "$ROUNDS"); do files+=("$OUT/raw/round-$r/${short}.json"); done
  # perf-fixratio holds every object to  ratio_to_anchor == ns_per_op /
  # anchor_ns_per_op, against its OWN anchor if it carries one. So the merge
  # cannot median the ratios and the anchors independently — median(ratio) is
  # not median(ns)/median(anchor), and the verifier rejects it. (It did: 210
  # violations on the first attempt, which is the gate doing its job.)
  #
  # Instead: the entry recomputes its ratio from the merged ns and the merged
  # anchor, and every sample is stamped with the anchor of the ROUND it came
  # from, so each is held to the divisor it was actually measured against —
  # the same mechanism MACRO_RECORD_ANCHOR uses for off-run backfills.
  jq -s '
    def med: sort | if length==0 then null
                    elif length%2==1 then .[(length/2|floor)]
                    else (.[length/2-1]+.[length/2])/2 end;
    . as $all | $all[0]
    | ([$all[].anchor.ns_per_op] | med) as $A
    | .anchor.ns_per_op = $A
    | .benchmarks = ( reduce ([$all[].benchmarks | keys] | add | unique)[] as $k ({};
        . + { ($k): (
          ([$all[].benchmarks[$k].ns_per_op // empty] | med) as $ns |
          { ns_per_op:       $ns,
            ratio_to_anchor: (if ($ns != null) and ($A != null) and ($A > 0) then ($ns / $A) else null end),
            allocs_per_op:   ([$all[].benchmarks[$k].allocs_per_op // empty] | med),
            bytes_per_op:    ([$all[].benchmarks[$k].bytes_per_op  // empty] | med),
            samples:         ([ $all[] | .anchor.ns_per_op as $a
                                | (.benchmarks[$k].samples // [])[]
                                | . + { anchor_ns_per_op: $a } ]),
            set_hash:        ([$all[].benchmarks[$k].set_hash // empty] | first),
            # Rounds that actually contributed THIS entry, not rounds in the
            # session. They differ whenever a series is present in only some
            # rounds — merging a micro-only session with a --macro one leaves
            # test262 in a subset — and a provenance field that overstates its
            # own sample count is worse than none.
            method: { reducer: "median-of-round-medians",
                      rounds: ([$all[] | select(.benchmarks[$k] != null)] | length) } } ) })
      )' "${files[@]}" > "$OUT/snapshots/${STAMP[$i]}-${short}.json"

  # A macro measured across rounds must agree on WHICH tests it timed, exactly
  # as reps within a round must. Disagreement means the rounds summarized
  # different sets and the median mixes speed with membership.
  if [ $MACRO -eq 1 ]; then
    nh="$(jq -s '[.[].benchmarks["test262.total"].set_hash // empty] | unique | length' "${files[@]}")"
    [ "${nh:-0}" -le 1 ] || die "${short}: rounds disagree on test262 set_hash (${nh} distinct) — not combinable"
  fi
done

# --- v2 -------------------------------------------------------------------
note "converting to machine-keyed v2"
( cd "$TOOLWT" && go run ./cmd/perf-migrate -dir "$OUT/snapshots" ) >/dev/null \
  || die "perf-migrate failed"
mkey="$( cd "$TOOLWT" && go run ./cmd/perf-migrate -print-key )"
note "machine key: ${mkey}"

# --- session metadata -----------------------------------------------------
jq -n --arg key "$mkey" --arg cpu "$cpu_model" --arg itype "${itype:-}" \
      --arg tool "$(git -C "$TOOLWT" rev-parse HEAD)" --arg drift "$anchor_spread" \
      --argjson rounds "$ROUNDS" --argjson macro "$MACRO" \
      --args '{
        session: { machine_key:$key, cpu_model:$cpu, instance_type:$itype,
                   tool_ref:$tool, rounds:$rounds, macro:($macro==1),
                   anchor_drift_pct:($drift|tonumber),
                   commits:$ARGS.positional }
      }' "${SHORT[@]}" > "$OUT/session.json"

# --- report ---------------------------------------------------------------
# Ratios, not raw ns: within a session raw would be fine, but a reader will
# inevitably compare this table against another session, and ratio is the only
# quantity that survives that.
#
# Per-benchmark deltas reduced by MEDIAN and geomean, never by an arithmetic
# mean of ratios. BenchmarkFactorial runs at ~5e8x the anchor, so a
# plain mean is that one benchmark plus rounding — it reported 9.27% on a pair
# of commits differing only in a workflow file. Median and geomean disagreeing
# widely is itself the signal that the session was noisy.
{
  echo "# Perf session — ${#FULL[@]} commits x ${ROUNDS} rounds"
  echo
  echo "Host \`${cpu_model}\`${itype:+ (\`${itype}\`)} · key \`${mkey}\` · anchor drift ${anchor_spread}%"
  echo
  echo "All commits measured on ONE machine, interleaved by round, so the"
  echo "between-host term that dominates the CI timeline cancels here."
  echo "Deltas are per-benchmark vs the first commit listed, then reduced."
  echo
  first="$(ls "$OUT/snapshots"/*"${SHORT[0]}"*.json 2>/dev/null | head -1)"
  echo "| commit | micro median Δ | micro geomean Δ | spread (best..worst) | test262 Δ | n |"
  echo "|---|---|---|---|---|---|"
  for i in "${!FULL[@]}"; do
    f="$(ls "$OUT/snapshots"/*"${SHORT[$i]}"*.json 2>/dev/null | head -1)"
    [ -n "$f" ] || continue
    jq -rn --arg k "$mkey" --arg sha "${SHORT[$i]}" --slurpfile b "$first" --slurpfile h "$f" '
      def med: sort | if length==0 then null elif length%2==1 then .[(length/2|floor)]
                      else (.[length/2-1]+.[length/2])/2 end;
      def geomean: if length==0 then null else ((map(log)|add/length)|exp) end;
      def pct($x): if $x == null then "-" else ((($x-1)*10000|round)/100|tostring)+"%" end;
      def micro($M): [$M | keys[] | select(startswith("test262")|not)];
      ($b[0].machines[$k].benchmarks) as $B | ($h[0].machines[$k].benchmarks) as $H |
      [ micro($B)[]
        | select(((($B[.].ratio_to_anchor // 0)) > 0) and ((($H[.].ratio_to_anchor // 0)) > 0))
        | ($H[.].ratio_to_anchor / $B[.].ratio_to_anchor) ] as $d |
      ($B["test262.total"].ratio_to_anchor) as $tb |
      ($H["test262.total"].ratio_to_anchor) as $th |
      "| \($sha) | \(pct($d|med)) | \(pct($d|geomean)) | \(pct($d|min))..\(pct($d|max)) | " +
      "\(if $tb and $th then pct($th/$tb) else "-" end) | \($d|length) |"
    '
  done
  echo
  echo "**Reading it.** Median is the robust headline; geomean is the conventional"
  echo "suite aggregate. If the two disagree much, or the spread is wide, the session"
  echo "was noisy and small deltas mean nothing — check the anchor drift above and"
  echo "re-run with more rounds on a quieter machine."
  echo
  echo "Raw rounds in \`raw/\`, v2 snapshots in \`snapshots/\`, metadata in \`session.json\`."
  echo "Publish with \`scripts/perf-session-publish.sh $OUT\`."
} > "$OUT/report.md"

note "done"
echo
cat "$OUT/report.md"
echo
echo "output: $OUT" >&2
