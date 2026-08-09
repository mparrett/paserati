#!/usr/bin/env python3
"""Decide which of several backfill targets THIS runner should measure.

WHY THIS EXISTS

perf-test262-backfill.yml takes one ref and aborts if the runner it drew is not
a wanted tier. You cannot choose a runner tier on dispatch, so filling a named
tier is a lottery: on 2026-08-09, filling three 7763 points took eight runs —
four of them off-tier aborts that measured nothing and left a red X each.

The waste is not the lottery, it is throwing away a runner that was won. A run
that lands on 7763 can backfill EVERY target that needs a 7763 point, not one.
This script is the "which ones" half, kept out of the workflow so it can be
tested without dispatching anything.

WHAT COUNTS AS NEEDED

A target is worth measuring here when both hold:

  - it has a snapshot on THIS runner's machine key. Ratios normalize within a
    CPU model, so a measurement taken here cannot be written into another tier's
    profile; when no file matches, the honest answer is "not this runner".
  - that snapshot does not already carry the metric. Re-measuring an existing
    point would overwrite it, which is a different operation from backfilling
    and not one a batch loop should do silently.

Both mirror perf-test262-backfill.yml's own checks; the file-matching rule in
particular (match the file to the runner, do not take the first one found) is
the fix from that workflow's inject step, and the two must not drift.
"""

import argparse
import json
import os
import re
import sys

SHORT = 12


def machine_key(path):
    """The snapshot's machine key, v2 (machines map) or v1 (flat machine)."""
    try:
        with open(path) as fh:
            snap = json.load(fh)
    except (OSError, ValueError):
        return None, None
    machines = snap.get("machines")
    if isinstance(machines, dict) and machines:
        key = sorted(machines)[0]
        return key, machines[key]
    m = snap.get("machine") or {}
    if m.get("arch") and m.get("cpu_model"):
        return f"{m['arch']}/{m['cpu_model']}", snap
    return None, None


def snapshots_for(timeline, short):
    """Snapshot files belonging to a commit.

    The stamp prefix varies and the sha may be followed by a machine slug, so
    match the sha as a whole dash-delimited field rather than by substring —
    otherwise a sha that is a prefix of another would collect both.
    """
    pat = re.compile(r"(?:^|-)" + re.escape(short) + r"(?:-|\.json$)")
    out = []
    for name in sorted(os.listdir(timeline)):
        if name.endswith(".json") and pat.search(name):
            out.append(os.path.join(timeline, name))
    return out


def needs(timeline, short, key, metric):
    """(selected, reason) for one target on this runner."""
    files = snapshots_for(timeline, short)
    if not files:
        return False, "no snapshot at all — run the timeline first"
    seen = []
    for f in files:
        fk, prof = machine_key(f)
        if fk is None:
            continue
        seen.append(fk)
        if fk != key:
            continue
        if (prof.get("benchmarks") or {}).get(metric) is not None:
            return False, f"already has {metric}"
        return True, os.path.basename(f)
    return False, "not on this tier (present: " + ", ".join(seen or ["none"]) + ")"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--timeline", required=True, help="perf-data timeline/ directory")
    ap.add_argument("--key", required=True, help="this runner's machine key, from perf-migrate -print-key")
    ap.add_argument("--metric", default="test262.total", help="benchmark key a target must be missing")
    # A won runner should be used, not used forever. The macro is ~4 minutes a
    # target and the job holds the perf-data writer lock throughout, so an
    # unbounded batch on a tier with 23 gaps would block the timeline for an
    # hour and a half. Cap it, and say what the cap dropped — a silent
    # truncation reads as "this tier is now full" when it is not.
    ap.add_argument("--limit", type=int, default=0, help="measure at most N targets (0 = no cap)")
    ap.add_argument("refs", nargs="+", help="target refs (full or short SHAs)")
    args = ap.parse_args()

    selected = []
    for ref in args.refs:
        short = ref.strip()[:SHORT]
        if not short:
            continue
        ok, why = needs(args.timeline, short, args.key, args.metric)
        print(f"{'MEASURE' if ok else 'skip   '}  {short}  {why}", file=sys.stderr)
        if ok:
            selected.append(short)

    if args.limit and len(selected) > args.limit:
        dropped = selected[args.limit:]
        selected = selected[:args.limit]
        print(f"--limit {args.limit}: deferring {len(dropped)} more that this tier still needs "
              f"({', '.join(dropped)}) — re-dispatch to continue", file=sys.stderr)

    # stdout is the machine-readable half: the workflow loops over exactly this.
    for s in selected:
        print(s)

    # Not an error. A runner with nothing to do is the normal case once the
    # wanted tier is full, and it must not paint the run red — that is the
    # noise this whole change exists to remove.
    if not selected:
        print("nothing to measure on this tier", file=sys.stderr)


if __name__ == "__main__":
    main()
