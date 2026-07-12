#!/usr/bin/env python3
"""Preserved reproduction: the memory-aware anchor does NOT work (nooga/paserati#21).

Median-of-N hardening bottomed out at a ~7% worst-family tail — fat-tailed memory
contention in ratio_to_anchor that the register-only CPU anchor can't normalize. A
BenchmarkRatchetMemAnchor (DRAM-latency-bound) was added to normalize it: divide
each family's ratio by the mem-anchor's. On a synthetic same-instant A/B it worked
(+20% -> 0%). On real CI it FAILS, and this reproduces why from a captured no-op
run (testdata/mem_anchor_sample.json — same-code, so every gate is a false positive):

  - ÷mem worst |median| BLOWS UP vs raw when the anchor swings hard (4.09% -> 13.23%),
  - per-family beta (family-swing regressed on anchor-swing, per cycle) is pure
    scatter -> no per-cycle co-variation for the normalization to cancel.

Root cause: `go test -bench` runs benchmarks sequentially, so the anchor and each
family are measured seconds apart; CI contention is non-stationary at that scale, so
they never share the instantaneous contention the normalization assumes. Decision:
shelved — median-of-N alone ships. Run: python3 scripts/repro_mem_anchor.py
"""
import json
import os
import statistics as st

HERE = os.path.dirname(os.path.abspath(__file__))
FIX = os.path.join(HERE, "testdata", "mem_anchor_sample.json")


def _cov(a, b):
    ma, mb = st.mean(a), st.mean(b)
    return sum((x - ma) * (y - mb) for x, y in zip(a, b)) / len(a)


def main():
    d = json.load(open(FIX))
    bud = d["budget"]
    raw_worst = max(abs(r["median"]) for r in d["rows"])
    mem_worst = max(abs(r["median"]) for r in d["nrows"])
    # per-cycle mem-anchor swing (head vs base); anchors = (cpu_b, cpu_h, mem_b, mem_h)
    mem_delta = [(h / b - 1) * 100 for (_, _, b, h) in d["anchors"] if b and h]
    var_m = _cov(mem_delta, mem_delta)
    betas = [_cov(r["ds"], mem_delta) / var_m for r in d["rows"]
             if len(r["ds"]) == len(mem_delta) and var_m]

    print(f"captured no-op run (N={d['n']}, budget={bud}%):")
    print(f"  mem-anchor per-cycle swing: [{min(mem_delta):+.1f}, {max(mem_delta):+.1f}]%")
    print(f"  worst |median|:  raw {raw_worst:.2f}%  ->  divide-by-mem {mem_worst:.2f}%")
    print(f"  per-family beta (family-swing vs anchor-swing): "
          f"[{min(betas):+.2f}, {max(betas):+.2f}], median {st.median(betas):+.2f}")

    # Assert the negative result reproduces, so it stays honest if anyone reruns.
    assert mem_worst > raw_worst, "expected divide-by-mem to inflate the worst |median|"
    assert mem_worst > bud, "expected divide-by-mem worst to exceed budget (injected phantom)"
    assert abs(st.median(betas)) < 0.5, "expected near-zero median beta (no co-variation)"
    print("\nREPRODUCED: divide-by-mem inflates the worst |median| past budget and beta "
          "is near-zero — no per-cycle co-variation. Memory-aware anchor shelved; "
          "median-of-N alone ships.")


if __name__ == "__main__":
    main()
