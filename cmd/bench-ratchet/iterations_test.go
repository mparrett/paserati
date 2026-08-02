package main

import (
	"bytes"
	"strings"
	"testing"
)

func withSamples(iters ...int64) BenchmarkEntry {
	e := BenchmarkEntry{NSPerOp: 100}
	for _, n := range iters {
		e.Samples = append(e.Samples, BenchmarkSample{Iterations: n, NSPerOp: 100})
	}
	return e
}

// The floor judges on the MINIMUM observed b.N, not the mean: one rep at N=1
// contributes an unaveraged reading to the reduction however healthy its
// siblings are. A mean would hide exactly the case worth catching.
func TestIterationFloorJudgesOnMinimum(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{
		"pkg.Healthy": withSamples(200, 210, 205),
		"pkg.Mixed":   withSamples(1, 400, 400), // mean 267, min 1
	}}
	v := iterationFloorViolations(b, 20)
	if len(v) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(v), v)
	}
	if v[0].Name != "pkg.Mixed" || v[0].Min != 1 || v[0].Max != 400 {
		t.Fatalf("want pkg.Mixed 1–400, got %+v", v[0])
	}
}

// Worst first — a reader scanning the top of the block should see the benchmark
// with the least resolution.
func TestIterationFloorOrdersWorstFirst(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{
		"pkg.Eight": withSamples(8),
		"pkg.One":   withSamples(1),
		"pkg.Four":  withSamples(4),
	}}
	v := iterationFloorViolations(b, 20)
	got := []string{v[0].Name, v[1].Name, v[2].Name}
	want := []string{"pkg.One", "pkg.Four", "pkg.Eight"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

// A merged snapshot can legitimately carry a summary whose samples live
// elsewhere. Reporting that as b.N=0 would be a fabricated violation, and a
// warning that cries wolf is one the reader learns to skip — which is the exact
// failure this check exists to correct.
func TestIterationFloorSkipsEntriesWithoutSamples(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{
		"pkg.SummaryOnly": {NSPerOp: 100},
	}}
	if v := iterationFloorViolations(b, 20); len(v) != 0 {
		t.Fatalf("want no violations for a sampleless entry, got %+v", v)
	}
}

func TestIterationFloorDisabledAtZero(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{"pkg.One": withSamples(1)}}
	if v := iterationFloorViolations(b, 0); len(v) != 0 {
		t.Fatalf("floor 0 must disable the check, got %+v", v)
	}
}

// The resolution figure is the point of the message: 1/N is the smallest change
// the number can express, which is what a reader compares against the delta
// being claimed.
func TestReportIterationFloorStatesResolution(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{"pkg.One": withSamples(1)}}
	var buf bytes.Buffer
	if !reportIterationFloor(&buf, b, 20) {
		t.Fatal("want true when a benchmark is under the floor")
	}
	out := buf.String()
	for _, want := range []string{"pkg.One", "b.N 1", "resolution ~100.0%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}

func TestReportIterationFloorSilentWhenClean(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{"pkg.Fine": withSamples(500)}}
	var buf bytes.Buffer
	if reportIterationFloor(&buf, b, 20) {
		t.Fatal("want false when every benchmark clears the floor")
	}
	if buf.Len() != 0 {
		t.Fatalf("want no output when clean, got:\n%s", buf.String())
	}
}
