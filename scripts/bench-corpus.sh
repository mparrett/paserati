#!/usr/bin/env bash
# Overlay a pinned benchmark corpus onto a worktree, so an engine commit can be
# measured with benchmarks it never shipped with.
#
# Usage:
#   scripts/bench-corpus.sh --into DIR [options]     apply the corpus
#   scripts/bench-corpus.sh --list DIR [options]     report what is measurable
#
#   -c, --config FILE   corpus config (default: docs/perf/bench-corpus.json)
#   -r, --ref REF       override the config's ref
#   -i, --into DIR      target worktree to overlay onto
#   -l, --list DIR      after overlay, list the benchmarks that actually build
#   -n, --dry-run       print what would be written, write nothing
#
# WHY THIS EXISTS
#
# perf-session.sh already splits tooling from target: everything that SUMMARIZES
# a measurement comes from a fixed tool tree, so all N commits are post-processed
# identically. The engine and its benchmarks both came from the target tree.
#
# That is fine for the engine and wrong for the benchmarks. If bench_test.go
# differs between two commits, their numbers were produced by different
# instruments and the delta between them is not attributable to the engine. And
# a commit older than a benchmark cannot be measured by it at all, which is what
# makes every benchmark fix an epoch break: fix it, and no earlier point is
# comparable to any later one.
#
# Pinning the corpus separates the two. The engine varies (that is the
# experiment); the instrument does not (that is the control).
#
# THE HARD GUARD
#
# Only _test.go files and tests/scripts fixtures may be overlaid. Overlaying
# engine source would mean measuring an engine other than the commit under test,
# which is the one failure this whole mechanism exists to prevent, so it is a
# refusal rather than a warning.
#
# N/A IS A RESULT
#
# A pinned benchmark may not build against an old engine — an API that did not
# exist yet, a language feature the fixture uses. That benchmark is genuinely
# unmeasurable at that commit and --list reports it as such. An honest N/A is
# the correct outcome; guessing or silently dropping it is not.
set -euo pipefail

die() { printf 'bench-corpus: %s\n' "$*" >&2; exit 1; }
note() { printf 'bench-corpus: %s\n' "$*" >&2; }

CONFIG="docs/perf/bench-corpus.json"
REF=""
INTO=""
LIST=""
DRY=0

while [ $# -gt 0 ]; do
  case "$1" in
    -c|--config)  CONFIG="${2:?}"; shift 2;;
    -r|--ref)     REF="${2:?}"; shift 2;;
    -i|--into)    INTO="${2:?}"; shift 2;;
    -l|--list)    LIST="${2:?}"; shift 2;;
    -n|--dry-run) DRY=1; shift;;
    -h|--help)    sed -n '2,40p' "$0"; exit 0;;
    *)            die "unknown argument: $1";;
  esac
done

[ -f "$CONFIG" ] || die "no corpus config at $CONFIG"

# Read the config. python3 rather than jq: jq is not guaranteed on the session
# box, python3 is already a dependency of the other scripts here.
read_cfg() {
  python3 - "$CONFIG" "$1" <<'PY'
import json, sys
cfg = json.load(open(sys.argv[1]))
what = sys.argv[2]
if what == "ref":
    print(cfg.get("ref", ""))
elif what == "version":
    print(cfg.get("version", ""))
elif what == "files":
    for f in cfg.get("files", []):
        print(f)
PY
}

CFG_REF="$(read_cfg ref)"
VERSION="$(read_cfg version)"
[ -n "$REF" ] || REF="$CFG_REF"
[ -n "$REF" ] || die "no ref in $CONFIG and none given with --ref"

# Read into an array without mapfile, which is bash 4+ and absent from the
# /bin/bash that ships on macOS.
FILES=()
while IFS= read -r line; do
  [ -n "$line" ] && FILES+=("$line")
done < <(read_cfg files)
[ "${#FILES[@]}" -gt 0 ] || die "$CONFIG lists no files"

# The guard. Refuse anything that could change the engine under test.
for f in "${FILES[@]}"; do
  case "$f" in
    *_test.go)        ;;
    tests/scripts/*)  ;;
    *) die "refusing $f: only _test.go files and tests/scripts fixtures may be overlaid, because anything else would measure a different engine than the commit under test";;
  esac
  case "$f" in
    /*|*..*) die "refusing $f: paths must be repo-relative with no ..";;
  esac
done

SHA="$(git rev-parse --short "$REF" 2>/dev/null)" || die "cannot resolve ref $REF"

# The stamp. Written next to the overlay so the measuring run can record WHICH
# benchmarks it ran, instead of the reader having to infer it from the shape of
# the numbers after the fact — which is what happened on 2026-07-25, where a
# missed overlay reached the timeline as a +253816% regression.
#
# files_hash digests the overlaid files as written, and perfdata.CorpusAt
# re-computes it before believing the stamp. That is the part that matters:
# `git checkout --force` between rounds discards the overlay but not this
# untracked file, so a stamp that is merely present proves nothing. One that
# still matches the tree does.
STAMP=".bench-corpus-stamp.json"

write_stamp() {
  python3 - "$1/$STAMP" "$VERSION" "$REF" "$SHA" "$1" "${FILES[@]}" <<'PY'
import hashlib, json, os, sys
out, version, ref, sha, root = sys.argv[1:6]
files = sys.argv[6:]

h = hashlib.sha256()
for f in sorted(files):
    b = open(os.path.join(root, f), "rb").read()
    # Length-prefixed, matching perfdata.HashFiles: without it two different
    # splits of the same bytes across files would collide.
    h.update(f.encode() + b"\x00" + str(len(b)).encode() + b"\x00" + b)

json.dump({
    "version": version,
    "ref": ref,
    "sha": sha,
    "files_hash": h.hexdigest()[:16],
    "files": files,
}, open(out, "w"), indent=2)
PY
}

if [ -n "$INTO" ]; then
  [ -d "$INTO" ] || die "no such worktree: $INTO"
  for f in "${FILES[@]}"; do
    if [ "$DRY" -eq 1 ]; then
      printf '  would write %s\n' "$f"
      continue
    fi
    mkdir -p "$INTO/$(dirname "$f")"
    git show "$REF:$f" > "$INTO/$f" 2>/dev/null \
      || die "$f is absent from $REF — the corpus config names a file its own ref does not have"
  done
  if [ "$DRY" -eq 1 ]; then
    printf '  would write %s\n' "$STAMP"
  else
    write_stamp "$INTO"
  fi
  note "corpus $VERSION from $SHA: ${#FILES[@]} file(s) -> $INTO"
fi

# --list: what is actually measurable at this commit, after the overlay.
if [ -n "$LIST" ]; then
  [ -d "$LIST" ] || die "no such worktree: $LIST"
  # Packages are derived from the corpus rather than hardcoded, so adding a
  # benchmark package to the config is enough.
  pkgs=$(printf '%s\n' "${FILES[@]}" | grep '_test\.go$' | xargs -n1 dirname | sort -u)
  for p in $pkgs; do
    if out=$( cd "$LIST" && go test "./$p" -list '^Benchmark' 2>&1 ); then
      printf '%s\n' "$out" | grep '^Benchmark' | sed "s|^|BUILDS\t$p\t|"
    else
      # Does not build against this engine. Every benchmark in the package is
      # unmeasurable here, which is a result and not an error.
      printf 'NA\t%s\t(package does not build at this commit)\n' "$p"
      printf '%s\n' "$out" | head -3 | sed 's|^|      |' >&2
    fi
  done
fi
