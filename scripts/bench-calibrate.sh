#!/usr/bin/env bash
# Find the b.N each benchmark should be PINNED at, by sweeping N and looking at
# the shape of the curve.
#
# Usage: scripts/bench-calibrate.sh [options] [BenchmarkName ...]
#
#   -p, --package PKG   go package (default: github.com/nooga/paserati/tests)
#   -m, --max-n N       largest N to sweep (default 256)
#   -c, --count N       go test -count per point (default 3, min taken)
#   -t, --tolerance PCT consecutive-point agreement that counts as flat (default 2)
#   -f, --floor N       smallest acceptable pin, matching bench-ratchet (default 20)
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
# Second — and this is not fixed by raising -benchtime — ns/op is a FUNCTION of
# N whenever iterations share state. paserati's ./tests benchmarks build one
# interpreter outside the loop and call InterpretChunk b.N times on it, so
# iteration k inherits iteration 1's warm inline caches, built shapes and grown
# heap. Measured 2026-08-01 (project-docs/docs/paserati/evidence/
# 2026-08-01-bn-sweep/), the direction is NOT the same for every benchmark:
#
#   MatrixMult  -33% by N=16 then flat     warmup dominates
#   Arith       -38% by N=256              warmup dominates
#   Add         +73% by N=256              accumulation dominates
#   Fib         +17% by N=256, monotone    accumulation dominates
#
# So "raise -benchtime until N is comfortably above 20" walks each benchmark a
# different distance along its own curve, in its own direction, and changes what
# each one measures. What a cross-commit comparison actually needs is not a
# steady state but a CONSTANT: pin N and the amortisation blend stops varying
# between commits, which is the whole confound.
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
    -m|--max-n)     MAXN="${2:?}"; shift 2;;
    -c|--count)     COUNT="${2:?}"; shift 2;;
    -t|--tolerance) TOL="${2:?}"; shift 2;;
    -f|--floor)     FLOOR="${2:?}"; shift 2;;
    -o|--out)       OUT="${2:?}"; shift 2;;
    -h|--help)      sed -n '2,49p' "$0"; exit 0;;
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
n=1; while [ "$n" -le "$MAXN" ]; do NS+=("$n"); n=$((n * 2)); done

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
