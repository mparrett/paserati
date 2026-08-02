package main

import (
	"os"
	"path/filepath"
	"regexp"
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
