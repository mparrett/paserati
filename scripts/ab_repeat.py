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

Memory-aware anchor: hardening showed median-of-N alone bottoms out around a ~7%
worst-family tail because the residual is fat-tailed memory contention in
ratio_to_anchor itself (family_ns / cpu_anchor_ns), and the CPU anchor can't
normalize memory noise. When a BenchmarkRatchetMemAnchor (DRAM-latency-bound) is
present in the snapshot, we ALSO report a memory-normalized delta: dividing each
family's ratio by the mem-anchor's ratio cancels the CPU anchor (leaving
family_ns / memanchor_ns) so common-mode memory contention divides out. Family and
mem-anchor are captured in the same snapshot, so they share a contention
environment even though base and head snapshots run in different cycles.

Each side is snapshotted with `bench-ratchet ... snapshot`, which emits
ratio_to_anchor per benchmark; delta = head_ratio / base_ratio - 1.
"""
import argparse, json, os, statistics, subprocess


def _read_snapshot(path):
    """Return ({family: ratio_to_anchor}, anchor_ns, mem_ratio). mem_ratio is the
    BenchmarkRatchetMemAnchor ratio_to_anchor (popped out of the family set), or
    None on pre-mem-anchor trees. Handles both the flat baseline (paserati:
    anchor/benchmarks at top level) and the machines-wrapped snapshot (let-go)."""
    d = json.load(open(path))
    node = d
    if "machines" in d:
        (_, node), = d["machines"].items()
    benches = {k: e["ratio_to_anchor"] for k, e in node["benchmarks"].items()}
    mem_key = next((k for k in benches if "RatchetMemAnchor" in k), None)
    mem_ratio = benches.pop(mem_key) if mem_key else None
    return benches, node["anchor"]["ns_per_op"], mem_ratio


def snapshot(worktree, bench_args, out, timeout):
    subprocess.run(
        ["go", "run", "./cmd/bench-ratchet", *bench_args,
         "-timeout", timeout, "-baseline", out, "snapshot"],
        cwd=worktree, check=True,
    )
    return _read_snapshot(out)


def _summarize(deltas, budget, confirm_k, strip):
    """Build sorted per-family rows from {family: [delta% per cycle]}."""
    rows = []
    for fam, ds in deltas.items():
        med = statistics.median(ds)
        exceed = sum(1 for d in ds if d > budget)
        rows.append({
            "fam": fam.replace(strip, ""),
            "n": len(ds), "median": med, "worst": max(ds, key=abs),
            "exceed": exceed, "ds": ds,
            "gate_median": med > budget,
            "gate_confirm": exceed >= confirm_k,
        })
    rows.sort(key=lambda r: r["median"], reverse=True)
    return rows


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

    strip = "github.com/nooga/paserati/"
    deltas = {}          # family -> [raw delta% per cycle]
    ndeltas = {}         # family -> [mem-normalized delta% per cycle]
    anchors = []         # (base_cpu, head_cpu, base_mem, head_mem) per cycle
    for i in range(1, args.n + 1):
        print(f"::group::cycle {i}/{args.n}", flush=True)
        # Counterbalance the measurement order (ABBA): odd cycles bench base
        # first, even cycles head first. Base and head are identical code, so a
        # fixed base-then-head order lets any first-vs-second-position drift
        # (warmup, cache, thermal) masquerade as a consistent head regression —
        # a systematic bias the median cannot remove. Alternating cancels it.
        if i % 2 == 1:
            b, ba, bm = snapshot(args.base, bench_args, f"{args.out}/base_{i}.json", args.timeout)
            h, ha, hm = snapshot(args.head, bench_args, f"{args.out}/head_{i}.json", args.timeout)
        else:
            h, ha, hm = snapshot(args.head, bench_args, f"{args.out}/head_{i}.json", args.timeout)
            b, ba, bm = snapshot(args.base, bench_args, f"{args.out}/base_{i}.json", args.timeout)
        anchors.append((ba, ha, bm, hm))
        for fam in set(b) & set(h):
            if b[fam]:
                deltas.setdefault(fam, []).append((h[fam] / b[fam] - 1.0) * 100)
                # Memory-normalized: (h_fam/h_mem)/(b_fam/b_mem) - 1. Divides out
                # the common-mode memory contention each side shares with the
                # mem-anchor measured in the same snapshot.
                if bm and hm:
                    nb, nh = b[fam] / bm, h[fam] / hm
                    if nb:
                        ndeltas.setdefault(fam, []).append((nh / nb - 1.0) * 100)
        print("::endgroup::", flush=True)

    have_mem = bool(ndeltas)
    rows = _summarize(deltas, args.budget, args.confirm_k, strip)
    nrows = _summarize(ndeltas, args.budget, args.confirm_k, strip) if have_mem else []

    json.dump({"budget": args.budget, "confirm_k": args.confirm_k,
               "count": args.count, "n": args.n, "have_mem": have_mem,
               "anchors": anchors, "rows": rows, "nrows": nrows},
              open(f"{args.out}/aggregate.json", "w"), indent=2)

    print(f"\n=== interleaved A/B, N={args.n}, count={args.count}, "
          f"budget={args.budget}%, confirm K={args.confirm_k} ===")
    print("anchor stability per cycle (cpu base/head ns/op | mem base/head ratio):")
    for i, (ba, ha, bm, hm) in enumerate(anchors, 1):
        mem = f"  mem {bm:.2f}/{hm:.2f} Δ{(hm/bm-1)*100:+.1f}%" if (bm and hm) else "  mem n/a"
        print(f"  cycle {i}: cpu {ba:.3f}/{ha:.3f} Δ{(ha/ba-1)*100:+.1f}%{mem}")

    def _table(title, rs):
        print(f"\n{title}")
        print(f"{'family':44} {'median%':>8} {'worst%':>7} {'exc':>3}  verdict")
        print("-" * 82)
        for r in rs[:14]:
            v = []
            if r["gate_median"]: v.append("MEDIAN")
            if r["gate_confirm"]: v.append("CONFIRM")
            print(f"{r['fam']:44} {r['median']:+8.2f} {r['worst']:+7.2f} "
                  f"{r['exceed']:3d}  {','.join(v) if v else 'ok'}")

    _table("RAW (family_ns / cpu_anchor):", rows)
    if have_mem:
        _table("MEM-NORMALIZED (family_ns / mem_anchor):", nrows)

    def _fp(rs):
        return ([r["fam"] for r in rs if r["gate_median"]],
                [r["fam"] for r in rs if r["gate_confirm"]])
    med_hits, conf_hits = _fp(rows)
    nmed_hits, nconf_hits = _fp(nrows) if have_mem else ([], [])

    print("-" * 82)
    # Same-code probe → any gate is a FALSE POSITIVE (both should be 0).
    print(f"FALSE-POSITIVE gates at budget {args.budget}%:")
    print(f"  RAW:            median={len(med_hits)}  confirm={len(conf_hits)}  "
          f"{med_hits or ''}")
    if have_mem:
        wr = max((abs(r["median"]) for r in rows), default=0)
        wn = max((abs(r["median"]) for r in nrows), default=0)
        print(f"  MEM-NORMALIZED: median={len(nmed_hits)}  confirm={len(nconf_hits)}  "
              f"{nmed_hits or ''}")
        print(f"  worst |median|: raw {wr:.2f}%  ->  mem-normalized {wn:.2f}%")
    else:
        print("  (no BenchmarkRatchetMemAnchor in snapshot — mem-normalization skipped)")


if __name__ == "__main__":
    main()
