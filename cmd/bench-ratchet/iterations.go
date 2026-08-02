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

// unstableIterations returns benchmarks whose b.N MOVED across the reps of a
// single run, worst first.
//
// This is the check that matters most and it is not the floor check. The floor
// asks "is N small"; the confound behind nooga/paserati#48 is "is N the same
// number it was last commit" — because ns/op is a function of N wherever
// iterations share state, so a moving N changes the measurement with the engine
// held fixed.
//
// The two are close to independent, and on this corpus they point at different
// benchmarks. Measured over the 2026-07-31 session: SetIndex sat at b.N=2 for
// all 16 commits — under any sane floor, and the TIGHTEST benchmark in the suite
// at 0.30% MAD/median. MatrixMult ran at b.N 139–261, far above any floor, moved
// in 16 of 16 commits, and was noisier at 0.42%/5.24%. A floor-only check flags
// the stable one and stays silent on the unstable one.
//
// Yes, this is max/min, which is banned for TIMINGS in this project — it is set
// by the single worst observation and grows with n, and it manufactured an
// entire false finding. The ban does not extend here: b.N is a control input,
// not a sampled quantity, and the question asked of it is literally "did it
// move", for which the extremes are the whole answer rather than a bad summary
// of a distribution.
func unstableIterations(b Baseline, tolerance float64) []iterationFloor {
	var out []iterationFloor
	for name, entry := range b.Benchmarks {
		if len(entry.Samples) < 2 {
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
		if lo > 0 && float64(hi-lo)/float64(lo) > tolerance {
			out = append(out, iterationFloor{Name: name, Min: lo, Max: hi})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		si := float64(out[i].Max-out[i].Min) / float64(out[i].Min)
		sj := float64(out[j].Max-out[j].Min) / float64(out[j].Min)
		if si != sj {
			return si > sj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// reportIterationHealth writes both b.N warnings. Returns whether anything was
// under the floor, so the caller can decide what that means for its exit code.
// Instability is reported but never fatal: it is a property of the run's
// interaction with the machine, and failing on it would redden a lane for a
// condition -pins is the fix for.
func reportIterationHealth(w io.Writer, b Baseline, floor int64, tolerance float64) bool {
	under := reportIterationFloor(w, b, floor)

	if u := unstableIterations(b, tolerance); len(u) > 0 {
		fmt.Fprintf(w, "\n::warning::%d benchmark(s) had b.N MOVE across reps — ns/op is N-dependent, so this is the measurement changing, not the engine\n", len(u))
		for _, f := range u {
			fmt.Fprintf(w, "  b.N %d–%d (%+.0f%%)  %s\n",
				f.Min, f.Max, float64(f.Max-f.Min)/float64(f.Min)*100, f.Name)
		}
		fmt.Fprintf(w, "  pin these with -pins to make the comparison protocol constant across commits\n\n")
	}
	return under
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
