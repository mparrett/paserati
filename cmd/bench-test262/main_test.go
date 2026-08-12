package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nooga/paserati/pkg/perfdata"
	"github.com/nooga/paserati/pkg/test262"
)

func res(path string, passed, timedOut bool, d time.Duration) test262.Result {
	return test262.Result{Path: path, Passed: passed, TimedOut: timedOut, Duration: d}
}

// totalRecord returns the "test262.total" record from a build.
func totalRecord(t *testing.T, out test262.Output) (nsPerOp float64, iters int64, setHash string) {
	t.Helper()
	for _, r := range buildRecords(out, "2026-07-12T00:00:00Z", nil) {
		if r.Name == "total" {
			return r.NSPerOp, r.Iterations, r.SetHash
		}
	}
	t.Fatal("no total record emitted")
	return 0, 0, ""
}

// records indexes a build by record name, so a test can assert on the total and
// a suite together.
func records(t *testing.T, out test262.Output, ref *refset) map[string]perfdata.StreamRecord {
	t.Helper()
	byName := map[string]perfdata.StreamRecord{}
	for _, r := range buildRecords(out, "2026-07-12T00:00:00Z", ref) {
		byName[r.Name] = r
	}
	return byName
}

// writeRefset writes a refset file with a correct header, which is the shape
// docs/perf/test262-refset.txt has.
func writeRefset(t *testing.T, paths []string) string {
	t.Helper()
	body := strings.Join(paths, "\n")
	hdr := fmt.Sprintf("# a comment\n# count: %d\n# set_hash: %s\n", len(paths), setHash(paths))
	p := filepath.Join(t.TempDir(), "refset.txt")
	if err := os.WriteFile(p, []byte(hdr+body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The point of pinning: a newly passing test must NOT join the sum. Without
// this, a conformance win changes the denominator and moves the mean for a
// reason that has nothing to do with speed.
func TestRefsetExcludesNonMembers(t *testing.T) {
	pinned := []string{
		"test262/test/built-ins/Math/abs/a.js",
		"test262/test/built-ins/Math/ceil/b.js",
	}
	ref := readRefset(writeRefset(t, pinned))

	before := test262.Output{Results: []test262.Result{
		res("test262/test/built-ins/Math/abs/a.js", true, false, 10),
		res("test262/test/built-ins/Math/ceil/b.js", true, false, 20),
	}}
	// Same engine, plus one test that newly started passing.
	after := test262.Output{Results: append(append([]test262.Result{}, before.Results...),
		res("test262/test/built-ins/Math/floor/new.js", true, false, 500))}

	b, a := records(t, before, ref)["total"], records(t, after, ref)["total"]
	if a.NSPerOp != b.NSPerOp || a.Iterations != b.Iterations || a.SetHash != b.SetHash {
		t.Fatalf("a newly passing non-member must not move the total: before(%.0f,%d,%q) after(%.0f,%d,%q)",
			b.NSPerOp, b.Iterations, b.SetHash, a.NSPerOp, a.Iterations, a.SetHash)
	}
	if a.SetHash != a.RefsetHash {
		t.Fatalf("a full run must summarize exactly the pinned set: set %q refset %q", a.SetHash, a.RefsetHash)
	}
	if a.RefsetSize != 2 {
		t.Fatalf("refset_size = %d, want 2", a.RefsetSize)
	}
}

// A member that stops passing must be visible, not absorbed. This is the one
// failure a pinned set can suffer, and the page depends on SetHash moving.
func TestRefsetMemberDroppingOutIsVisible(t *testing.T) {
	pinned := []string{
		"test262/test/built-ins/Math/abs/a.js",
		"test262/test/built-ins/Math/ceil/b.js",
	}
	ref := readRefset(writeRefset(t, pinned))

	regressed := test262.Output{Results: []test262.Result{
		res("test262/test/built-ins/Math/abs/a.js", true, false, 10),
		res("test262/test/built-ins/Math/ceil/b.js", false, false, 20), // now failing
	}}

	r := records(t, regressed, ref)["total"]
	if r.Iterations != 1 {
		t.Fatalf("iterations = %d, want 1 (the surviving member)", r.Iterations)
	}
	if r.RefsetSize-r.Iterations != 1 {
		t.Fatalf("shortfall = %d, want 1", r.RefsetSize-r.Iterations)
	}
	if r.SetHash == r.RefsetHash {
		t.Fatal("a short run must not claim the pinned set's identity")
	}
}

// A suite record is sized against its own share of the pinned set. Sizing it
// against the whole set would report every other suite's tests as missing.
func TestRefsetSuiteRecordsUseTheirOwnShare(t *testing.T) {
	pinned := []string{
		"test262/test/built-ins/Math/abs/a.js",
		"test262/test/built-ins/Math/ceil/b.js",
		"test262/test/language/statements/c.js",
	}
	ref := readRefset(writeRefset(t, pinned))

	out := test262.Output{Results: []test262.Result{
		res("test262/test/built-ins/Math/abs/a.js", true, false, 10),
		res("test262/test/built-ins/Math/ceil/b.js", true, false, 20),
		res("test262/test/language/statements/c.js", true, false, 30),
	}}

	got := records(t, out, ref)
	if n := got["built-ins"].RefsetSize; n != 2 {
		t.Fatalf("built-ins refset_size = %d, want 2", n)
	}
	if n := got["language"].RefsetSize; n != 1 {
		t.Fatalf("language refset_size = %d, want 1", n)
	}
	for _, name := range []string{"built-ins", "language"} {
		if r := got[name]; r.SetHash != r.RefsetHash {
			t.Fatalf("%s: full suite must match its share: set %q refset %q", name, r.SetHash, r.RefsetHash)
		}
	}
	if got["total"].RefsetSize != 3 {
		t.Fatalf("total refset_size = %d, want 3", got["total"].RefsetSize)
	}
}

// A refset file whose contents no longer match its own header is refused rather
// than measured against — an edited set moves the metric exactly like a speedup.
func TestRefsetHeaderMustMatchContents(t *testing.T) {
	if os.Getenv("REFSET_TAMPER") == "1" {
		dir := t.TempDir()
		p := filepath.Join(dir, "refset.txt")
		// Header describes two tests; the body lists one.
		body := "# count: 2\n# set_hash: " + setHash([]string{"a.js", "b.js"}) + "\na.js\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		readRefset(p) // must die
		return
	}
	// die() calls os.Exit, so the refusal is observed in a subprocess.
	cmd := exec.Command(os.Args[0], "-test.run=TestRefsetHeaderMustMatchContents")
	cmd.Env = append(os.Environ(), "REFSET_TAMPER=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit on a tampered refset; output: %s", out)
	}
	if !strings.Contains(string(out), "count 2") {
		t.Fatalf("expected the refusal to name the mismatch; got: %s", out)
	}
}

// The core F3 property: two runs with the SAME passing count but a DIFFERENT
// set must get different SetHashes, so the total isn't silently comparable.
func TestSetHashDistinguishesEqualCountDifferentSet(t *testing.T) {
	runA := test262.Output{Results: []test262.Result{
		res("test262/test/built-ins/Math/abs/a.js", true, false, 10),
		res("test262/test/built-ins/Math/ceil/b.js", true, false, 20),
	}}
	// Same count (2), same summed ns (30), but c.js replaces ceil/b.js.
	runB := test262.Output{Results: []test262.Result{
		res("test262/test/built-ins/Math/abs/a.js", true, false, 10),
		res("test262/test/built-ins/Math/floor/c.js", true, false, 20),
	}}

	nsA, cntA, hA := totalRecord(t, runA)
	nsB, cntB, hB := totalRecord(t, runB)

	if cntA != cntB || nsA != nsB {
		t.Fatalf("precondition: expected equal count/sum, got A(%d,%.0f) B(%d,%.0f)", cntA, nsA, cntB, nsB)
	}
	if hA == "" || hB == "" {
		t.Fatalf("expected non-empty set hashes, got %q %q", hA, hB)
	}
	if hA == hB {
		t.Fatalf("equal-count different-set runs must hash differently; both = %q", hA)
	}
}

// The hash is order-independent (it's a set) and stable across identical runs.
func TestSetHashIsOrderIndependentAndStable(t *testing.T) {
	forward := test262.Output{Results: []test262.Result{
		res("test262/test/language/a.js", true, false, 5),
		res("test262/test/language/b.js", true, false, 7),
		res("test262/test/language/c.js", true, false, 9),
	}}
	reversed := test262.Output{Results: []test262.Result{
		res("test262/test/language/c.js", true, false, 9),
		res("test262/test/language/b.js", true, false, 7),
		res("test262/test/language/a.js", true, false, 5),
	}}

	_, _, hF := totalRecord(t, forward)
	_, _, hR := totalRecord(t, reversed)
	if hF != hR {
		t.Fatalf("set hash must be order-independent: %q != %q", hF, hR)
	}
}

// Failing and timed-out tests are excluded from both the sum and the set,
// matching the metric's definition.
func TestSetHashExcludesFailedAndTimedOut(t *testing.T) {
	withNoise := test262.Output{Results: []test262.Result{
		res("test262/test/built-ins/Math/abs/a.js", true, false, 10),
		res("test262/test/built-ins/Math/ceil/b.js", false, false, 20), // failed
		res("test262/test/built-ins/Math/floor/c.js", true, true, 30),  // timed out
	}}
	clean := test262.Output{Results: []test262.Result{
		res("test262/test/built-ins/Math/abs/a.js", true, false, 10),
	}}

	nsN, cntN, hN := totalRecord(t, withNoise)
	nsC, cntC, hC := totalRecord(t, clean)
	if cntN != 1 || nsN != 10 {
		t.Fatalf("expected only the one passing test to count, got count=%d ns=%.0f", cntN, nsN)
	}
	if hN != hC || cntN != cntC || nsN != nsC {
		t.Fatalf("passing-only set must match the clean run: h %q vs %q", hN, hC)
	}
}
