package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func fakeLister(byPkg map[string][]string) func(string, string) ([]string, error) {
	return func(pkg, _ string) ([]string, error) { return byPkg[pkg], nil }
}

// The whole point of pinning: a benchmark must be measured at exactly one
// benchtime. Two invocations covering the same benchmark would merge two
// protocols into one reduction — worse than not pinning at all.
func TestPlanJobsCoversEachBenchmarkExactlyOnce(t *testing.T) {
	names := []string{"BenchmarkAdd", "BenchmarkArith", "BenchmarkMatrixMult", "BenchmarkFib"}
	jobs, _, err := planJobs([]string{"p"}, "", nil,
		map[string]string{"BenchmarkAdd": "32x", "BenchmarkFib": "32x", "BenchmarkArith": "64x"},
		"1s", fakeLister(map[string][]string{"p": names}))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		hits := 0
		for _, j := range jobs {
			if j.filter.MatchString(n) {
				hits++
			}
		}
		if hits != 1 {
			t.Fatalf("%s matched %d jobs, want exactly 1", n, hits)
		}
	}
}

func TestPlanJobsGroupsByBenchtime(t *testing.T) {
	jobs, _, err := planJobs([]string{"p"}, "", nil,
		map[string]string{"BenchmarkAdd": "32x", "BenchmarkFib": "32x"},
		"1s", fakeLister(map[string][]string{"p": {"BenchmarkAdd", "BenchmarkFib", "BenchmarkOther"}}))
	if err != nil {
		t.Fatal(err)
	}
	// Two distinct benchtimes → two invocations, not one per benchmark.
	if len(jobs) != 2 {
		t.Fatalf("want 2 jobs (32x, 1s), got %d", len(jobs))
	}
	got := map[string]bool{}
	for _, j := range jobs {
		got[j.benchtime] = true
	}
	if !got["32x"] || !got["1s"] {
		t.Fatalf("want benchtimes 32x and 1s, got %v", got)
	}
}

// A pin that stops matching — a renamed benchmark, a narrowed package — must be
// reported. Silently dropping it measures at the default N while the run still
// records itself as pinned.
func TestPlanJobsReportsUnmatchedPins(t *testing.T) {
	_, unmatched, err := planJobs([]string{"p"}, "", nil,
		map[string]string{"BenchmarkAdd": "32x", "BenchmarkRenamedAway": "32x"},
		"1s", fakeLister(map[string][]string{"p": {"BenchmarkAdd"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(unmatched) != 1 || unmatched[0] != "BenchmarkRenamedAway" {
		t.Fatalf("want [BenchmarkRenamedAway], got %v", unmatched)
	}
}

// -filter must still narrow the set when pins are in play, and a pin for a
// benchmark the filter excluded is genuinely unmatched rather than applied.
func TestPlanJobsHonoursFilter(t *testing.T) {
	jobs, unmatched, err := planJobs([]string{"p"}, "", regexp.MustCompile("^BenchmarkAdd$"),
		map[string]string{"BenchmarkAdd": "32x", "BenchmarkFib": "64x"},
		"1s", fakeLister(map[string][]string{"p": {"BenchmarkAdd", "BenchmarkFib"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].benchtime != "32x" {
		t.Fatalf("want a single 32x job, got %+v", jobs)
	}
	if jobs[0].filter.MatchString("BenchmarkFib") {
		t.Fatal("filtered-out benchmark still selected")
	}
	if len(unmatched) != 1 || unmatched[0] != "BenchmarkFib" {
		t.Fatalf("want BenchmarkFib reported unmatched, got %v", unmatched)
	}
}

// The alternation filter must be anchored: a pin for BenchmarkAdd must not drag
// in BenchmarkAddIndexed, which would measure it at a b.N nobody calibrated.
func TestPlanJobsFilterIsAnchored(t *testing.T) {
	jobs, _, err := planJobs([]string{"p"}, "", nil,
		map[string]string{"BenchmarkAdd": "32x"},
		"1s", fakeLister(map[string][]string{"p": {"BenchmarkAdd", "BenchmarkAddIndexed"}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.benchtime == "32x" && j.filter.MatchString("BenchmarkAddIndexed") {
			t.Fatalf("pin for BenchmarkAdd also selected BenchmarkAddIndexed (%s)", j.filter)
		}
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pins.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A null suggested_n is the calibration declining to recommend a pin. Defaulting
// it would invent the pin the null exists to withhold.
func TestLoadPinsSkipsNullSuggestions(t *testing.T) {
	p := writeTemp(t, `{"curves":[
	  {"benchmark":"BenchmarkAdd","suggested_n":32},
	  {"benchmark":"BenchmarkArith","suggested_n":null}]}`)
	pins, err := loadPins(p)
	if err != nil {
		t.Fatal(err)
	}
	if pins["BenchmarkAdd"] != "32x" {
		t.Fatalf("want BenchmarkAdd=32x, got %q", pins["BenchmarkAdd"])
	}
	if _, ok := pins["BenchmarkArith"]; ok {
		t.Fatal("a null suggestion must not become a pin")
	}
}

func TestLoadPinsAcceptsPlainMap(t *testing.T) {
	pins, err := loadPins(writeTemp(t, `{"BenchmarkAdd":"32x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if pins["BenchmarkAdd"] != "32x" {
		t.Fatalf("got %v", pins)
	}
}

func TestLoadPinsRejectsGarbage(t *testing.T) {
	if _, err := loadPins(writeTemp(t, `["not", "a", "table"]`)); err == nil {
		t.Fatal("want an error for a file that is neither form")
	}
}

// A pin naming a benchmark this commit does not have was never applied, so it
// must not survive into the recorded protocol. Two snapshots claiming identical
// pin tables while one ran at the global -benchtime is exactly the confound the
// pin table exists to remove.
func TestAppliedPinsDropsUnmatched(t *testing.T) {
	pins := map[string]string{"BenchmarkAdd": "8x", "BenchmarkGone": "32x"}
	got := appliedPins(pins, []string{"BenchmarkGone"})

	if _, ok := got["BenchmarkGone"]; ok {
		t.Errorf("unmatched pin recorded as applied: %v", got)
	}
	if got["BenchmarkAdd"] != "8x" {
		t.Errorf("matched pin lost: %v", got)
	}
	// The caller's table is provenance for what was asked; do not mutate it.
	if len(pins) != 2 {
		t.Errorf("requested table mutated: %v", pins)
	}
}

func TestAppliedPinsKeepsEverythingWhenAllMatch(t *testing.T) {
	pins := map[string]string{"BenchmarkAdd": "8x"}
	if got := appliedPins(pins, nil); len(got) != 1 || got["BenchmarkAdd"] != "8x" {
		t.Errorf("appliedPins(all matched) = %v, want the table unchanged", got)
	}
}

// All pins missing means nothing was pinned, and `omitempty` then drops the
// field. That reads as an unpinned run, which is what it was.
func TestAppliedPinsEmptyWhenNoneMatch(t *testing.T) {
	pins := map[string]string{"BenchmarkGone": "32x"}
	if got := appliedPins(pins, []string{"BenchmarkGone"}); len(got) != 0 {
		t.Errorf("appliedPins(none matched) = %v, want empty", got)
	}
}

// The anchor is the denominator of every ratio in the corpus. Pinning it to a
// b.N other than the one the historical snapshots were taken at shifts the whole
// timeline against itself, so the table is rejected rather than warned about.
func TestPinsRejectAnchor(t *testing.T) {
	err := pinsRejectAnchor(map[string]string{"BenchmarkAdd": "8x", anchorName: "1000000x"})
	if err == nil {
		t.Fatal("pinning the anchor was accepted; it must be refused")
	}
	if !strings.Contains(err.Error(), anchorName) {
		t.Errorf("error should name the offending pin, got: %v", err)
	}
}

func TestPinsAllowEverythingElse(t *testing.T) {
	if err := pinsRejectAnchor(map[string]string{
		"BenchmarkGetOwn": "70000000x", "BenchmarkIsObject": "90000000x",
	}); err != nil {
		t.Errorf("ordinary pins rejected: %v", err)
	}
}

// bench-calibrate auto-discovers by RUNNING the benchmarks, where go test
// prints one line per subtest and none for the parent. Those keys match nothing
// here — -list and -bench both work in top-level names — so the benchmark ran
// unpinned while the table read as pinned. Collapsing is what makes the
// calibrator's output consumable by the tool that reads it.
func TestCollapseSubtestPinsFoldsOntoParent(t *testing.T) {
	got := collapseSubtestPins(map[string]string{
		"BenchmarkGetOwn/n=1/first": "10000000x",
		"BenchmarkGetOwn/n=1/last":  "10000000x",
		"BenchmarkGetOwn/n=64/last": "10000000x",
		"BenchmarkAdd":              "64x",
	})
	if got["BenchmarkGetOwn"] != "10000000x" {
		t.Errorf("subtests did not fold onto the parent: %v", got)
	}
	if got["BenchmarkAdd"] != "64x" {
		t.Errorf("top-level pin lost: %v", got)
	}
	if _, ok := got["BenchmarkGetOwn/n=1/first"]; ok {
		t.Errorf("subtest key survived, it will match nothing: %v", got)
	}
}

// Subtests disagree when their calibration curves differ. The most common
// suggestion wins; a tie takes the smaller N so a disagreement cannot silently
// inflate runtime.
func TestCollapseSubtestPinsVotesAndBreaksTiesLow(t *testing.T) {
	got := collapseSubtestPins(map[string]string{
		"BenchmarkX/a": "32x", "BenchmarkX/b": "32x", "BenchmarkX/c": "64x",
	})
	if got["BenchmarkX"] != "32x" {
		t.Errorf("majority vote lost, got %q", got["BenchmarkX"])
	}

	tie := collapseSubtestPins(map[string]string{
		"BenchmarkY/a": "128x", "BenchmarkY/b": "16x",
	})
	if tie["BenchmarkY"] != "16x" {
		t.Errorf("tie should break toward the smaller N, got %q", tie["BenchmarkY"])
	}
}

// An explicit top-level entry is a deliberate choice and outranks anything the
// subtests would have voted for.
func TestCollapseSubtestPinsExplicitParentWins(t *testing.T) {
	got := collapseSubtestPins(map[string]string{
		"BenchmarkZ":   "8x",
		"BenchmarkZ/a": "4096x", "BenchmarkZ/b": "4096x",
	})
	if got["BenchmarkZ"] != "8x" {
		t.Errorf("explicit parent pin was overridden, got %q", got["BenchmarkZ"])
	}
}
