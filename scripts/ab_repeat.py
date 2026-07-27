#!/usr/bin/env python3
"""Interleaved repeated A/B for perf-pr variance reduction (paserati port).

Runs N interleaved base/head benchmark snapshots across two pre-built worktrees
and aggregates per-family deltas two ways:

  strategy 1 (median-of-N): gate if median delta > budget
  strategy 2 (confirm):     gate if >= K of N runs exceed +budget

Both are ONE-DIRECTIONAL: only regressions (head slower than base) gate. An
improvement never fails a PR, and benchmark noise on shared runners is itself
one-directional-slower, so a two-sided test would spend its false-positive
budget on the side where nothing can go wrong.

GATING IS OPT-IN, and off by default: `--gate none` (the default) reports the
verdict and exits 0. The evidence for the gate is two captured no-op fixtures
showing zero false positives (scripts/test_ab_repeat.py) — enough to justify
the design, not enough to fail other people's PRs on. Pass --gate median (or
confirm/either) once a maintainer decides to make it authoritative. To override
a suspected false positive, re-run with a larger --n, or with --gate none to
land on the report alone.

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
import argparse, json, os, statistics, subprocess, sys


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


def _summarize(deltas, budget, confirm_k, strip="github.com/nooga/paserati/"):
    """Per-family gate rows from {family: [delta% per cycle]}: the median-of-N
    gate (gate_median = median > budget) and the confirm-K-of-N gate. This is the
    real gate decision — scripts/test_ab_repeat.py drives it on captured samples
    to prove single-shot phantoms vanish under median-of-N.

    Both comparisons are `> budget`, not `abs(...) > budget`: see the module
    docstring on why only regressions gate."""
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
    # median-of-N tolerates floor((N-1)/2) contaminated cycles. Observed CI
    # contamination ran up to 2 of 5 cycles, so N=5 (tolerates 2) sat at the edge
    # and broke on 1/4 runs; N=7 (tolerates 3) held 0 FP/10. N=9 (tolerates 4)
    # is the next ODD step for extra margin near benchstat's >=10-sample guidance
    # — even N is avoided because its median averages the two middle cycles,
    # reintroducing the mean-like tail sensitivity we want to escape.
    ap.add_argument("--n", type=int, default=9, help="repeat count (interleaved cycles); odd")
    ap.add_argument("--profile", default="", help="bench-ratchet -profile (let-go); empty uses --count/--benchtime")
    ap.add_argument("--count", type=int, default=3, help="go test -count per snapshot")
    ap.add_argument("--benchtime", default="500ms")
    ap.add_argument("--budget", type=float, default=10.0, help="gate budget percent")
    ap.add_argument("--confirm-k", type=int, default=2,
                    help="runs that must exceed budget to gate (strategy 2)")
    ap.add_argument("--timeout", default="15m")
    ap.add_argument("--out", default="ab-out")
    # Which verdict, if any, controls the exit status. Default off: the script has
    # always computed these verdicts and always exited 0, so a workflow could not
    # fail a PR on a detected regression and the name "gate" was a promise the code
    # did not keep. Making it selectable fixes the contract without silently turning
    # on a gate that two no-op fixtures do not yet justify.
    ap.add_argument("--gate", choices=("none", "median", "confirm", "either"),
                    default="none",
                    help="which strategy sets a non-zero exit (default none: report only)")
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)

    bench_args = (["-profile", args.profile] if args.profile
                  else ["-count", str(args.count), "-benchtime", args.benchtime])

    deltas = {}          # family -> [delta% per cycle]
    anchors = []         # (base_anchor, head_anchor) per cycle
    # Families that appeared on only one side. `set(b) & set(h)` below silently
    # drops them, which is the right arithmetic (there's no pair to difference) but
    # the wrong silence: a PR that renames or removes a benchmark then shows a clean
    # report for a suite that no longer covers what it used to.
    base_only, head_only = set(), set()
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
        base_only |= set(b) - set(h)
        head_only |= set(h) - set(b)
        for fam in set(b) & set(h):
            if b[fam]:
                deltas.setdefault(fam, []).append((h[fam] / b[fam] - 1.0) * 100)
        print("::endgroup::", flush=True)

    rows = _summarize(deltas, args.budget, args.confirm_k)

    strip = "github.com/nooga/paserati/"
    mismatch = {"base_only": sorted(f.replace(strip, "") for f in base_only),
                "head_only": sorted(f.replace(strip, "") for f in head_only)}

    json.dump({"budget": args.budget, "confirm_k": args.confirm_k,
               "count": args.count, "n": args.n, "gate": args.gate,
               "benchmark_set_mismatch": mismatch,
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

    # A changed benchmark set makes the comparison partial, whichever way the
    # deltas came out — report it next to the verdict rather than in the artifact
    # only, since the table above cannot show a family it never paired.
    if mismatch["base_only"] or mismatch["head_only"]:
        print(f"\nBENCHMARK SET MISMATCH — compared {len(rows)} paired families")
        for side, fams in (("base only (dropped in head)", mismatch["base_only"]),
                           ("head only (added in head)", mismatch["head_only"])):
            if fams:
                print(f"  {side}: {', '.join(fams)}")

    fired = {"none": [], "median": med_hits, "confirm": conf_hits,
             "either": sorted(set(med_hits) | set(conf_hits))}[args.gate]
    if args.gate == "none":
        print(f"\nverdict: informational (--gate none) — "
              f"{len(set(med_hits) | set(conf_hits))} families would gate under some strategy")
        return 0
    if fired:
        print(f"\nverdict: FAIL under --gate {args.gate} — {len(fired)} families: {fired}")
        return 1
    print(f"\nverdict: pass under --gate {args.gate}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
