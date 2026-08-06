#!/usr/bin/env python3
"""Rename a benchmark key across perf-data timeline snapshots, in place.

    perf-rename-benchmark.py -d timeline OLD NEW      # rewrite
    perf-rename-benchmark.py -d timeline --dry-run    # report, write nothing
    perf-rename-benchmark.py -d timeline --verify     # exit 1 unless converged

WHY THIS EXISTS

Renaming a benchmark strands its history. bench-ratchet keeps a vanished
benchmark as Missing rather than releasing the ratchet, pins.go warns that a
renamed benchmark silently stops matching its pin, and the timeline is keyed on
the name — so without a remap, a rename reads as one benchmark dying and another
being born with no past.

BenchmarkFibPlaceholderRun -> BenchmarkFactorial is the first case (nooga#53),
but the corpus will keep moving, so this takes the pair as arguments rather than
hardcoding it.

WHY IT EDITS TEXT RATHER THAN RE-SERIALIZING

Parsing and re-emitting looks cleaner and is wrong here. These files were not all
written by the same code: the test262 backfill left `"anchor_ns_per_op":
1.248000` in at least one, where a JSON re-emit produces `1.248`. Same value,
different bytes — so a re-serialize silently reformats unrelated numbers and
buries the rename in churn.

Replacing the key string leaves every other byte alone, so the diff is one line
per occurrence and nothing else. Correctness is then checked by parsing BOTH
sides and asserting the documents are equal once the key is accounted for, which
catches a replacement landing anywhere it should not have.

Idempotent: a file already carrying the new name is left byte-for-byte alone, so
running twice is indistinguishable from running once.
"""

import argparse
import json
import pathlib
import sys


def renamed_doc(doc, old: str, new: str):
    """Return doc with the benchmark key renamed, preserving key order."""
    out = json.loads(json.dumps(doc))
    for machine in out.get("machines", {}).values():
        benches = machine.get("benchmarks", {})
        if old not in benches:
            continue
        if new in benches:
            raise ValueError(f"both {old!r} and {new!r} are present — refusing to merge")
        items = [(new if k == old else k, v) for k, v in benches.items()]
        benches.clear()
        benches.update(items)
    return out


def process(path: pathlib.Path, old: str, new: str):
    raw = path.read_bytes()
    doc = json.loads(raw)
    # The key as it appears in the file, quoted. Matching the bare name could hit
    # a substring of a longer benchmark name; the closing quote prevents that.
    needle = f'"{old}"'.encode()
    count = raw.count(needle)
    if count == 0:
        return raw, raw, 0
    body = raw.replace(needle, f'"{new}"'.encode())
    # Structural check: the rewritten bytes must parse to exactly the document we
    # would have built by renaming the key, and nothing else may have moved.
    if json.loads(body) != renamed_doc(doc, old, new):
        raise ValueError("textual rename did not match the structural rename")
    return raw, body, count


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("old", nargs="?", default="github.com/nooga/paserati/tests.BenchmarkFibPlaceholderRun")
    ap.add_argument("new", nargs="?", default="github.com/nooga/paserati/tests.BenchmarkFactorial")
    ap.add_argument("-d", "--dir", default="timeline")
    ap.add_argument("-n", "--dry-run", action="store_true")
    ap.add_argument("--verify", action="store_true", help="exit 1 unless already converged")
    args = ap.parse_args()

    root = pathlib.Path(args.dir)
    if not root.is_dir():
        print(f"perf-rename-benchmark: no such directory: {root}", file=sys.stderr)
        return 2

    files = sorted(root.rglob("*.json"))
    if not files:
        print(f"perf-rename-benchmark: no snapshots under {root}", file=sys.stderr)
        return 2

    changed = untouched = 0
    for f in files:
        try:
            raw, body, count = process(f, args.old, args.new)
        except ValueError as e:
            print(f"perf-rename-benchmark: {f}: {e}", file=sys.stderr)
            return 1
        if count == 0:
            untouched += 1
            continue
        changed += 1
        if args.verify:
            print(f"  would change {f}", file=sys.stderr)
            continue
        if args.dry_run:
            print(f"  {f}  ({count} occurrence(s))")
            continue
        # The rename is the only thing that may move: every other byte, including
        # number formatting this script did not write, stays put.
        expected = count * (len(args.new) - len(args.old))
        if len(body) - len(raw) != expected:
            print(f"perf-rename-benchmark: {f}: size moved unexpectedly", file=sys.stderr)
            return 1
        f.write_bytes(body)

    if args.verify:
        if changed:
            print(f"perf-rename-benchmark: {changed} file(s) still carry {args.old}", file=sys.stderr)
            return 1
        print(f"perf-rename-benchmark: converged — {untouched} file(s) already renamed")
        return 0

    verb = "would rename" if args.dry_run else "renamed"
    print(f"perf-rename-benchmark: {verb} in {changed} file(s); {untouched} untouched")
    return 0


if __name__ == "__main__":
    sys.exit(main())
