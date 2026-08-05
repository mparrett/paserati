#!/usr/bin/env bash
# Group commits by the benchmark BINARY they produce, so a measurement session
# runs once per distinct engine instead of once per commit.
#
# Usage: scripts/bench-dedup.sh [options] <sha>...
#
#   -c, --corpus FILE   corpus config (default: docs/perf/bench-corpus.json)
#   -o, --out FILE      write the grouping as JSON
#   -w, --work DIR      scratch worktree (default: a temp dir)
#   -p, --packages "A B"  packages to build (default: ./tests ./pkg/vm)
#
# WHY THIS EXISTS
#
# The engine key on the timeline hashes pkg/ SOURCE, so commits that compile to
# byte-identical binaries get distinct keys and are drawn as separate engine
# runs when they cannot possibly differ (C3). 47% of commits change no runtime
# code at all, which the timeline trigger allow-list already knows.
#
# Measuring those separately buys nothing and costs the most expensive resource
# in this whole apparatus: time on a dedicated box. Hashing the built artifact
# instead of the source says exactly which commits are the same experiment.
#
# THE CORPUS MUST BE PINNED FOR THIS TO MEAN ANYTHING
#
# Without it the benchmark files come from each commit's own tree, so a commit
# that only edited bench_test.go produces a different binary and reads as a
# distinct engine — which is the confound, not the signal. Overlaying one pinned
# corpus first makes the remaining binary difference attributable to engine
# source alone. That is why --corpus defaults to on rather than off.
set -euo pipefail

die() { printf 'bench-dedup: %s\n' "$*" >&2; exit 1; }
note() { printf 'bench-dedup: %s\n' "$*" >&2; }

CORPUS="docs/perf/bench-corpus.json"
OUT=""
WORK=""
PACKAGES="./tests ./pkg/vm"
SHAS=()

while [ $# -gt 0 ]; do
  case "$1" in
    -c|--corpus)   CORPUS="${2:?}"; shift 2;;
    -o|--out)      OUT="${2:?}"; shift 2;;
    -w|--work)     WORK="${2:?}"; shift 2;;
    -p|--packages) PACKAGES="${2:?}"; shift 2;;
    -h|--help)     sed -n '2,32p' "$0"; exit 0;;
    -*)            die "unknown option: $1";;
    *)             SHAS+=("$1"); shift;;
  esac
done

[ "${#SHAS[@]}" -gt 0 ] || die "no commits given"
[ -f "$CORPUS" ] || die "no corpus config at $CORPUS"

ROOT="$(git rev-parse --show-toplevel)"
CORPUS_ABS="$(cd "$(dirname "$CORPUS")" && pwd)/$(basename "$CORPUS")"

[ -n "$WORK" ] || WORK="$(mktemp -d)/wt"
cleanup() {
  git worktree remove --force "$WORK" 2>/dev/null || true
  rm -rf "$WORK"
  git worktree prune
}
trap cleanup EXIT
cleanup
git worktree add --detach "$WORK" "${SHAS[0]}" >/dev/null 2>&1 || die "cannot create worktree"

note "${#SHAS[@]} commit(s), packages: $PACKAGES"

rows=""
for sha in "${SHAS[@]}"; do
  short="$(git rev-parse --short "$sha")"
  git -C "$WORK" checkout --force --quiet "$sha" || die "checkout $sha failed"
  "$ROOT/scripts/bench-corpus.sh" --config "$CORPUS_ABS" --into "$WORK" >/dev/null 2>&1 \
    || { rows="$rows$short\tBUILD_FAIL\tcorpus overlay failed\n"; note "$short  corpus overlay failed"; continue; }

  # Hash the built test binaries, not the source. -buildvcs=false because Go
  # otherwise stamps the commit into the binary and nothing is ever identical.
  h=""
  failed=0
  for p in $PACKAGES; do
    bin="$WORK/.dedup-$(basename "$p").test"
    if ( cd "$WORK" && go test -c -buildvcs=false -o "$bin" "$p" ) >/dev/null 2>&1; then
      h="$h$(shasum -a 256 "$bin" | cut -c1-16)"
      rm -f "$bin"
    else
      failed=1
      break
    fi
  done

  if [ "$failed" -eq 1 ]; then
    # Genuinely unmeasurable at this commit with this corpus. A result, not an
    # error — see bench-corpus.sh.
    rows="$rows$short\tBUILD_FAIL\t-\n"
    note "$short  does not build with the pinned corpus"
    continue
  fi

  key="$(printf '%s' "$h" | shasum -a 256 | cut -c1-12)"
  rows="$rows$short\t$key\t-\n"
  note "$short  -> $key"
done

printf '%b' "$rows" | sort -k2,2 > /tmp/.bench-dedup-rows.$$
distinct=$(awk -F'\t' '$2 != "BUILD_FAIL" {print $2}' /tmp/.bench-dedup-rows.$$ | sort -u | wc -l | tr -d ' ')
total=$(wc -l < /tmp/.bench-dedup-rows.$$ | tr -d ' ')
failures=$(awk -F'\t' '$2 == "BUILD_FAIL"' /tmp/.bench-dedup-rows.$$ | wc -l | tr -d ' ')

printf '\n%-12s %-14s %s\n' commit engine note
awk -F'\t' '{printf "%-12s %-14s %s\n", $1, $2, $3}' /tmp/.bench-dedup-rows.$$

printf '\n%s of %s commits are distinct engines (%s unmeasurable)\n' "$distinct" "$total" "$failures"

if [ -n "$OUT" ]; then
  python3 - "$OUT" /tmp/.bench-dedup-rows.$$ <<'PY'
import json, sys, collections
out, rows = sys.argv[1], sys.argv[2]
groups = collections.OrderedDict()
fails = []
for line in open(rows):
    sha, key, _ = line.rstrip("\n").split("\t")
    if key == "BUILD_FAIL":
        fails.append(sha)
    else:
        groups.setdefault(key, []).append(sha)
json.dump({
    "engines": [{"key": k, "commits": v, "measure": v[0]} for k, v in groups.items()],
    "unmeasurable": fails,
}, open(out, "w"), indent=2)
PY
  note "wrote $OUT"
fi
rm -f /tmp/.bench-dedup-rows.$$
