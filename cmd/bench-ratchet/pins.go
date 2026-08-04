package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// PER-BENCHMARK PINNED b.N
//
// `go test` accepts one -benchtime per invocation, so a single capture measures
// every benchmark under the same rule. That is the wrong rule here: b.N is an
// OUTPUT of the timing loop (N ≈ benchtime / per-op cost), and wherever
// iterations share state, ns/op is a function of N. On ./tests the direction is
// not even common — MatrixMult and Arith get cheaper with N while Add and Fib
// get more expensive — so one -benchtime for all of them slides each benchmark
// a different distance along its own curve.
//
// What a cross-commit comparison needs is not a steady state but a CONSTANT:
// pin N and the amortisation blend stops varying between commits, which is the
// actual confound. scripts/bench-calibrate.sh finds where to pin; this applies
// it, by splitting a package into one `go test` invocation per distinct
// benchtime and giving each an explicit alternation filter.
//
// Every benchmark lands in exactly one group. Running a benchmark twice under
// two benchtimes would merge two protocols into one reduction, which is worse
// than not pinning at all.
//
// Pins are keyed by TOP-LEVEL benchmark name, because that is what `go test
// -list` enumerates and what a -bench filter selects. Subtests inherit their
// parent's pin.

// loadPins reads a pin table. Two forms are accepted: the JSON that
// bench-calibrate.sh -o writes, and a plain {"BenchmarkX": "32x"} map for the
// hand-written case.
//
// A suggested_n of null means the calibration explicitly declined to recommend
// a pin for that benchmark — it is skipped rather than defaulted, because
// inventing a pin the calibration refused to make is the one thing the null is
// there to prevent.
func loadPins(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var calib struct {
		Curves []struct {
			Benchmark  string `json:"benchmark"`
			SuggestedN *int64 `json:"suggested_n"`
		} `json:"curves"`
	}
	if err := json.Unmarshal(raw, &calib); err == nil && calib.Curves != nil {
		pins := map[string]string{}
		for _, c := range calib.Curves {
			if c.SuggestedN != nil && c.Benchmark != "" {
				pins[c.Benchmark] = fmt.Sprintf("%dx", *c.SuggestedN)
			}
		}
		return pins, nil
	}

	var plain map[string]string
	if err := json.Unmarshal(raw, &plain); err != nil {
		return nil, fmt.Errorf("%s: not a bench-calibrate curves file nor a name→benchtime map: %w", path, err)
	}
	return plain, nil
}

// listBenchmarks enumerates a package's top-level benchmarks WITHOUT running
// them. -list compiles the test binary and prints matching names, which costs
// well under a second and has no timing side effects.
func listBenchmarks(pkg, tags string) ([]string, error) {
	args := []string{"test", pkg, "-list", "^Benchmark"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("go test -list %s: %w", pkg, err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// -list prints one name per line plus go test's own "ok <pkg> <time>"
		// trailer, which is not a benchmark.
		if strings.HasPrefix(line, "Benchmark") {
			names = append(names, line)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no benchmarks listed in %s", pkg)
	}
	return names, nil
}

// planJobs splits each package into one job per distinct benchtime.
//
// lister is injected so the planning logic is testable without a Go toolchain.
// Returns the jobs and any pin names that matched nothing — an unmatched pin is
// reported rather than dropped, because a table that silently stops applying
// (a benchmark renamed, a package narrowed) measures at the wrong N while
// reading as if it pinned.
func planJobs(pkgs []string, tags string, filterRE *regexp.Regexp, pins map[string]string,
	defaultBenchtime string, lister func(pkg, tags string) ([]string, error)) ([]captureJob, []string, error) {

	matched := map[string]bool{}
	var jobs []captureJob

	for _, pkg := range pkgs {
		names, err := lister(pkg, tags)
		if err != nil {
			return nil, nil, err
		}

		byBenchtime := map[string][]string{}
		for _, n := range names {
			if filterRE != nil && !filterRE.MatchString(n) {
				continue
			}
			bt := defaultBenchtime
			if p, ok := pins[n]; ok {
				bt, matched[n] = p, true
			}
			byBenchtime[bt] = append(byBenchtime[bt], n)
		}

		bts := make([]string, 0, len(byBenchtime))
		for bt := range byBenchtime {
			bts = append(bts, bt)
		}
		sort.Strings(bts) // deterministic job order across runs

		for _, bt := range bts {
			group := byBenchtime[bt]
			sort.Strings(group)
			re, err := regexp.Compile("^(" + strings.Join(group, "|") + ")$")
			if err != nil {
				return nil, nil, fmt.Errorf("%s: build filter: %w", pkg, err)
			}
			jobs = append(jobs, captureJob{pkg: pkg, tags: tags, filter: re, benchtime: bt})
		}
	}

	var unmatched []string
	for name := range pins {
		if !matched[name] {
			unmatched = append(unmatched, name)
		}
	}
	sort.Strings(unmatched)
	return jobs, unmatched, nil
}

// appliedPins is the requested table minus the pins that matched no benchmark.
// It is what gets recorded as provenance: a pin naming a benchmark this commit
// does not have was never applied, and writing it down anyway would let two
// snapshots claim identical protocols while one of them ran at the global
// -benchtime. Returns a copy; the caller's table is left alone.
// pinsRejectAnchor refuses a pin table that names the calibration anchor.
//
// Every ratio in the corpus is bench_ns / anchor_ns, so the anchor is the one
// benchmark whose measured value must not change for reasons of protocol. Pin
// it at a different b.N from the historical snapshots and every ratio shifts
// together — the whole timeline moves, silently and by an amount nobody
// measured. The anchor stays on the global -benchtime, which is what the corpus
// was built with.
//
// This is a hard error rather than a warning: a warning would be printed once,
// into a session log nobody re-reads, while the corrupted snapshot lives on.
func pinsRejectAnchor(pins map[string]string) error {
	for name := range pins {
		if name == anchorName {
			return fmt.Errorf("pin table names %s: the anchor must stay on the global "+
				"-benchtime, or every ratio_to_anchor in the corpus shifts against it", name)
		}
	}
	return nil
}

func appliedPins(pins map[string]string, unmatched []string) map[string]string {
	if len(unmatched) == 0 {
		return pins
	}
	drop := make(map[string]struct{}, len(unmatched))
	for _, n := range unmatched {
		drop[n] = struct{}{}
	}
	applied := make(map[string]string, len(pins))
	for k, v := range pins {
		if _, skip := drop[k]; !skip {
			applied[k] = v
		}
	}
	// If every pin missed, this returns an empty map and `omitempty` drops the
	// field entirely — the snapshot then reads as an unpinned run. That is the
	// correct reading, not a gap: with no pin applied, every benchmark did run at
	// the global -benchtime. The operator learns pinning was attempted from the
	// per-pin stderr warnings and the scope line, which is where a request that
	// failed belongs; the snapshot records the protocol, not the intent.
	return applied
}
