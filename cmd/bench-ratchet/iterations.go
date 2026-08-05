package main

import (
	"fmt"
	"io"
	"math"
	"sort"
)

// THE b.N FLOOR CHECK
//
// `go test -bench` picks b.N to fill -benchtime, so N is an OUTPUT of the
// measurement: N ≈ benchtime / per-op cost.
//
// WHAT LOW N DOES AND DOES NOT MEAN. This warning used to report a "resolution"
// of 1/N and call the metric quantised. That was wrong, and wrong by nine orders
// of magnitude: Go computes ns/op as elapsed nanoseconds divided by N, so the
// quantum is (1 ns)/N. On BenchmarkFactorial at N=1 that is 1.3e-07% of
// the reported value, not 100%. The old text also shouted loudest exactly where
// quantisation is most irrelevant — a 700 ms/op benchmark — while printing 0.0%
// for a 70 ns/op one with the same absolute quantum.
//
// What low N actually costs is AVERAGING: ns/op is the mean over N iterations,
// so per-iteration variance falls only as sqrt(N), and at N=1 a single slow
// iteration is the whole reading. That is worth flagging, and it is a different
// claim.
//
// It is also NOT the confound behind nooga/paserati#48. That one is N MOVING
// between commits while per-iteration cost depends on N — see
// iterationProtocolDrift, which needs two snapshots to see it.
//
// The data to catch this has been recorded in every sample since the tool was
// written; nothing ever read it. Four benchmarks in ./tests ran at N ≤ 8 for
// more than a year — BenchmarkFactorial at N=1–2 — and it was found by
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

// iterationProtocolDrift compares b.N BETWEEN two snapshots — the confound
// unstableIterations structurally cannot see.
//
// unstableIterations watches b.N move across the reps inside one run. That is a
// real signal, but it is not #48's. A benchmark can sit rock-steady at N=4 in
// the baseline and rock-steady at N=8 today, be silent under every within-run
// check, and still have had its measurement protocol changed underneath the
// comparison — which is precisely the confound, because ns/op is a function of N
// wherever iterations share state.
//
// Seeing it needs two snapshots, so it lives in check mode where a baseline is
// already loaded, not in the single-snapshot path.
func iterationProtocolDrift(baseline, current Baseline, tolerance float64) []iterationDrift {
	var out []iterationDrift
	for name, cur := range current.Benchmarks {
		base, ok := baseline.Benchmarks[name]
		if !ok {
			continue // new benchmark: nothing to compare against, not a drift
		}
		b, c := medianIterations(base), medianIterations(cur)
		if b <= 0 || c <= 0 {
			continue
		}
		lo, hi := b, c
		if lo > hi {
			lo, hi = hi, lo
		}
		if float64(hi-lo)/float64(lo) > tolerance {
			out = append(out, iterationDrift{Name: name, Was: b, Now: c})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].driftAbs() > out[j].driftAbs() ||
			(out[i].driftAbs() == out[j].driftAbs() && out[i].Name < out[j].Name)
	})
	return out
}

type iterationDrift struct {
	Name     string
	Was, Now int64
}

func (d iterationDrift) driftAbs() float64 {
	lo, hi := d.Was, d.Now
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo <= 0 {
		return 0
	}
	return float64(hi-lo) / float64(lo)
}

// medianIterations summarises a snapshot's b.N for one benchmark. Median rather
// than min: here the question is "what N did this run typically use", not "was
// any single rep starved".
func medianIterations(e BenchmarkEntry) int64 {
	if len(e.Samples) == 0 {
		return 0
	}
	v := make([]int64, 0, len(e.Samples))
	for _, s := range e.Samples {
		v = append(v, s.Iterations)
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[len(v)/2]
}

// reportProtocolDrift writes the cross-snapshot warning. Never fatal on its own:
// it describes a protocol difference, and -pins is the fix.
func reportProtocolDrift(w io.Writer, baseline, current Baseline, tolerance float64) bool {
	d := iterationProtocolDrift(baseline, current, tolerance)
	if len(d) == 0 {
		return false
	}
	fmt.Fprintf(w, "\n::warning::%d benchmark(s) ran at a DIFFERENT b.N than the baseline — the comparison spans two protocols\n", len(d))
	for _, x := range d {
		fmt.Fprintf(w, "  b.N %d → %d (%+.0f%%)  %s\n",
			x.Was, x.Now, float64(x.Now-x.Was)/float64(x.Was)*100, x.Name)
	}
	fmt.Fprintf(w, "  ns/op is N-dependent where iterations share state; pin with -pins so both sides use one protocol\n\n")
	return true
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
	fmt.Fprintf(w, "\n::warning::%d benchmark(s) ran below the b.N floor of %d — few iterations to average over\n", len(v), floor)
	for _, f := range v {
		rng := fmt.Sprintf("%d", f.Min)
		if f.Max != f.Min {
			rng = fmt.Sprintf("%d–%d", f.Min, f.Max)
		}
		// sqrt(N) is the honest figure: how much per-iteration variance the mean
		// actually damps. NOT 1/N, which describes a quantisation that does not
		// exist at these magnitudes.
		fmt.Fprintf(w, "  b.N %-8s (variance damped only %.1f×)  %s\n",
			rng, math.Sqrt(float64(f.Min)), f.Name)
	}
	fmt.Fprintf(w, "  raise -benchtime, or pin the count with -benchtime Nx; see scripts/bench-calibrate.sh\n\n")
	return true
}
