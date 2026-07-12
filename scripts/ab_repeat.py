#!/usr/bin/env python3
"""Interleaved repeated A/B for perf-pr variance reduction (paserati port).

Runs N interleaved base/head benchmark snapshots across two pre-built worktrees
and aggregates per-family deltas two ways:

  strategy 1 (median-of-N): gate if |median delta| > budget
  strategy 2 (confirm):     gate if >= K of N runs exceed +budget

Motivation (nooga/paserati#21): a single-shot base-vs-head A/B on shared CI
runners is too heavy-tailed to gate on — the register-only BenchmarkRatchetAnchor
is blind to memory-bandwidth contention, so memory-bound families (GetOwn on deep
objects, PrototypeMethodAccess chain walks) blow out 14-27% on no-op PRs while the
anchor stays flat. Interleaving + repetition suppresses that.

This composes with the min-of-count reducer (nooga/paserati#22): min collapses
noise WITHIN a capture (across -count repeats), median collapses it ACROSS the N
base/head cycles. This driver only reads snapshots and touches no cmd/bench-ratchet
code.

Each side is snapshotted with `bench-ratchet ... snapshot`, which emits
ratio_to_anchor per benchmark; delta = head_ratio / base_ratio - 1.
"""
import argparse, json, os, statistics, subprocess


def _read_snapshot(path):
    """Return ({family: ratio_to_anchor}, anchor_ns). Handles both the flat
    baseline (paserati: anchor/benchmarks at top level) and the machines-wrapped
    snapshot (let-go)."""
    d = json.load(open(path))
    node = d
    if "machines" in d:
        (_, node), = d["machines"].items()
    benches = {k: e["ratio_to_anchor"] for k, e in node["benchmarks"].items()}
    return benches, node["anchor"]["ns_per_op"]


def snapshot(worktree, bench_args, out, timeout):
    subprocess.run(
        ["go", "run", "./cmd/bench-ratchet", *bench_args,
         "-timeout", timeout, "-baseline", out, "snapshot"],
        cwd=worktree, check=True,
    )
    return _read_snapshot(out)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True, help="pre-built base worktree")
    ap.add_argument("--head", required=True, help="pre-built head worktree")
    ap.add_argument("--n", type=int, default=7, help="repeat count (cycles)")
    ap.add_argument("--profile", default="", help="bench-ratchet -profile (let-go); empty uses --count/--benchtime")
    ap.add_argument("--count", type=int, default=3, help="go test -count per snapshot")
    ap.add_argument("--benchtime", default="500ms")
    ap.add_argument("--budget", type=float, default=10.0, help="gate budget percent")
    ap.add_argument("--confirm-k", type=int, default=2,
                    help="runs that must exceed budget to gate (strategy 2)")
    ap.add_argument("--timeout", default="15m")
    ap.add_argument("--out", default="ab-out")
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)

    bench_args = (["-profile", args.profile] if args.profile
                  else ["-count", str(args.count), "-benchtime", args.benchtime])

    deltas = {}          # family -> [delta% per cycle]
    anchors = []         # (base_anchor, head_anchor) per cycle
    for i in range(1, args.n + 1):
        print(f"::group::cycle {i}/{args.n}", flush=True)
        # Counterbalance the measurement order (ABBA): odd cycles bench base
        # first, even cycles head first. Base and head are identical code, so a
        # fixed base-then-head order lets any first-vs-second-position drift
        # (warmup, cache, thermal) masquerade as a consistent head regression —
        # a systematic bias the median cannot remove. Alternating cancels it.
        if i % 2 == 1:
            b, ba = snapshot(args.base, bench_args, f"{args.out}/base_{i}.json", args.timeout)
            h, ha = snapshot(args.head, bench_args, f"{args.out}/head_{i}.json", args.timeout)
        else:
            h, ha = snapshot(args.head, bench_args, f"{args.out}/head_{i}.json", args.timeout)
            b, ba = snapshot(args.base, bench_args, f"{args.out}/base_{i}.json", args.timeout)
        anchors.append((ba, ha))
        for fam in set(b) & set(h):
            if b[fam]:
                deltas.setdefault(fam, []).append((h[fam] / b[fam] - 1.0) * 100)
        print("::endgroup::", flush=True)

    rows = []
    for fam, ds in deltas.items():
        med = statistics.median(ds)
        exceed = sum(1 for d in ds if d > args.budget)
        rows.append({
            "fam": fam.replace("github.com/nooga/paserati/", ""),
            "n": len(ds), "median": med, "worst": max(ds, key=abs),
            "exceed": exceed, "ds": ds,
            "gate_median": med > args.budget,
            "gate_confirm": exceed >= args.confirm_k,
        })
    rows.sort(key=lambda r: r["median"], reverse=True)

    json.dump({"budget": args.budget, "confirm_k": args.confirm_k,
               "count": args.count, "n": args.n,
               "anchors": anchors, "rows": rows},
              open(f"{args.out}/aggregate.json", "w"), indent=2)

    print(f"\n=== interleaved A/B, N={args.n}, count={args.count}, "
          f"budget={args.budget}%, confirm K={args.confirm_k} ===")
    print("anchor stability per cycle (base/head ns/op):")
    for i, (ba, ha) in enumerate(anchors, 1):
        print(f"  cycle {i}: base {ba:.3f}  head {ha:.3f}  Δ{(ha/ba-1)*100:+.1f}%")
    print(f"\n{'family':44} {'median%':>8} {'worst%':>7} {'exc':>3}  verdict")
    print("-" * 82)
    for r in rows[:14]:
        v = []
        if r["gate_median"]: v.append("MEDIAN")
        if r["gate_confirm"]: v.append("CONFIRM")
        print(f"{r['fam']:44} {r['median']:+8.2f} {r['worst']:+7.2f} "
              f"{r['exceed']:3d}  {','.join(v) if v else 'ok'}")
    med_hits = [r["fam"] for r in rows if r["gate_median"]]
    conf_hits = [r["fam"] for r in rows if r["gate_confirm"]]
    print("-" * 82)
    print(f"strategy 1 (median>{args.budget}%): {len(med_hits)} families gate {med_hits or ''}")
    print(f"strategy 2 (>={args.confirm_k}/{args.n} exceed {args.budget}%): "
          f"{len(conf_hits)} families gate {conf_hits or ''}")
    # Same-code probe → any gate is a FALSE POSITIVE.
    print(f"\nFALSE-POSITIVE gates at budget {args.budget}%: "
          f"median={len(med_hits)}  confirm={len(conf_hits)}  "
          f"(both should be 0 on a no-op PR)")


if __name__ == "__main__":
    main()
