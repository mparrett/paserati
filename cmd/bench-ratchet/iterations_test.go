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

// This test used to assert "resolution ~100.0%" and so locked in the defect:
// 1/N is not a resolution, and Go's ns/op is not quantised at these magnitudes.
// It now asserts the claim the warning is actually entitled to make.
func TestReportIterationFloorStatesAveraging(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{"pkg.One": withSamples(1)}}
	var buf bytes.Buffer
	if !reportIterationFloor(&buf, b, 20) {
		t.Fatal("want true when a benchmark is under the floor")
	}
	out := buf.String()
	for _, want := range []string{"pkg.One", "b.N 1", "damped only 1.0×"} {
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

// The floor and the instability check are close to independent, and the corpus
// case that motivated this is a benchmark far ABOVE the floor whose b.N moves in
// every commit. A floor-only check is silent on exactly the wrong one.
func TestUnstableIterationsCatchesWhatTheFloorMisses(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{
		"pkg.SetIndex":   withSamples(2, 2, 2),       // under the floor, rock steady
		"pkg.MatrixMult": withSamples(139, 200, 261), // far above the floor, moves 88%
	}}
	if v := iterationFloorViolations(b, 20); len(v) != 1 || v[0].Name != "pkg.SetIndex" {
		t.Fatalf("floor should flag only SetIndex, got %+v", v)
	}
	u := unstableIterations(b, 0.02)
	if len(u) != 1 || u[0].Name != "pkg.MatrixMult" {
		t.Fatalf("instability should flag only MatrixMult, got %+v", u)
	}
}

func TestUnstableIterationsRespectsTolerance(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{
		"pkg.Jitter": withSamples(200, 201), // 0.5%, within a 2% tolerance
	}}
	if u := unstableIterations(b, 0.02); len(u) != 0 {
		t.Fatalf("want no violation inside tolerance, got %+v", u)
	}
	if u := unstableIterations(b, 0.001); len(u) != 1 {
		t.Fatalf("want a violation below tolerance, got %+v", u)
	}
}

// The case codex identified: b.N rock-steady WITHIN each run, but different
// BETWEEN them. Every within-run check is silent, and the comparison still spans
// two protocols — which is #48's actual confound.
func TestProtocolDriftCatchesStableButDifferentAcrossSnapshots(t *testing.T) {
	base := Baseline{Benchmarks: map[string]BenchmarkEntry{
		"pkg.Arith": withSamples(4, 4, 4),
	}}
	cur := Baseline{Benchmarks: map[string]BenchmarkEntry{
		"pkg.Arith": withSamples(8, 8, 8),
	}}
	if u := unstableIterations(cur, 0.02); len(u) != 0 {
		t.Fatalf("within-run check should see nothing here, got %+v", u)
	}
	d := iterationProtocolDrift(base, cur, 0.02)
	if len(d) != 1 || d[0].Was != 4 || d[0].Now != 8 {
		t.Fatalf("want Arith 4→8 reported as drift, got %+v", d)
	}
}

func TestProtocolDriftIgnoresNewAndMatchingBenchmarks(t *testing.T) {
	base := Baseline{Benchmarks: map[string]BenchmarkEntry{"pkg.Same": withSamples(100, 100)}}
	cur := Baseline{Benchmarks: map[string]BenchmarkEntry{
		"pkg.Same": withSamples(100, 101), // 1%, inside tolerance
		"pkg.New":  withSamples(7),        // no baseline to compare against
	}}
	if d := iterationProtocolDrift(base, cur, 0.02); len(d) != 0 {
		t.Fatalf("want no drift, got %+v", d)
	}
}

// The floor warning must not claim a quantisation that does not exist. Go
// computes ns/op as elapsed/N, so the quantum is (1ns)/N — 1.3e-07% at N=1 on a
// 700ms/op benchmark, not 100%.
func TestFloorWarningMakesNoResolutionClaim(t *testing.T) {
	b := Baseline{Benchmarks: map[string]BenchmarkEntry{"pkg.Slow": withSamples(1)}}
	var buf bytes.Buffer
	reportIterationFloor(&buf, b, 20)
	out := buf.String()
	for _, banned := range []string{"resolution", "quantis", "quantiz", "100.0%"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(banned)) {
			t.Fatalf("floor warning still claims %q:\n%s", banned, out)
		}
	}
	if !strings.Contains(out, "damped") {
		t.Fatalf("want the averaging framing, got:\n%s", out)
	}
}
