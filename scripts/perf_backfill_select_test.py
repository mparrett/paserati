#!/usr/bin/env python3
"""Tests for perf-backfill-select.py.

Run: python3 scripts/perf_backfill_select_test.py

The cases that matter are the two the workflow got wrong in the field: taking
the first snapshot file for a commit rather than the one matching the runner
(ccf2da20f14f has four tiers), and treating an already-measured point as work.
"""

import importlib.util
import json
import os
import shutil
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("sel", os.path.join(HERE, "perf-backfill-select.py"))
sel = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sel)

K7763 = "amd64/AMD EPYC 7763 64-Core Processor"
K9V74 = "amd64/AMD EPYC 9V74 80-Core Processor"


class Base(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)

    def snap(self, stamp, short, slug, key, benchmarks=None, v1=False):
        name = f"{stamp}-{short}-{slug}.json" if slug else f"{stamp}-{short}.json"
        body = (
            {"machine": {"arch": key.split("/")[0], "cpu_model": key.split("/", 1)[1]},
             "benchmarks": benchmarks or {}}
            if v1 else
            {"version": 2, "machines": {key: {"benchmarks": benchmarks or {}}}}
        )
        with open(os.path.join(self.dir, name), "w") as fh:
            json.dump(body, fh)


class TestSelection(Base):
    def test_selects_a_target_missing_the_metric_on_this_tier(self):
        self.snap("20260727T205737Z", "3623248df573", "amd64-amd-epyc-7763-64-core-processor", K7763)
        ok, why = sel.needs(self.dir, "3623248df573", K7763, "test262.total")
        self.assertTrue(ok, why)

    def test_skips_a_target_that_already_has_the_metric(self):
        self.snap("20260727T205737Z", "3623248df573", "amd64-amd-epyc-7763-64-core-processor",
                  K7763, {"test262.total": {"ns_per_op": 3.08e6}})
        ok, why = sel.needs(self.dir, "3623248df573", K7763, "test262.total")
        self.assertFalse(ok)
        self.assertIn("already has", why)

    def test_matches_the_runner_not_the_first_file(self):
        """The field bug: a commit holds one file per tier, and `find | head -1`
        picked one at random, refusing while the right file sat beside it."""
        short = "ccf2da20f14f"
        # Deliberately ordered so the wrong tier sorts first.
        self.snap("20260627T114841Z", short, "amd64-amd-epyc-9v74-80-core-processor", K9V74)
        self.snap("20260627T114841Z", short, "amd64-amd-epyc-7763-64-core-processor", K7763)
        ok, why = sel.needs(self.dir, short, K7763, "test262.total")
        self.assertTrue(ok, why)
        self.assertIn("7763", why)

    def test_reports_tiers_present_when_this_runner_has_none(self):
        self.snap("20260627T114841Z", "ccf2da20f14f", "amd64-amd-epyc-9v74-80-core-processor", K9V74)
        ok, why = sel.needs(self.dir, "ccf2da20f14f", K7763, "test262.total")
        self.assertFalse(ok)
        self.assertIn("not on this tier", why)
        self.assertIn("9V74", why)

    def test_no_snapshot_at_all(self):
        ok, why = sel.needs(self.dir, "deadbeefdead", K7763, "test262.total")
        self.assertFalse(ok)
        self.assertIn("no snapshot", why)

    def test_reads_v1_snapshots(self):
        self.snap("20260625T141056Z", "5b19d5a9fbcd", "", K7763, v1=True)
        ok, why = sel.needs(self.dir, "5b19d5a9fbcd", K7763, "test262.total")
        self.assertTrue(ok, why)

    def test_sha_is_matched_as_a_field_not_a_substring(self):
        """A sha that prefixes another must not collect the other's snapshot."""
        self.snap("20260727T205737Z", "3623248df573aaaa"[:12], "amd64-amd-epyc-7763-64-core-processor",
                  K7763, {"test262.total": {"ns_per_op": 1}})
        # A different commit whose short sha merely CONTAINS the query is not a match.
        self.snap("20260727T205738Z", "zz3623248df5", "amd64-amd-epyc-7763-64-core-processor", K7763)
        files = sel.snapshots_for(self.dir, "3623248df573")
        self.assertEqual(len(files), 1, files)

    def test_malformed_snapshot_does_not_crash_selection(self):
        with open(os.path.join(self.dir, "20260727T205737Z-3623248df573-x.json"), "w") as fh:
            fh.write("{not json")
        ok, why = sel.needs(self.dir, "3623248df573", K7763, "test262.total")
        self.assertFalse(ok)


if __name__ == "__main__":
    unittest.main(verbosity=2)
