package main

import (
	"fmt"
	"io"
	"sort"
)

// THE b.N FLOOR CHECK
//
// `go test -bench` picks b.N to fill -benchtime, so N is an OUTPUT of the
// measurement: N ≈ benchtime / per-op cost. A benchmark whose per-op cost is
// large enough gets a tiny N, and its ns/op is then quantised to 1-in-N — at
// N=1 the reported figure is a single wall-clock reading with no averaging in
// it at all. A metric quantised to 1-in-8 cannot honestly report the 1–5%
// changes the perf lanes exist to detect (nooga/paserati#48).
//
// The data to catch this has been recorded in every sample since the tool was
// written; nothing ever read it. Four benchmarks in ./tests ran at N ≤ 8 for
// more than a year — BenchmarkFibPlaceholderRun at N=1–2 — and it was found by
// accident while chasing something else. This check exists so the fifth one is
// caught by the machine on its first run instead.
//
// It is deliberately a WARNING by default. Four benchmarks are under the floor
// today, so failing would turn every perf lane red on a condition nobody has
// had a chance to fix yet; -strict-iterations opts into the hard failure once
// the floor is actually met. The warning is unconditional, because the whole
// failure mode here was a true fact sitting unread in a file.
const defaultMinIterations = 20

// iterationFloor is one benchmark's observed b.N range across its samples.
type iterationFloor struct {
	Name     string
	Min, Max int64
}

// iterationFloorViolations returns the benchmarks whose LOWEST observed b.N
// falls under floor, worst first.
//
// It judges on the minimum rather than the mean because the harm is per-sample:
// one rep at N=1 contributes an unaveraged reading to the reduction no matter
// how healthy its siblings are. Entries carrying no samples are skipped rather
// than reported as zero — a snapshot merged from rounds may legitimately hold a
// summary with its samples elsewhere, and inventing a violation for missing
// provenance would train the reader to ignore this.
func iterationFloorViolations(b Baseline, floor int64) []iterationFloor {
	var out []iterationFloor
	for name, entry := range b.Benchmarks {
		if len(entry.Samples) == 0 {
			continue
		}
		lo, hi := entry.Samples[0].Iterations, entry.Samples[0].Iterations
		for _, s := range entry.Samples[1:] {
			if s.Iterations < lo {
				lo = s.Iterations
			}
			if s.Iterations > hi {
				hi = s.Iterations
			}
		}
		if lo < floor {
			out = append(out, iterationFloor{Name: name, Min: lo, Max: hi})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Min != out[j].Min {
			return out[i].Min < out[j].Min
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// reportIterationFloor writes the warning block. Returns whether anything was
// under the floor, so the caller can decide what that means for its exit code.
func reportIterationFloor(w io.Writer, b Baseline, floor int64) bool {
	v := iterationFloorViolations(b, floor)
	if len(v) == 0 {
		return false
	}
	fmt.Fprintf(w, "\n::warning::%d benchmark(s) ran below the b.N floor of %d — their ns/op is quantised\n", len(v), floor)
	for _, f := range v {
		rng := fmt.Sprintf("%d", f.Min)
		if f.Max != f.Min {
			rng = fmt.Sprintf("%d–%d", f.Min, f.Max)
		}
		// A resolution figure beats a raw count: 1/N is the smallest change the
		// number can express, which is the quantity to compare against the
		// deltas being claimed.
		fmt.Fprintf(w, "  b.N %-8s (resolution ~%.1f%%)  %s\n", rng, 100.0/float64(f.Min), f.Name)
	}
	fmt.Fprintf(w, "  raise -benchtime, or pin the count with -benchtime Nx; see scripts/bench-calibrate.sh\n\n")
	return true
}
