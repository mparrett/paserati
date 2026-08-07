#!/usr/bin/env python3
"""Analyse an accumulating bisect and choose the next commit to measure.

Reads the per-level sessions perf-bisect.sh has produced so far, works out for
each benchmark which adjacent pair of measured commits still brackets a change,
and prints either the next commit to measure or a report.

WHY THIS IS NOT JUST git bisect

git bisect asks a yes/no question and needs a single ordering. Here every
benchmark bisects independently and the answer is a magnitude, not a bit. But a
measurement cell costs the same whether one benchmark or thirty-five care about
it, so the commits are chosen to split the *union* of outstanding brackets: each
new cell serves every benchmark at once. That is why this converges in roughly
log2(N) cells total rather than log2(N) per benchmark.

WHAT IT REFUSES TO DO

Compare across machines. Every level must carry the same machine_key; the whole
point of accumulating on one live box is that the between-host term cancels
instead of being normalised away, and mixing keys silently forfeits that. A
mismatch is an error, not a warning.
"""

import argparse
import json
import math
import os
import re
import sys

# perf-migrate renames snapshots to <stamp>-<sha>-<machine-key-slug>.json, so
# neither the first nor the last dash-separated field is the commit. Pick the
# field that actually looks like one: the stamp carries T and Z, and the key
# slug carries non-hex letters, so a pure-hex run is unambiguous.
_SHA_FIELD = re.compile(r"^[0-9a-f]{7,40}$")


def display(name):
    """Snapshot keys are package-qualified; the package is noise in a table."""
    return "Benchmark" + name.split(".Benchmark", 1)[1] if ".Benchmark" in name else name


def sha_from_snapshot(filename):
    for field in filename[:-5].split("-"):
        if _SHA_FIELD.match(field):
            return field
    return None


def die(msg):
    print(f"error: {msg}", file=sys.stderr)
    sys.exit(1)


def load_levels(outdir):
    """Every snapshot measured so far, keyed by short sha.

    Later levels win on a repeated commit: a re-measurement is deliberate.
    """
    measured, keys, drifts = {}, set(), {}
    levels = sorted(
        d for d in os.listdir(outdir)
        if d.startswith("level-") and os.path.isdir(os.path.join(outdir, d))
    )
    for lvl in levels:
        ldir = os.path.join(outdir, lvl)
        meta_path = os.path.join(ldir, "session.json")
        snapdir = os.path.join(ldir, "snapshots")
        if not (os.path.isfile(meta_path) and os.path.isdir(snapdir)):
            continue  # a level that was killed mid-run; resumable, not fatal
        meta = json.load(open(meta_path))["session"]
        keys.add(meta["machine_key"])
        drifts[lvl] = meta.get("anchor_drift_pct")
        for fn in sorted(os.listdir(snapdir)):
            if not fn.endswith(".json"):
                continue
            snap = json.load(open(os.path.join(snapdir, fn)))
            for mkey, m in snap.get("machines", {}).items():
                benches = {
                    name: b["ratio_to_anchor"]
                    for name, b in m.get("benchmarks", {}).items()
                    if b.get("ratio_to_anchor")
                }
                sha = sha_from_snapshot(fn)
                if benches and sha:
                    measured[sha] = {
                        "level": lvl, "key": mkey, "benchmarks": benches,
                    }
    return measured, keys, drifts


def ordered(measured, commits):
    """Measured commits in history order, as (index, short) pairs."""
    out = []
    for i, sha in enumerate(commits):
        for short in measured:
            if sha.startswith(short) or short.startswith(sha[:len(short)]):
                out.append((i, short))
                break
    return sorted(set(out))


def brackets(measured, commits, threshold):
    """Adjacent measured pairs that still contain unmeasured commits and a move.

    Returns (gap_lo_index, gap_hi_index, benchmark, pct) per outstanding bracket,
    largest move first.
    """
    pts = ordered(measured, commits)
    out = []
    for (i0, s0), (i1, s1) in zip(pts, pts[1:]):
        if i1 - i0 < 2:
            continue  # nothing unmeasured between them; already resolved
        b0 = measured[s0]["benchmarks"]
        b1 = measured[s1]["benchmarks"]
        for name in sorted(set(b0) & set(b1)):
            if b0[name] <= 0 or b1[name] <= 0:
                continue
            pct = (b1[name] / b0[name] - 1.0) * 100.0
            if abs(pct) >= threshold:
                out.append((i0, i1, name, pct))
    out.sort(key=lambda r: -abs(r[3]))
    return out


def pick_next(measured, commits, threshold):
    """The commit that splits the most outstanding bracket-weight at once."""
    outstanding = brackets(measured, commits, threshold)
    if not outstanding:
        return None, outstanding
    # Weight a gap by its largest move, so the biggest unexplained change is
    # always what the next cell is spent on.
    weight = {}
    for i0, i1, _name, pct in outstanding:
        weight[(i0, i1)] = max(weight.get((i0, i1), 0.0), abs(pct))
    (i0, i1), _ = max(weight.items(), key=lambda kv: kv[1])
    return commits[(i0 + i1) // 2], outstanding


def classify(measured, commits, threshold):
    """Step or ramp, per benchmark, from three or more measured points.

    A step puts the midpoint at one end in log space; a ramp puts it in between.
    Told apart early this decides whether bisecting is worth continuing at all —
    against a ramp there is no culprit commit to find and it subdivides forever.
    """
    pts = ordered(measured, commits)
    if len(pts) < 3:
        return []
    lo, mid, hi = pts[0][1], pts[len(pts) // 2][1], pts[-1][1]
    rows = []
    for name in sorted(
        set(measured[lo]["benchmarks"])
        & set(measured[mid]["benchmarks"])
        & set(measured[hi]["benchmarks"])
    ):
        a = measured[lo]["benchmarks"][name]
        m = measured[mid]["benchmarks"][name]
        z = measured[hi]["benchmarks"][name]
        if min(a, m, z) <= 0:
            continue
        total = (z / a - 1.0) * 100.0
        if abs(total) < threshold:
            rows.append((name, total, "flat", None))
            continue
        span = math.log(z) - math.log(a)
        frac = (math.log(m) - math.log(a)) / span if span else 0.5
        if frac < 0.25:
            shape = "step late"
        elif frac > 0.75:
            shape = "step early"
        else:
            shape = "ramp"
        rows.append((name, total, shape, frac))
    rows.sort(key=lambda r: -abs(r[1]))
    return rows


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True, help="bisect directory")
    ap.add_argument("--commits", required=True, help="ordered sha list, oldest first")
    ap.add_argument("--threshold", type=float, default=5.0,
                    help="percent a benchmark must move to be worth subdividing")
    ap.add_argument("--mode", choices=["next", "report"], default="report")
    args = ap.parse_args()

    commits = [l.strip() for l in open(args.commits) if l.strip()]
    if len(commits) < 3:
        die("need at least three commits to bisect")

    measured, keys, drifts = load_levels(args.out)
    if not measured:
        die(f"no completed levels under {args.out}")
    if len(keys) > 1:
        die("levels span multiple machine keys (" + ", ".join(sorted(keys)) + ") — "
            "these are not comparable; the box must stay the same across levels")

    if args.mode == "next":
        nxt, _ = pick_next(measured, commits, args.threshold)
        print(nxt or "")
        return

    pts = ordered(measured, commits)
    print(f"# Bisect — {len(pts)} of {len(commits)} commits measured")
    print()
    print(f"Machine key `{next(iter(keys))}`. Anchor drift per level: " +
          ", ".join(f"{l} {d}%" for l, d in sorted(drifts.items())) + ".")
    print()
    print("Same box across every level, so these are directly comparable; the")
    print("drift figures are the check on that, not a formality.")
    print()

    shapes = classify(measured, commits, args.threshold)
    if shapes:
        print("## Shape — is there a commit to find?")
        print()
        print("| benchmark | total Δ | shape | midpoint position |")
        print("|---|---|---|---|")
        for name, total, shape, frac in shapes[:20]:
            pos = "-" if frac is None else f"{frac:.2f}"
            print(f"| `{display(name)}` | {total:+.2f}% | {shape} | {pos} |")
        print()
        print("A **step** has a culprit commit and bisecting will find it. A **ramp**")
        print("is accumulated change with no single cause — subdividing it just buys")
        print("resolution, so stop when the resolution is enough.")
        print()

    outstanding = brackets(measured, commits, args.threshold)
    nxt, _ = pick_next(measured, commits, args.threshold)
    if not outstanding:
        print(f"## Done — nothing outstanding above {args.threshold}%")
        return
    print("## Outstanding brackets")
    print()
    print("| benchmark | Δ across bracket | bracket | unmeasured inside |")
    print("|---|---|---|---|")
    for i0, i1, name, pct in outstanding[:20]:
        print(f"| `{display(name)}` | {pct:+.2f}% | {commits[i0][:8]}..{commits[i1][:8]} | {i1 - i0 - 1} |")
    print()
    print(f"**Next:** `{nxt}`")


if __name__ == "__main__":
    main()
