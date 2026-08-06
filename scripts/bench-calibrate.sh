#!/usr/bin/env bash
# Find the b.N each benchmark should be PINNED at, by sweeping N and looking at
# the shape of the curve.
#
# Usage: scripts/bench-calibrate.sh [options] [BenchmarkName ...]
#
#   -p, --package PKG   go package (default: github.com/nooga/paserati/tests)
#   -n, --min-n N       smallest N to sweep (default 1)
#   -m, --max-n N       largest N to sweep (default 256)
#   -c, --count N       go test -count per point (default 3, min taken)
#   -t, --tolerance PCT consecutive-point agreement that counts as flat (default 2)
#   -f, --floor N       smallest acceptable pin, matching bench-ratchet (default 20)
#   -o, --out FILE      also write the curves as JSON
#
# The defaults suit ./tests, whose benchmarks run in milliseconds and land at
# b.N between 1 and a few hundred. They are wrong for pkg/vm, which runs at
# NANOSECONDS per op and lands at b.N in the tens of millions: sweeping 1..256
# there measures timer and loop overhead and nothing else. For that package:
#
#   scripts/bench-calibrate.sh -p github.com/nooga/paserati/pkg/vm \
#     -n 10000000 -m 320000000 -f 10000000
#
# NEVER pin BenchmarkRatchetAnchor. Every ratio in the corpus is
# bench_ns/anchor_ns, so moving the anchor's b.N shifts the entire timeline
# against itself. bench-ratchet rejects a pin table that names it.
#   -o, --out FILE      also write the curves as JSON
#
# WHY THIS EXISTS
#
# `go test -bench` chooses b.N to fill -benchtime, so N is an OUTPUT of the
# measurement (N ≈ benchtime / per-op cost). Two things follow, and the second
# is the one that motivated this script.
#
# First, a slow benchmark gets a tiny N, which costs AVERAGING: ns/op is the
# mean over N iterations, so per-iteration variance falls only as sqrt(N) and at
# N=1 a single slow iteration is the whole reading.
#
# It is NOT quantisation. This comment used to say ns/op was "quantised to
# 1-in-N", and that bench-ratchet warned about it. Both were wrong and both are
# withdrawn (5fb6f8ee): Go computes ns/op as elapsed nanoseconds divided by N,
# so the quantum is (1 ns)/N — an absolute quantity, not a fraction of the
# result. On Fib at N=1, ~780 ms/op, that is 1.3e-07% of the value. The check
# bench-ratchet actually runs now is whether b.N MOVES between commits, which is
# the real confound; see cmd/bench-ratchet/iterations.go.
#
# Second, ns/op CAN be a function of N where iterations share state — and where
# it is, two commits measured at different N were measured under different
# protocols. Which benchmarks those are is a fact to measure, not to assume.
#
# On the measurement host (c7a.2xlarge, 2026-08-03) the five ./tests workloads
# that compile above b.ResetTimer() are FLAT from N=1 to N=256: Add +6.2%,
# Arith +0.1%, Fib -0.4%, MatrixMult +0.9%, SetIndex +1.9%, each with per-curve
# scatter of the same size. An earlier sweep on a loaded MacBook reported
# -38%..+73% for the same four; those shapes were the laptop's load and do not
# reproduce. See project-docs .../perf-session-remeasure-results.md.
#
# The group that IS strongly N-dependent is PrototypeMethodAccess and
# PrototypeCacheHitRate, at a median -37.8% across the same sweep, because they
# construct an engine inside the timed loop and larger N amortises the first,
# coldest iteration (nooga#51).
#
# Either way, what a cross-commit comparison needs is not a steady state but a
# CONSTANT: pin N and the blend stops varying between commits, which is the
# confound. Pinning is cheap where the curve is flat, so pin regardless rather
# than reasoning per benchmark about whether it matters.
#
# This script finds where to pin. It reports the curve and suggests the smallest
# N at or above the floor whose neighbours agree within --tolerance, because a
# pin inside a flat region survives small changes in per-op cost — which is
# exactly what an engine optimisation is.
#
# RUN IT ON THE MEASUREMENT HOST. A laptop cannot resolve a 2% flatness test;
# the 2026-08-01 sweep could not find a flat region for Arith or SetIndex at
# all. Treat a laptop run as a shape sketch and nothing more.
set -uo pipefail

PKG="github.com/nooga/paserati/tests"
MINN=1
MAXN=256
COUNT=3
TOL=2
FLOOR=20      # matches defaultMinIterations in cmd/bench-ratchet/iterations.go
OUT=""
BENCHES=()

die() { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    -p|--package)   PKG="${2:?}"; shift 2;;
    -n|--min-n)     MINN="${2:?}"; shift 2;;
    -m|--max-n)     MAXN="${2:?}"; shift 2;;
    -c|--count)     COUNT="${2:?}"; shift 2;;
    -t|--tolerance) TOL="${2:?}"; shift 2;;
    -f|--floor)     FLOOR="${2:?}"; shift 2;;
    -o|--out)       OUT="${2:?}"; shift 2;;
    -h|--help)      awk 'NR>1 && /^#/ {sub(/^# ?/,""); print; next} NR>1 {exit}' "$0"; exit 0;;
    -*)             die "unknown option: $1";;
    *)              BENCHES+=("$1"); shift;;
  esac
done

command -v go >/dev/null || die "go not on PATH"
command -v jq >/dev/null || die "jq not on PATH"
cd "$(git rev-parse --show-toplevel)" || die "not in a git repo"

# No benchmarks named: discover them. -benchtime 1x is the cheapest possible
# listing run — one iteration each, purely to learn the names.
if [ "${#BENCHES[@]}" -eq 0 ]; then
  note "discovering benchmarks in $PKG"
  while IFS= read -r n; do BENCHES+=("$n"); done < <(
    go test "$PKG" -run '^$' -bench . -benchtime 1x 2>/dev/null \
      | awk '/^Benchmark/ {sub(/-[0-9]+$/, "", $1); print $1}' | sort -u)
  [ "${#BENCHES[@]}" -ge 1 ] || die "no benchmarks found in $PKG"
fi

# Powers of two to --max-n. Geometric rather than linear because the curves are
# read on a log axis: the interesting structure is between 1 and 32, and a
# linear sweep spends all its wall clock in the flat tail.
NS=()
n="$MINN"; while [ "$n" -le "$MAXN" ]; do NS+=("$n"); n=$((n * 2)); done
[ "${#NS[@]}" -ge 2 ] || die "--min-n $MINN and --max-n $MAXN leave fewer than 2 points to sweep"

note "${#BENCHES[@]} benchmark(s) x ${#NS[@]} N values, -count $COUNT"
echo
printf '%-46s %s\n' "benchmark" "min ns/op by b.N"

json='[]'
for b in "${BENCHES[@]}"; do
  vals=()
  for n in "${NS[@]}"; do
    v=$(go test "$PKG" -run '^$' -bench "^${b}$" -benchtime "${n}x" -count "$COUNT" 2>/dev/null \
        | awk -v b="$b" '$1 ~ "^" b "(-[0-9]+)?$" {print $3}' | sort -n | head -1)
    vals+=("${v:-}")
  done

  # Suggest the smallest N >= --floor whose value agrees with
  # its next neighbour within tolerance. Agreement with the NEXT point, not the
  # previous: a pin wants to sit at the start of a plateau, not at the end of a
  # slope that happens to have flattened for one step.
  suggest=""; evaluable=0
  for i in "${!NS[@]}"; do
    [ "${NS[$i]}" -ge "$FLOOR" ] || continue
    a="${vals[$i]}"; nxt=$((i + 1))
    [ -n "$a" ] && [ "$nxt" -lt "${#NS[@]}" ] || continue
    bb="${vals[$nxt]}"
    [ -n "$bb" ] || continue
    evaluable=$((evaluable + 1))
    if awk -v a="$a" -v b="$bb" -v t="$TOL" 'BEGIN{d=(a>b?a-b:b-a)/a*100; exit !(d<=t)}'; then
      suggest="${NS[$i]}"; break
    fi
  done

  line=""
  for i in "${!NS[@]}"; do line+="$(printf '%s=%s ' "${NS[$i]}" "${vals[$i]:-x}")"; done
  printf '%-46s %s\n' "$b" "$line"
  # "Could not look" and "looked and found nothing" are different results, and
  # reporting the first as the second is how a sweep that covered nothing reads
  # as a sweep that came back clean. A candidate needs a successor to compare
  # against, so the largest N swept is never one.
  if [ -n "$suggest" ]; then
    printf '%-46s → pin at -benchtime %sx\n' "" "$suggest"
  elif [ "$evaluable" -eq 0 ]; then
    printf '%-46s → NOT EVALUATED: no N >= %s has a successor in this sweep; raise --max-n above %s\n' \
      "" "$FLOOR" "$((FLOOR * 2))"
  else
    printf '%-46s → NO FLAT REGION within %d%% up to N=%s (%d candidate(s) checked); do not pin from this run\n' \
      "" "$TOL" "$MAXN" "$evaluable"
  fi

  # NEVER pin the anchor, and say so in the output rather than leaving it to the
  # reader. bench-ratchet refuses any pin table naming BenchmarkRatchetAnchor —
  # every ratio in the corpus is bench_ns/anchor_ns, so moving its b.N shifts the
  # whole timeline against itself — which meant this script emitted a file its
  # own consumer rejects, and every calibration needed hand-surgery before use.
  # A null suggested_n is the "calibration declined" signal loadPins already
  # honours, so declining here is expressed in the vocabulary that exists.
  case "$b" in
    *BenchmarkRatchetAnchor*)
      if [ -n "$suggest" ]; then
        printf '%-46s → declining to pin: the anchor must stay on the global -benchtime\n' ""
      fi
      suggest=""
      ;;
  esac

  json=$(jq -c --arg b "$b" --arg s "$suggest" \
            --argjson ns "$(printf '%s\n' "${NS[@]}" | jq -sc .)" \
            --argjson v "$(printf '%s\n' "${vals[@]:-}" | jq -sc '[.[] | if . == "" then null else tonumber end]')" \
            --argjson ev "$evaluable" \
            '. + [{benchmark:$b, n:$ns, ns_per_op:$v, candidates_checked:$ev, suggested_n:(if $s=="" then null else ($s|tonumber) end)}]' \
         <<<"$json")
done

if [ -n "$OUT" ]; then
  jq --arg pkg "$PKG" --arg host "$(uname -sm)" --argjson tol "$TOL" \
     '{package:$pkg, host:$host, tolerance_pct:$tol, curves:.}' <<<"$json" > "$OUT"
  note "curves → $OUT"
fi

echo
echo "Apply with:  bench-ratchet -pins <this file> ...   (needs --out)"
echo
echo "A suggestion is a starting point, not a verdict. Confirm the pin by"
echo "re-running the two commits you actually care about at that N and checking"
echo "they still separate the way the variable-N run said they did."
