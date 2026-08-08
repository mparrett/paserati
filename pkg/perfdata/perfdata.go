// Package perfdata defines the JSON schema shared by the perf tools.
package perfdata

// Baseline is the on-disk format. Keep field tags stable.
type Baseline struct {
	Version       int                       `json:"version"`
	CapturedAt    string                    `json:"captured_at"`
	CapturedAtSHA string                    `json:"captured_at_sha"`
	Provenance    Provenance                `json:"provenance,omitempty"`
	Machine       Machine                   `json:"machine"`
	Method        Method                    `json:"method,omitempty"`
	Anchor        Anchor                    `json:"anchor"`
	Benchmarks    map[string]BenchmarkEntry `json:"benchmarks"`
}

// Provenance identifies the CODE a snapshot measured, as opposed to the commit
// it was taken at.
//
// captured_at_sha alone is a dangling reference the moment history is rewritten:
// 14 of the 86 snapshots in this corpus point at commits no longer on main, and
// 11 of those have no counterpart there by patch-id, tree or subject, because
// they were dropped rather than replayed. Nothing can relink those.
//
// BuildHash is the field that makes the loss survivable. It digests only the
// inputs that reach the binary, so a snapshot transfers to any commit that
// builds the same thing — and two commits sharing one BuildHash MUST produce
// the same measurement, which turns the corpus into its own noise floor.
// Measured 2026-08-07: the 86 snapshots span just 32 distinct builds.
//
// PatchID is a lineage hint for the rebase-and-replay case only. It is not an
// identity: a small mechanical patch can collide, so it must never be used to
// decide that two measurements are interchangeable. BuildHash decides that.
type Provenance struct {
	CapturedAtTree string `json:"captured_at_tree,omitempty"`
	BuildHash      string `json:"build_hash,omitempty"`
	PatchID        string `json:"patch_id,omitempty"`
	// BuildManifest records WHICH paths the BuildHash covered. Without it a
	// later manifest change would silently make old and new hashes
	// incomparable while both still look like build hashes.
	BuildManifest string `json:"build_manifest,omitempty"`
}

// Method records HOW a snapshot was measured, so a later reader can tell a
// protocol change from an engine change. Both have moved over this series and
// neither was recoverable: the page could detect a CPU change and a test-set
// change, but a benchtime or reducer change was invisible.
//
// Reducer especially. It is written by the code that does the reducing rather
// than asserted by whoever calls it, because the two drift: the timeline
// workflow carried reducer:"min" as a literal that happened to match
// bench-ratchet, and would have kept claiming "min" unchanged if the default
// moved to mean. Provenance that can disagree with the thing it describes is
// worse than none — it is trusted.
type Method struct {
	Reducer   string `json:"reducer,omitempty"`
	Count     int    `json:"count,omitempty"`
	Benchtime string `json:"benchtime,omitempty"`
	// PerTestTimeout is the macro's equivalent of Benchtime: the per-test bound the
	// Test262 run was given. It decides which slow tests are counted as timeouts and
	// excluded from the timing sum, so two macro measurements taken under different
	// bounds are not comparable in their failed/timeout split. Recorded by the driver
	// that applied it (scripts/macro-test262.sh) rather than by whoever reports it.
	PerTestTimeout string `json:"per_test_timeout,omitempty"`
	// Pins records benchmarks measured at their own -benchtime rather than the
	// run's. Without it Benchtime is a claim the run did not honour: it would
	// name one value while several were used, and a later reader comparing two
	// snapshots would see matching protocol strings for different protocols.
	Pins map[string]string `json:"pins,omitempty"`
	// Corpus is WHICH benchmarks were run, as Reducer and Benchtime are how they
	// were run. Absent means the snapshot was measured with whatever benchmarks
	// its own commit shipped, which is a different instrument per commit and so
	// not comparable to a pinned one. See Corpus.
	Corpus *Corpus `json:"corpus,omitempty"`
}

// Test262Stats is the conformance outcome of the Test262 run that produced a macro
// measurement: how many tests ran and how they ended, as opposed to how fast they
// were. The two are kept apart on purpose. test262.total was changed from a summed
// to a mean per-test time because the sum moved when the pass count moved, which
// made a conformance change read as a speed change; folding the pass count back
// into the speed series would undo that. This is the other half — the conformance
// signal in its own right, and the denominator that turns the entry's passing count
// into a rate.
type Test262Stats struct {
	Total    int `json:"total"`
	Passed   int `json:"passed"`
	Failed   int `json:"failed"`
	Timeouts int `json:"timeouts"`
	Skipped  int `json:"skipped"`
	// DurationNS is the SUM of per-test durations over every result — passing,
	// failing and timed out alike — not the run's wall-clock time and not the
	// timing metric. The metric (BenchmarkEntry.NSPerOp for test262.total) sums
	// only the passing, non-timed-out set, so this is always the larger number:
	// 159.6s against 117.3s on the first snapshot to carry both. Useful as a
	// rough cost-of-a-run figure; not comparable to ns_per_op * passed.
	DurationNS int64 `json:"duration_ns,omitempty"`
}

// Machine fingerprints the host that captured a baseline.
type Machine struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	NumCPU    int    `json:"num_cpu"`
	CPUModel  string `json:"cpu_model"`
	GoVersion string `json:"go_version"`
}

// Anchor captures the absolute speed of the calibration benchmark.
type Anchor struct {
	Name       string            `json:"name"`
	Package    string            `json:"package"`
	NSPerOp    float64           `json:"ns_per_op"`
	Iterations int64             `json:"iterations,omitempty"`
	Samples    []BenchmarkSample `json:"samples,omitempty"`
}

// BenchmarkEntry is one benchmark's current summary plus optional raw samples.
type BenchmarkEntry struct {
	NSPerOp       float64 `json:"ns_per_op"`
	AllocsPerOp   int64   `json:"allocs_per_op"`
	BytesPerOp    int64   `json:"bytes_per_op"`
	RatioToAnchor float64 `json:"ratio_to_anchor"`
	BestSinceSHA  string  `json:"best_since_sha,omitempty"`
	BestSinceAt   string  `json:"best_since_at,omitempty"`
	// SetHash fingerprints the set of operations aggregated into this measurement,
	// for records whose metric sums over a variable set (e.g. the test262 macro
	// sums per-test times over the passing, non-timed-out tests). Two captures
	// with equal counts but different sets get different hashes, so a consumer can
	// tell whether the number is comparable across snapshots. Empty for ordinary
	// single-op benchmarks, where the op is fixed and the count is the sample size.
	SetHash string `json:"set_hash,omitempty"`
	// AnchorNSPerOp overrides the profile's anchor as this entry's normalization
	// divisor. Present only when the measurement was NOT captured in the same run
	// as the rest of the profile — currently just the Test262 macro backfill, which
	// measures an old commit's engine on today's runner and folds the result into a
	// snapshot whose micro benchmarks came from a different run.
	//
	// Without it that entry is unrepresentable honestly. Normalizing against the
	// profile's anchor is the foreign-anchor bug that cmd/perf-fixratio exists to
	// repair — a run-2 measurement divided by a run-1 calibration. Normalizing
	// against its own fresh anchor is correct but silently violates the invariant
	// the profile advertises. Carrying the divisor makes the second option checkable
	// instead of merely defensible: the ratio still means "times the anchor", and a
	// reader (or the verifier) can see WHICH anchor without trusting a convention.
	AnchorNSPerOp float64 `json:"anchor_ns_per_op,omitempty"`
	// Method overrides the snapshot's Method for this entry, for entries not reduced
	// by bench-ratchet. The Test262 macro is one: the workflow runs it N times and
	// reduces the reps itself, so the snapshot-level "how was this measured" is
	// simply not the answer for this series. It carries reducer:"median" where the
	// micro benchmarks carry bench-ratchet's, and that difference has to be readable
	// — the macro's reducer changed from min to median partway through the corpus
	// (see scripts/macro-test262-reduce.sh for why), and an unrecorded reducer change
	// is exactly the invisible protocol shift Method exists to prevent.
	Method *Method `json:"method,omitempty"`
	// Stats is the conformance outcome of the run behind this entry. Present only on
	// the Test262 macro, where the measurement is taken over a set whose membership
	// is itself a signal; an ordinary benchmark measures a fixed op and has nothing
	// to report here.
	Stats   *Test262Stats     `json:"stats,omitempty"`
	Samples []BenchmarkSample `json:"samples,omitempty"`
}

// BenchmarkSample is one raw benchmark measurement retained for statistics.
type BenchmarkSample struct {
	Iterations    int64   `json:"iterations"`
	NSPerOp       float64 `json:"ns_per_op"`
	BytesPerOp    int64   `json:"bytes_per_op"`
	AllocsPerOp   int64   `json:"allocs_per_op"`
	RatioToAnchor float64 `json:"ratio_to_anchor,omitempty"`
	// AnchorNSPerOp: see BenchmarkEntry.AnchorNSPerOp. Per-sample because samples
	// accumulated across runs each carry their own calibration — the case the entry
	// field generalizes to.
	AnchorNSPerOp float64 `json:"anchor_ns_per_op,omitempty"`
	CapturedAt    string  `json:"captured_at,omitempty"`
}

// StreamRecord is one .jsonl line emitted by bench-ratchet capture.
type StreamRecord struct {
	Package     string  `json:"package"`
	Name        string  `json:"name"`
	Iterations  int64   `json:"iterations"`
	NSPerOp     float64 `json:"ns_per_op"`
	BytesPerOp  int64   `json:"bytes_per_op"`
	AllocsPerOp int64   `json:"allocs_per_op"`
	SetHash     string  `json:"set_hash,omitempty"` // identity of the aggregated set; see BenchmarkEntry.SetHash
	CapturedAt  string  `json:"captured_at"`
}

// Sample returns the record's benchmark measurement without identity fields.
func (r StreamRecord) Sample() BenchmarkSample {
	return BenchmarkSample{
		Iterations:  r.Iterations,
		NSPerOp:     r.NSPerOp,
		BytesPerOp:  r.BytesPerOp,
		AllocsPerOp: r.AllocsPerOp,
		CapturedAt:  r.CapturedAt,
	}
}
