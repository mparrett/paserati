package main

import (
	"os"
	"testing"
)

func TestReduceMin(t *testing.T) {
	if got := reduceMin([]float64{3, 1, 2}); got != 1 {
		t.Fatalf("want 1, got %v", got)
	}
	if got := reduceMin(nil); got != 0 {
		t.Fatalf("want 0 for no samples, got %v", got)
	}
}

// The count-3 case is the whole subtlety of the ported reducer: discarding a
// warmup from three reps leaves two, and the median of two is their mean, which
// is exactly as spike-sensitive as the mean this reducer exists to replace. So
// three reps keep all three.
func TestReduceWarmupMedianKeepsAllThreeAtCountThree(t *testing.T) {
	// Capture order: warm-ish, spike, warm. Keeping all three medians to 100.
	// Discarding the first would average 500 and 100 into 300.
	if got := reduceWarmupMedian([]float64{100, 500, 100}); got != 100 {
		t.Fatalf("want median 100 with all three kept, got %v", got)
	}
}

func TestReduceWarmupMedianBoundaries(t *testing.T) {
	cases := []struct {
		name string
		in   []float64
		want float64
	}{
		// One rep: nothing to discard, nothing to median.
		{"n=1 passes through", []float64{42}, 42},
		// Two reps: the cold one goes; a single warm sample beats averaging the
		// cold rep in.
		{"n=2 discards warmup", []float64{999, 100}, 100},
		{"n=3 keeps all", []float64{300, 100, 200}, 200},
		// Four reps: discard first, median of the remaining three.
		{"n=4 discards then medians", []float64{999, 30, 10, 20}, 20},
		// Five: discard first, median of four = mean of middle two.
		{"n=5 discards then medians four", []float64{999, 10, 20, 30, 40}, 25},
		{"empty", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reduceWarmupMedian(c.in); got != c.want {
				t.Fatalf("want %v, got %v", c.want, got)
			}
		})
	}
}

// The reducer is positional, so it must not sort in place and corrupt the
// caller's capture order — the samples slice is retained in the snapshot.
func TestReduceWarmupMedianDoesNotMutateInput(t *testing.T) {
	in := []float64{999, 30, 10, 20}
	orig := append([]float64(nil), in...)
	reduceWarmupMedian(in)
	for i := range orig {
		if in[i] != orig[i] {
			t.Fatalf("input mutated at %d: %v vs %v", i, in, orig)
		}
	}
}

func TestLookupReducerRejectsUnknown(t *testing.T) {
	if _, err := lookupReducer("mean"); err == nil {
		t.Fatal("want an error for an unknown reducer")
	}
	for _, n := range []string{reducerMin, reducerWarmupMedian} {
		if _, err := lookupReducer(n); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
}

// method.reducer must name what was actually applied. A snapshot claiming "min"
// while a median was used is the drift the provenance exists to prevent.
func TestAggregateRecordsTheReducerItApplied(t *testing.T) {
	jsonl := `{"package":"p","name":"BenchmarkX","iterations":10,"ns_per_op":100.0,"bytes_per_op":0,"allocs_per_op":0,"captured_at":"2026-08-01T00:00:00Z"}
`
	for _, want := range []string{reducerMin, reducerWarmupMedian} {
		dir := t.TempDir() + "/r.jsonl"
		writeFile(t, dir, anchorJSONL+jsonl)
		base, err := aggregateFromFile(dir, want)
		if err != nil {
			t.Fatal(err)
		}
		if base.Method.Reducer != want {
			t.Fatalf("want method.reducer %q, got %q", want, base.Method.Reducer)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
