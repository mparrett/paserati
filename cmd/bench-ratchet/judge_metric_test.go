package main

import "testing"

// The micro family is judged on raw ns/op and the macro family on
// ratio_to_anchor (A10). The case that separates the two policies is a
// benchmark that got FASTER in wall time while its anchor ratio got WORSE,
// which happens when the anchor itself drifts — and the anchor's whole
// resolution is one ulp of a four-digit ~1.1ns value, 0.0923%, so it drifts by
// quantisation alone on benchmarks tighter than that.
//
// Judged on the ratio, such a benchmark reads as a regression and the ratchet
// refuses to tighten. Judged on ns/op it reads as the improvement it is.
func TestMicroJudgedOnNsPerOpNotAnchorRatio(t *testing.T) {
	const (
		micro = "github.com/nooga/paserati/pkg/vm.BenchmarkToInteger"
		macro = "github.com/nooga/paserati/tests.BenchmarkFactorial"
	)

	// Both families: ns/op improves 100 -> 90, ratio worsens 1.00 -> 1.05.
	base := Baseline{Benchmarks: map[string]BenchmarkEntry{
		micro: {NSPerOp: 100, RatioToAnchor: 1.00},
		macro: {NSPerOp: 100, RatioToAnchor: 1.00},
	}}
	cur := Baseline{Benchmarks: map[string]BenchmarkEntry{
		micro: {NSPerOp: 90, RatioToAnchor: 1.05},
		macro: {NSPerOp: 90, RatioToAnchor: 1.05},
	}}

	merged, summary := ratchetMerge(base, cur)

	if got := merged.Benchmarks[micro].NSPerOp; got != 90 {
		t.Errorf("micro NSPerOp = %.1f, want 90 (judged on ns/op, so it tightens)", got)
	}
	if got := merged.Benchmarks[macro].RatioToAnchor; got != 1.00 {
		t.Errorf("macro RatioToAnchor = %.2f, want 1.00 (judged on the ratio, so it does not tighten)", got)
	}

	var tightened []string
	for _, e := range summary.Tightened {
		tightened = append(tightened, e.Name)
	}
	if len(tightened) != 1 || tightened[0] != micro {
		t.Errorf("tightened = %v, want exactly [%s]", tightened, micro)
	}

	// The ratio is still recorded for micro. Dropping the field would break the
	// stored series; the harm was in judging on it.
	if merged.Benchmarks[micro].RatioToAnchor == 0 {
		t.Error("micro ratio_to_anchor was dropped; it should still be stored, only not judged on")
	}
}

func TestIsMicroClassifiesByPackage(t *testing.T) {
	for name, want := range map[string]bool{
		"github.com/nooga/paserati/pkg/vm.BenchmarkToInteger": true,
		"github.com/nooga/paserati/pkg/vm.BenchmarkGetOwn":    true,
		"github.com/nooga/paserati/tests.BenchmarkFactorial":  false,
		"github.com/nooga/paserati/tests.BenchmarkSetIndex":   false,
	} {
		if got := isMicro(name); got != want {
			t.Errorf("isMicro(%q) = %v, want %v", name, got, want)
		}
	}
}
