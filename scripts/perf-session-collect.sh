#!/usr/bin/env bash
# Pull a finished session off the box, render it, and say what is left to do.
#
# Usage: scripts/perf-session-collect.sh -h HOST [-k KEY] [-o DIR]
#
#   -h, --host HOST   ec2-user@<ip>
#   -k, --key  KEY    ssh key (default ./.perf-box/key.pem)
#   -o, --out  DIR    where to put it (default ./perf-session-collected)
#
# Deliberately does NOT tear the box down. Collect, look at the numbers, and
# only then run `perf-session-box.sh down` — a box that is already terminated
# cannot be re-queried when a number looks wrong.
set -euo pipefail

die() { printf 'collect: %s\n' "$*" >&2; exit 1; }
note() { printf 'collect: %s\n' "$*" >&2; }

HOST=""; KEY="./.perf-box/key.pem"; OUT="./perf-session-collected"
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--host) HOST="${2:?}"; shift 2;;
    -k|--key)  KEY="${2:?}"; shift 2;;
    -o|--out)  OUT="${2:?}"; shift 2;;
    --help)    sed -n '2,14p' "$0"; exit 0;;
    *)         die "unknown argument: $1";;
  esac
done
[ -n "$HOST" ] || die "need --host ec2-user@<ip>"
[ -f "$KEY" ] || die "no ssh key at $KEY"

REPO="$(git rev-parse --show-toplevel)"
mkdir -p "$OUT"

note "pulling session"
scp -q -o StrictHostKeyChecking=accept-new -i "$KEY" -r "$HOST:~/session" "$OUT/" \
  || die "could not pull ~/session"
for f in calib-tests.json calib-vm.json pins.json run3.log engines.json measure-list.txt; do
  scp -q -o StrictHostKeyChecking=accept-new -i "$KEY" "$HOST:~/$f" "$OUT/" 2>/dev/null || true
done

cells=$(find "$OUT/session/raw" -name '*.json' 2>/dev/null | wc -l | tr -d ' ')
snaps=$(find "$OUT/session/snapshots" -name '*.json' 2>/dev/null | wc -l | tr -d ' ')
note "$cells raw cells, $snaps snapshots"

# A session that never reached its aggregation phase has no snapshots. Say so
# rather than rendering a page from a partial corpus and calling it a result.
if [ "$snaps" -eq 0 ]; then
  note "NO SNAPSHOTS — the session did not finish aggregating."
  note "Re-run the identical perf-session.sh command on the box to continue;"
  note "completed cells are skipped."
  exit 1
fi

note "extracting (A10 family split: micro on ns/op, macro on ratio_to_anchor)"
PASERATI_REPO="$REPO" python3 "$REPO/scripts/perf-session-extract.py" \
  "$OUT/session" "$OUT/session-data.json" >/dev/null

VIZ="$(dirname "$REPO")/../project-docs/docs/paserati/evidence/2026-07-31-microopt-session/regenerate-viz.py"
if [ -f "$VIZ" ]; then
  python3 "$VIZ" "$OUT/session-data.json" "$OUT/session.html" >&2
else
  note "renderer not found at $VIZ — data is in $OUT/session-data.json"
fi

cat >&2 <<EOF

collected to $OUT

Next, in this order:
  1. read the numbers — $OUT/session.html
  2. tear the box down   — scripts/perf-session-box.sh down
  3. verify it is gone   — scripts/perf-session-box.sh status
  4. publish, separately — scripts/perf-session-publish.sh

Still outstanding before the timeline shows this:
  * perf-data needs the BenchmarkFactorial remap (branch perfdata/rename-factorial)
  * results map onto all 60 timeline commits via engines.json, not just the 21 measured
EOF
