package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAggregateFromFileUsesMin verifies that multiple -count repetitions of the
// same benchmark are reduced by minimum, not mean — the fastest sample is the
// least noise-contaminated estimate. A mean reducer here would report 115.
func TestAggregateFromFileUsesMin(t *testing.T) {
	// anchor: three clean samples; noisy: one fast + two slower (upward noise).
	jsonl := `{"package":"github.com/nooga/paserati/pkg/vm","name":"BenchmarkRatchetAnchor","iterations":1000,"ns_per_op":1.0,"bytes_per_op":0,"allocs_per_op":0,"captured_at":"2026-07-11T00:00:00Z"}
{"package":"github.com/nooga/paserati/pkg/vm","name":"BenchmarkRatchetAnchor","iterations":1000,"ns_per_op":1.0,"bytes_per_op":0,"allocs_per_op":0,"captured_at":"2026-07-11T00:00:00Z"}
{"package":"github.com/nooga/paserati/pkg/vm","name":"BenchmarkRatchetAnchor","iterations":1000,"ns_per_op":1.0,"bytes_per_op":0,"allocs_per_op":0,"captured_at":"2026-07-11T00:00:00Z"}
{"package":"github.com/nooga/paserati/pkg/vm","name":"BenchmarkNoisy","iterations":500,"ns_per_op":100.0,"bytes_per_op":8,"allocs_per_op":2,"captured_at":"2026-07-11T00:00:00Z"}
{"package":"github.com/nooga/paserati/pkg/vm","name":"BenchmarkNoisy","iterations":500,"ns_per_op":140.0,"bytes_per_op":8,"allocs_per_op":2,"captured_at":"2026-07-11T00:00:00Z"}
{"package":"github.com/nooga/paserati/pkg/vm","name":"BenchmarkNoisy","iterations":500,"ns_per_op":105.0,"bytes_per_op":8,"allocs_per_op":2,"captured_at":"2026-07-11T00:00:00Z"}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	if err := os.WriteFile(path, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	base, err := aggregateFromFile(path)
	if err != nil {
		t.Fatalf("aggregateFromFile: %v", err)
	}

	const key = "github.com/nooga/paserati/pkg/vm.BenchmarkNoisy"
	e, ok := base.Benchmarks[key]
	if !ok {
		t.Fatalf("benchmark %q missing from baseline", key)
	}
	if e.NSPerOp != 100.0 {
		t.Errorf("NSPerOp = %.1f, want 100.0 (min of {100,140,105}; mean would be 115)", e.NSPerOp)
	}
	// Anchor is min-reduced too (all 1.0 here) and normalizes the ratio.
	if e.RatioToAnchor != 100.0 {
		t.Errorf("RatioToAnchor = %.1f, want 100.0", e.RatioToAnchor)
	}
	if e.AllocsPerOp != 2 || e.BytesPerOp != 8 {
		t.Errorf("allocs/bytes = %d/%d, want 2/8", e.AllocsPerOp, e.BytesPerOp)
	}
	// All raw samples retained for provenance.
	if len(e.Samples) != 3 {
		t.Errorf("Samples len = %d, want 3", len(e.Samples))
	}
}
