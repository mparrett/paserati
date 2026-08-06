#!/usr/bin/env bash
# Bisect the perf history on ONE box, measuring one new commit per level.
#
# Usage: scripts/perf-bisect.sh [options] <oldest-sha> <newest-sha>
#
#   -o, --out DIR       bisect directory (accumulates level-N/ sessions)
#       --commits FILE  ordered sha list, oldest first; default is the distinct
#                       commits between the two endpoints
#       --corpus FILE   passed to perf-session.sh (you want this)
#       --pins FILE     passed to perf-session.sh
#       --rounds N      rounds per level (default 3)
#       --threshold P   percent a benchmark must move to be worth subdividing
#                       (default 5, chosen because A9 puts a CI timeline point
#                       at ~2.26%, so two points need ~6.4% before a difference
#                       is visible there — under 5% here is not worth chasing)
#       --levels N      how many subdivisions to run this invocation (default 1)
#       --probe         run only the three-point probe, then stop
#
# WHY BISECT INSTEAD OF MEASURING EVERYTHING
#
# The whole-world run measured 21 commits x 3 rounds in ~7h. Most of those cells
# bought nothing: the history is mostly flat with a few large steps, and a cell
# spent inside a flat stretch tells you what you already knew. Bisecting spends
# cells only where a change is known to be, which gets the same answers in
# roughly log2(N) cells.
#
# WHY IT KEEPS ONE BOX ALIVE
#
# The naive version re-measures both bracket ends every level so the comparison
# is within one session, and costs three cells per level — barely better than
# measuring everything. Keeping the box up means a level costs ONE cell, because
# earlier levels are still comparable: same machine, same boot, and
# ratio_to_anchor, which perf-micro-noise-and-anchor-results.md shows absorbing
# 100% of a 22.4% hardware split inside one CPU model.
#
# That is the assumption this script rests on, so it is checked rather than
# trusted. perf-bisect-select.py refuses to run if levels span machine keys, and
# every level's anchor drift is carried into the report.
#
# WHAT IT CANNOT DO
#
# Find a culprit that is not there. Bisection assumes a step; against gradual
# accumulated change it subdivides forever and finds nothing, because there is
# nothing to find. The three-point probe classifies step vs ramp before any of
# the subdivision budget is spent — read that table before continuing.
set -euo pipefail

die() { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*" >&2; }

OUT=""; COMMITS=""; CORPUS=""; PINS=""; ROUNDS=3; THRESHOLD=5; LEVELS=1; PROBE=0
declare -a ENDPOINTS=()
while [ $# -gt 0 ]; do
  case "$1" in
    -o|--out)      OUT="${2:?}"; shift 2;;
    --commits)     COMMITS="${2:?}"; shift 2;;
    --corpus)      CORPUS="${2:?}"; shift 2;;
    --pins)        PINS="${2:?}"; shift 2;;
    --rounds)      ROUNDS="${2:?}"; shift 2;;
    --threshold)   THRESHOLD="${2:?}"; shift 2;;
    --levels)      LEVELS="${2:?}"; shift 2;;
    --probe)       PROBE=1; shift;;
    -h|--help)     sed -n '2,20p' "$0"; exit 0;;
    -*)            die "unknown option: $1";;
    *)             ENDPOINTS+=("$1"); shift;;
  esac
done

[ -n "$OUT" ] || die "give --out"
: "${ENDPOINTS:?give two commits: oldest and newest}"
[ "${#ENDPOINTS[@]}" -eq 2 ] || die "give exactly two endpoints (oldest, newest)"

repo="$(git rev-parse --show-toplevel)" || die "not in a git repo"
cd "$repo"
HERE="$repo/scripts"
mkdir -p "$OUT"

LO="$(git rev-parse --verify -q "${ENDPOINTS[0]}^{commit}")" || die "not a commit: ${ENDPOINTS[0]}"
HI="$(git rev-parse --verify -q "${ENDPOINTS[1]}^{commit}")" || die "not a commit: ${ENDPOINTS[1]}"

# The commit list is the bisect's coordinate system: positions are indices into
# it, so it must be stable for the life of the bisect. Written once, then reused.
LIST="$OUT/commits.txt"
if [ ! -f "$LIST" ]; then
  if [ -n "$COMMITS" ]; then
    cp "$COMMITS" "$LIST"
  else
    { echo "$LO"; git rev-list --reverse "${LO}..${HI}"; } > "$LIST"
  fi
  note "commit list pinned: $(wc -l < "$LIST" | tr -d ' ') commits"
fi
N="$(wc -l < "$LIST" | tr -d ' ')"
[ "$N" -ge 3 ] || die "need at least three commits between the endpoints"

session() { # level-dir sha...
  local dir="$1"; shift
  [ -f "$dir/session.json" ] && { note "$(basename "$dir") already complete, skipping"; return 0; }
  note "$(basename "$dir"): measuring $# commit(s)"
  "$HERE/perf-session.sh" -o "$dir" --rounds "$ROUNDS" \
    ${CORPUS:+--corpus "$CORPUS"} ${PINS:+--pins "$PINS"} "$@"
}

# --- level 0: the three-point probe --------------------------------------
# All three in ONE session. They are the frame every later level is compared
# against, so they get the strongest form of comparability available.
if [ ! -f "$OUT/level-0/session.json" ]; then
  mid="$(sed -n "$(( (N + 1) / 2 ))p" "$LIST")"
  session "$OUT/level-0" "$(sed -n 1p "$LIST")" "$mid" "$(sed -n "${N}p" "$LIST")"
fi

report() {
  python3 "$HERE/perf-bisect-select.py" --out "$OUT" --commits "$LIST" \
    --threshold "$THRESHOLD" --mode report | tee "$OUT/report.md"
}

if [ "$PROBE" -eq 1 ]; then
  report
  note "probe only; re-run without --probe to subdivide"
  exit 0
fi

# --- subdivide ------------------------------------------------------------
for _ in $(seq 1 "$LEVELS"); do
  nxt="$(python3 "$HERE/perf-bisect-select.py" --out "$OUT" --commits "$LIST" \
           --threshold "$THRESHOLD" --mode next)"
  if [ -z "$nxt" ]; then
    note "nothing outstanding above ${THRESHOLD}% — bisect converged"
    break
  fi
  lvl=1; while [ -d "$OUT/level-$lvl" ]; do lvl=$(( lvl + 1 )); done
  session "$OUT/level-$lvl" "$nxt"
done

report
note "output: $OUT"
