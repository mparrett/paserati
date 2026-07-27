#!/usr/bin/env python3
"""Verdict-flip evidence for the median-of-N perf-pr gate (nooga/paserati#21).

Feeds real captured no-op A/B samples through the gate's REAL decision function
(_summarize) and asserts the flip that motivates the gate:

    single-shot (one A/B cycle)  -> at least one family trips the budget (phantom)
    median-of-N                  -> zero families gate

The fixtures in testdata/perf_pr_*.json are same-code runs captured on the
mparrett/paserati fork (base == head), so EVERY gate is by definition a false
positive. Deterministic, no benchmarking. Parallels the min-of-count reducer's
real-data verdict test (nooga/paserati#22).

Run: `python3 scripts/test_ab_repeat.py` (prints the flip) or `pytest scripts/`.
"""
import glob
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ab_repeat import _summarize  # the real gate decision  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
FIXTURES = sorted(glob.glob(os.path.join(HERE, "testdata", "perf_pr_*.json")))


def _load(path):
    d = json.load(open(path))
    return d, {r["fam"]: r["ds"] for r in d["rows"]}


def test_median_of_n_gates_zero_false_positives():
    """On same-code runs, median-of-N must gate nothing (0 false positives)."""
    assert FIXTURES, "no perf_pr_*.json fixtures found"
    for path in FIXTURES:
        d, deltas = _load(path)
        rows = _summarize(deltas, d["budget"], confirm_k=2)
        fps = [r["fam"] for r in rows if r["gate_median"]]
        assert fps == [], f"{os.path.basename(path)}: median-of-N false positives {fps}"


def test_single_shot_would_false_positive():
    """A single A/B cycle DOES trip the budget — the very noise the gate removes.
    Single-shot = each family's most-positive cycle as if it were the lone run."""
    for path in FIXTURES:
        d, deltas = _load(path)
        single = {f: [max(ds)] for f, ds in deltas.items()}
        rows = _summarize(single, d["budget"], confirm_k=2)
        phantoms = [r["fam"] for r in rows if r["gate_median"]]
        assert phantoms, f"{os.path.basename(path)}: expected a single-shot phantom, got none"


def _drive_main(tmpdir, gate, head_delta_pct):
    """Run ab_repeat.main() end-to-end with the benchmarking stubbed out, and
    return its exit status. head_delta_pct is applied to every family on the head
    side, so the verdict is known in advance and only the wiring is under test."""
    import ab_repeat

    def fake_snapshot(worktree, bench_args, out, timeout):
        scale = 1.0 + head_delta_pct / 100.0 if "head" in worktree else 1.0
        return {"github.com/nooga/paserati/pkg/vm.BenchmarkFake": 2.0 * scale}, 1.0

    argv = ["ab_repeat.py", "--base", "/tmp/wt-base", "--head", "/tmp/wt-head",
            "--n", "3", "--budget", "10", "--gate", gate, "--out", tmpdir]
    real_snapshot, real_argv = ab_repeat.snapshot, sys.argv
    try:
        ab_repeat.snapshot, sys.argv = fake_snapshot, argv
        return ab_repeat.main()
    finally:
        ab_repeat.snapshot, sys.argv = real_snapshot, real_argv


def test_gate_none_never_fails():
    """The default must report a regression and still exit 0 — a workflow that
    only wants the report cannot be failed by turning the budget down."""
    import tempfile
    with tempfile.TemporaryDirectory() as d:
        assert _drive_main(d, "none", head_delta_pct=50.0) == 0


def test_gate_median_fails_on_regression():
    """--gate median must actually set the exit status. This is the blocker the
    flag exists to close: the verdict was computed and then discarded, so no
    workflow could fail a PR on it."""
    import tempfile
    with tempfile.TemporaryDirectory() as d:
        assert _drive_main(d, "median", head_delta_pct=50.0) == 1
        assert _drive_main(d, "median", head_delta_pct=0.0) == 0


def _main():
    for path in FIXTURES:
        d, deltas = _load(path)
        med = _summarize(deltas, d["budget"], 2)
        single = _summarize({f: [max(ds)] for f, ds in deltas.items()}, d["budget"], 2)
        mfp = [r["fam"] for r in med if r["gate_median"]]
        sfp = [(r["fam"].split("/")[-1], round(r["median"], 1))
               for r in single if r["gate_median"]]
        print(f"{os.path.basename(path)}  N={d['n']} budget={d['budget']}%:")
        print(f"  single-shot false positives: {len(sfp):2d}  e.g. {sfp[:3]}")
        print(f"  median-of-N false positives: {len(mfp):2d}  {mfp}")
    test_median_of_n_gates_zero_false_positives()
    test_single_shot_would_false_positive()
    test_gate_none_never_fails()
    test_gate_median_fails_on_regression()
    print("\nPASS: single-shot trips the budget on real no-op data; "
          "median-of-N gates 0 on every captured run; --gate none exits 0 on a "
          "regression and --gate median exits 1.")


if __name__ == "__main__":
    _main()
