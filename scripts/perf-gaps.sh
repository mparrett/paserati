#!/usr/bin/env bash
# What is missing from the timeline, as a work list.
#
# Answers the three questions you actually need before dispatching anything:
#
#   1. which plottable commits have NO snapshot on the reference tier
#      (the fidelity gap — these are points the default view is missing);
#   2. which reference-tier profiles lack `test262.total`
#      (the Test262 backfill queue — empty as of 2026-07-27);
#   3. which recent main commits have no snapshot at all
#      (the pipeline-is-broken signal — this is how a silently dead forward
#      writer shows up, and it stayed invisible for two runs once because the
#      live page renders off already-migrated historical data).
#
# Reads both layouts. Plottable == reachable from the source repo's HEAD, which
# is the same rule pages.yml ranks by; commits unreachable from HEAD are
# tombstoned and can never be snapshotted, so they are excluded rather than
# reported as gaps.
#
# Usage: perf-gaps.sh [<n-recent-commits>]     (default 25)
# Env:   SRC=<path to a main checkout>  DATA=<path to a perf-data checkout>
#        REF_CPU='EPYC 7763'
set -euo pipefail

SRC="${SRC:-$HOME/projects-new/3p/paserati-pagesort}"
DATA="${DATA:-$HOME/projects-new/3p/paserati-perfdata}"
REF_CPU="${REF_CPU:-EPYC 7763}"
RECENT="${1:-25}"

git -C "$DATA" fetch -q origin perf-data && git -C "$DATA" reset --hard -q origin/perf-data
git -C "$SRC" fetch -q origin main

SRC="$SRC" DATA="$DATA" REF_CPU="$REF_CPU" RECENT="$RECENT" python3 - <<'PY'
import json, os, glob, subprocess, collections

src, data = os.environ["SRC"], os.environ["DATA"]
ref_cpu, recent = os.environ["REF_CPU"], int(os.environ["RECENT"])

def git(*a):
    return subprocess.run(["git","-C",src,*a],capture_output=True,text=True).stdout

reach = {c[:12] for c in git("rev-list","--topo-order","origin/main").split()}

profiles = collections.defaultdict(list)   # sha -> [cpu_model]
have_t262 = collections.defaultdict(bool)  # (sha,cpu) -> bool
for f in sorted(glob.glob(os.path.join(data,"timeline","*.json"))):
    base = os.path.basename(f)
    if base in ("index.json","unreachable.json"):
        continue
    d = json.load(open(f))
    sha = base[:-5].split("-")[1]
    for p in (d["machines"].values() if "machines" in d else [d]):
        cpu = p.get("machine",{}).get("cpu_model","?")
        profiles[sha].append(cpu)
        if "test262.total" in p.get("benchmarks",{}):
            have_t262[(sha,cpu)] = True

def onref(cpus): return any(ref_cpu in c for c in cpus)

print(f"reference tier: {ref_cpu}")
print(f"snapshots: {sum(len(v) for v in profiles.values())} profiles over {len(profiles)} commits\n")

# 1. fidelity gap
gaps = sorted((s for s in profiles if s in reach and not onref(profiles[s])),
              key=lambda s: git("log","-1","--format=%ct",s).strip() or "0")
print(f"[1] plottable commits with no reference-tier snapshot: {len(gaps)}")
for s in gaps:
    print(f"    {s}  {git('log','-1','--format=%cs',s).strip()}  "
          f"has={sorted(set(profiles[s]))}  {git('log','-1','--format=%s',s).strip()[:46]}")

# 2. test262 backfill queue
miss = [(s,c) for s in profiles if s in reach for c in set(profiles[s])
        if ref_cpu in c and not have_t262[(s,c)]]
print(f"\n[2] reference-tier profiles missing test262.total: {len(miss)}")
for s,c in sorted(miss): print(f"    {s}")

# 3. never-snapshotted recent commits
head = [l.split(None,1) for l in git("log","--format=%h %s",f"-{recent}","origin/main").splitlines()]
never = [(h,m) for h,m in head if git("rev-parse",f"{h}^{{commit}}").strip()[:12] not in profiles]
print(f"\n[3] of the last {recent} commits on main, {len(never)} have no snapshot:")
for h,m in never:
    full = git("rev-parse",f"{h}^{{commit}}").strip()[:12]
    print(f"    {full}  {m[:56]}")
if never:
    print("\n    NOTE: page-only commits are skipped on purpose (paths-ignore).")
    print("    A run of engine/tooling commits here means the writer is failing.")

print("\nfidelity dispatch list:")
print("  " + " ".join(gaps) if gaps else "  (none)")
PY
