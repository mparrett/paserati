package main

import (
	"fmt"
	"sort"
)

// HOW -count REPETITIONS COLLAPSE TO ONE NUMBER
//
// The reducer is the single most consequential choice in this tool and it is
// selectable rather than hardcoded, because the right answer depends on the
// shape of the distribution and that shape is an empirical question per project.
//
// paserati's default stays MIN. Measured over this corpus the distribution is a
// tight core plus a strictly ONE-SIDED slow tail: ~2 launches in 20 land
// +15–29% high, none low. With no fast outliers to guard against, the minimum
// is the closest available estimator of the core, and every robust alternative
// is strictly further from it. See project-docs/docs/paserati/
// perf-session-microopt-results.md.
//
// Whatever is chosen is written into method.reducer by the code that does the
// reducing, not asserted by the caller — the two drift otherwise, and a
// snapshot whose recorded protocol disagrees with its actual one is worse than
// no provenance at all because it is trusted.
const (
	reducerMin          = "min"
	reducerWarmupMedian = "warmup-median"
	defaultReducer      = reducerMin
)

var reducers = map[string]func([]float64) float64{
	reducerMin:          reduceMin,
	reducerWarmupMedian: reduceWarmupMedian,
}

func reducerNames() []string {
	out := make([]string, 0, len(reducers))
	for k := range reducers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lookupReducer(name string) (func([]float64) float64, error) {
	fn, ok := reducers[name]
	if !ok {
		return nil, fmt.Errorf("unknown reducer %q (want one of %v)", name, reducerNames())
	}
	return fn, nil
}

func reduceMin(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

// reduceWarmupMedian is ported from let-go (nooga/let-go#561), which adopted it
// after a cold first rep in a bimodal anchor distribution turned real
// improvements into reported regressions of +38%/+31%/+17%.
//
// It discards the first repetition as warmup and takes the median of the rest —
// EXCEPT at exactly three reps, where all three are kept. That case is not an
// oversight: discarding from three leaves two, and the "median" of two is their
// mean, which reintroduces exactly the sensitivity to a single spike that the
// median was chosen to avoid. A median of three tolerates one bad rep; a mean
// of two does not.
//
// vals must be in CAPTURE ORDER — the whole mechanism is positional.
//
// Note for paserati specifically: the in-process climb this mitigates is
// benchmark-dependent and was NOT observed on Arith, and the hypothesis that
// -count process sharing drives our instability was refuted by experiment E7.
// It is offered for comparison against min on the retained samples, not as a
// replacement for it.
func reduceWarmupMedian(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	if len(vals) > 3 || len(vals) == 2 {
		vals = vals[1:]
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}
