#!/usr/bin/env bash
# Capture perf snapshots on this machine (a local tier, not CI) for a few commits.
#
# Safe to run only because snapshots are machine-keyed (v2): the filename carries
# <arch>-<cpumodel>, so these land beside the CI EPYC snapshots for the same commit
# instead of overwriting them. Under v1 this would have destroyed data.
#
# Order is deliberate: the FIRST commit is measured again at the END, and the two
# readings are compared. A laptop is not a quiet benchmark host — thermals, other
# processes and power state all drift over a sweep — so without a repeat there is
# no way to tell a real between-commit difference from the machine getting slower
# as it heats up. The repeat is a control, not a data point; only the first
# reading is published.
#
# Test262 is skipped: it needs the corpus and ~12 min per commit. The page treats
# test262.total as sparse already.
#
# Usage: m2-sweep.sh <outdir> <sha>...

set -uo pipefail

OUT="${1:?usage: m2-sweep.sh <outdir> <sha>...}"; shift
[ $# -gt 0 ] || { echo "no commits given" >&2; exit 2; }

REPO="${REPO:-/Users/matt/projects-new/3p/paserati-pagesort}"
COUNT="${COUNT:-3}"
BENCHTIME="${BENCHTIME:-1s}"
WT="${TMPDIR:-/tmp}/m2sweep-wt"

mkdir -p "$OUT"
log() { printf '%s  %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }

capture() {           # capture <sha> <destfile>
  local sha="$1" dest="$2" epoch stamp short
  # `rm -rf` alone is not enough: git keeps the worktree registered even once the
  # directory is gone, so the next `add` fails with "missing but already
  # registered". Prune the registration too, or every capture after the first dies
  # instantly.
  git -C "$REPO" worktree remove --force "$WT" >/dev/null 2>&1 || true
  rm -rf "$WT"
  git -C "$REPO" worktree prune
  git -C "$REPO" worktree add --detach "$WT" "$sha" >/dev/null 2>&1 || return 1

  short="$(git -C "$REPO" rev-parse --short=12 "$sha")"
  epoch="$(git -C "$REPO" show -s --format=%ct "$sha")"
  stamp="$(date -u -r "$epoch" +%Y%m%dT%H%M%SZ)"

  ( cd "$WT" && go run ./cmd/bench-ratchet \
      -count "$COUNT" -benchtime "$BENCHTIME" -timeout 20m \
      -baseline "$dest" snapshot ) >/dev/null 2>&1 || return 1

  # Same provenance the CI workflow records, so local and CI snapshots are
  # readable by the same tooling.
  local subject committed changed files engine
  subject="$(git -C "$REPO" log -1 --format=%s "$sha")"
  committed="$(git -C "$REPO" log -1 --format=%cI "$sha")"
  changed="$(git -C "$REPO" diff --name-only "${sha}^" "$sha" 2>/dev/null || true)"
  files="$(printf '%s\n' "$changed" | grep -c . || true)"
  engine="$(printf '%s\n' "$changed" | grep -E '^(pkg|cmd)/' \
            | grep -vE '^(pkg/perfdata/|cmd/(bench-|perf-))' | grep -c . || true)"

  # MERGE into method, don't replace it. bench-ratchet writes reducer/count/
  # benchtime itself now, from the code that reduces and the flags it was given;
  # rebuilding the object here would drop that and reinstate the literal
  # reducer:"min" that goes stale the moment nooga#22 lands. Only `host` is ours
  # to add — it is the one fact bench-ratchet cannot know.
  jq --arg s "$subject" --arg ca "$committed" \
     --argjson fc "${files:-0}" --argjson ec "${engine:-0}" \
     '. + { commit: { subject: $s, committed_at: $ca,
                      files_changed: $fc, engine_files_changed: $ec } }
        | .method = ((.method // {}) + { host: "local" })' \
     "$dest" > "$dest.tmp" && mv "$dest.tmp" "$dest"

  printf '%s-%s.json' "$stamp" "$short"
}

first_sha="$1"
log "drift control: measuring ${first_sha:0:12} before the sweep"
ctl_before="$OUT/.control-before.json"
name="$(capture "$first_sha" "$ctl_before")" || { log "control run failed"; exit 1; }

n=0; total=$#
for sha in "$@"; do
  n=$((n + 1))
  log "[$n/$total] ${sha:0:12}  $(git -C "$REPO" log -1 --format=%s "$sha" | cut -c1-52)"
  tmp="$OUT/.tmp.json"
  fname="$(capture "$sha" "$tmp")" || { log "  ! capture failed"; continue; }
  mv "$tmp" "$OUT/$fname"
  log "  wrote $fname  anchor=$(jq -r '.anchor.ns_per_op' "$OUT/$fname")"
done

log "drift control: re-measuring ${first_sha:0:12} after the sweep"
ctl_after="$OUT/.control-after.json"
capture "$first_sha" "$ctl_after" >/dev/null || { log "control re-run failed"; exit 1; }

a="$(jq -r '.anchor.ns_per_op' "$ctl_before")"
b="$(jq -r '.anchor.ns_per_op' "$ctl_after")"
log "anchor drift across sweep: ${a} -> ${b} ns ($(awk -v x="$a" -v y="$b" 'BEGIN{printf "%+.2f%%", (y-x)/x*100}'))"

# Same commit, same machine, minutes apart: this is the noise floor for everything
# above. If it is large, between-commit differences in this sweep mean nothing.
jq -n --slurpfile p "$ctl_before" --slurpfile q "$ctl_after" '
  [ $p[0].benchmarks | keys[] as $k
    | select($q[0].benchmarks[$k] != null)
    | { k: $k, d: (($q[0].benchmarks[$k].ratio_to_anchor / $p[0].benchmarks[$k].ratio_to_anchor) - 1) } ]
  | { n: length,
      median_abs_pct: ((map(.d | fabs) | sort)[(length/2|floor)] * 100),
      worst_pct: (max_by(.d | fabs) | {bench: .k, pct: (.d * 100)}) }' \
  | tee "$OUT/.drift.json"

git -C "$REPO" worktree remove --force "$WT" 2>/dev/null || true
rm -rf "$WT"
log "done — snapshots in $OUT"
