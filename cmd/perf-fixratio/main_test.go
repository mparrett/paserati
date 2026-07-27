package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A snapshot shaped like the real ones: a correct micro entry, and a test262
// entry whose ratio was computed against a FOREIGN anchor (1.4073) rather than
// its own (1.2467) — the actual corruption, at its actual magnitude.
const corrupt = `{
  "version": 1,
  "captured_at": "2026-06-28T02:40:00Z",
  "captured_at_sha": "976a3faa12f3",
  "machine": {"os":"linux","arch":"amd64","num_cpu":4,"cpu_model":"AMD EPYC 7763 64-Core Processor","go_version":"go1.26.0"},
  "anchor": {"name":"BenchmarkRatchetAnchor","package":"pkg/vm","ns_per_op":1.2467},
  "benchmarks": {
    "pkg/vm.BenchmarkGetOwn/n=64/last": {
      "ns_per_op": 8.109,
      "allocs_per_op": 0,
      "bytes_per_op": 0,
      "ratio_to_anchor": 6.504371540867892,
      "samples": [{"iterations": 1000, "ns_per_op": 8.109, "ratio_to_anchor": 6.504371540867892}]
    },
    "test262.total": {
      "ns_per_op": 3016513.72893052,
      "ratio_to_anchor": 2143426.0,
      "set_hash": "abc123",
      "samples": [{"iterations": 40141, "ns_per_op": 3016513.72893052, "ratio_to_anchor": 2143426.0}]
    }
  }
}`

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func seed(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	p := write(t, dir, "20260628T023641Z-976a3faa12f3.json", corrupt)
	write(t, dir, "index.json", `["20260628T023641Z-976a3faa12f3.json"]`)
	return dir, p
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func ratioOf(t *testing.T, doc map[string]any, bench string) float64 {
	t.Helper()
	b := doc["benchmarks"].(map[string]any)[bench].(map[string]any)
	return b["ratio_to_anchor"].(float64)
}

// -verify must FAIL on corrupt input, or it proves nothing.
func TestVerifyFailsOnCorruptInput(t *testing.T) {
	dir, p := seed(t)
	before, _ := os.ReadFile(p)
	if err := run(dir, true, true, false); err == nil {
		t.Fatal("verify passed on a corrupt corpus")
	}
	after, _ := os.ReadFile(p)
	if !reflect.DeepEqual(before, after) {
		t.Error("verify wrote to the file; it must be read-only")
	}
}

// The repair must produce exactly ns_per_op / anchor.ns_per_op, and -verify must
// then pass.
func TestRepairRestoresTheInvariant(t *testing.T) {
	dir, p := seed(t)
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	doc := readJSON(t, p)
	anchor := doc["anchor"].(map[string]any)["ns_per_op"].(float64)
	bench := doc["benchmarks"].(map[string]any)

	for name, v := range bench {
		e := v.(map[string]any)
		ns := e["ns_per_op"].(float64)
		want := ns / anchor
		if got := e["ratio_to_anchor"].(float64); math.Abs(got-want)/want > 1e-12 {
			t.Errorf("%s: ratio = %g, want %g", name, got, want)
		}
		// samples must be repaired too, not left inconsistent
		for i, s := range e["samples"].([]any) {
			sm := s.(map[string]any)
			sw := sm["ns_per_op"].(float64) / anchor
			if got := sm["ratio_to_anchor"].(float64); math.Abs(got-sw)/sw > 1e-12 {
				t.Errorf("%s.samples[%d]: ratio = %g, want %g", name, i, got, sw)
			}
		}
	}
	if err := run(dir, true, true, false); err != nil {
		t.Errorf("verify after repair: %v", err)
	}
}

// Repairing twice must be indistinguishable from repairing once, byte for byte —
// otherwise the tool can't be re-run against a corpus that's still growing.
func TestRepairIsIdempotent(t *testing.T) {
	dir, p := seed(t)
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(p)
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(p)
	if !reflect.DeepEqual(first, second) {
		t.Error("second repair changed the file")
	}
}

// A file that already satisfies the invariant must not be touched at all — not
// even reformatted. Rewriting correct files would make the repair commit's diff
// span the whole corpus and hide what actually changed.
func TestCorrectFileIsNotRewritten(t *testing.T) {
	dir := t.TempDir()
	// Deliberately ugly formatting: if the tool rewrites, this changes.
	good := `{"version":1,"captured_at_sha":"deadbeef",
   "machine":{"arch":"amd64","cpu_model":"AMD EPYC 7763 64-Core Processor"},
        "anchor":{"ns_per_op":2.0},
  "benchmarks":{"a":{"ns_per_op":8.0,"ratio_to_anchor":4.0}}}`
	p := write(t, dir, "20260101T000000Z-deadbeefcafe.json", good)
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != good {
		t.Errorf("a correct file was rewritten:\n got: %s\nwant: %s", got, good)
	}
}

// Only ratio_to_anchor may change. Everything else — including fields the typed
// structs would normalize, like the samples entry that omits allocs/bytes — must
// survive untouched.
func TestOnlyRatiosChange(t *testing.T) {
	dir, p := seed(t)
	before := readJSON(t, p)
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	after := readJSON(t, p)

	strip := func(m map[string]any) {
		for _, v := range m["benchmarks"].(map[string]any) {
			e := v.(map[string]any)
			delete(e, "ratio_to_anchor")
			for _, s := range e["samples"].([]any) {
				delete(s.(map[string]any), "ratio_to_anchor")
			}
		}
	}
	strip(before)
	strip(after)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("fields other than ratio_to_anchor changed\n before: %v\n after:  %v", before, after)
	}

	// The micro entry was already correct; its ratio must be unchanged in value.
	orig := 6.504371540867892
	if got := ratioOf(t, readJSON(t, p), "pkg/vm.BenchmarkGetOwn/n=64/last"); math.Abs(got-orig)/orig > 1e-9 {
		t.Errorf("an already-correct micro ratio was altered: %g -> %g", orig, got)
	}
}

// The corrupt test262 ratio is ~12.9% off; confirm the repair moves it by about
// that much and in the right direction, so a sign error can't pass silently.
func TestRepairMagnitude(t *testing.T) {
	dir, p := seed(t)
	before := ratioOf(t, readJSON(t, p), "test262.total")
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	after := ratioOf(t, readJSON(t, p), "test262.total")
	delta := (after - before) / before * 100
	if delta < 12 || delta > 14 {
		t.Errorf("expected ~+12.9%% correction, got %+.2f%% (%g -> %g)", delta, before, after)
	}
}

// index.json is generated, not a snapshot, and has no anchor — it must be skipped
// rather than erroring the run.
func TestIndexJSONIgnored(t *testing.T) {
	dir, _ := seed(t)
	if err := run(dir, true, false, false); err != nil {
		t.Fatalf("index.json was not skipped: %v", err)
	}
}

// v2 nests one anchor+benchmarks profile per machine. The invariant is per
// profile — a ratio is normalized against the anchor measured on ITS machine — so
// the repair must descend rather than look only at the top level. Without this it
// would silently pass every v2 file, which is worse than failing: the guard would
// report "verified" while checking nothing.
func TestRepairsV2PerMachineProfile(t *testing.T) {
	v2 := `{
  "version": 2,
  "machines": {
    "amd64/AMD EPYC 7763 64-Core Processor": {
      "captured_at_sha": "aaaaaaaaaaaa",
      "machine": {"arch":"amd64","cpu_model":"AMD EPYC 7763 64-Core Processor"},
      "anchor": {"ns_per_op": 1.25},
      "benchmarks": {"a": {"ns_per_op": 10.0, "ratio_to_anchor": 9.0}}
    },
    "arm64/Apple M2": {
      "captured_at_sha": "aaaaaaaaaaaa",
      "machine": {"arch":"arm64","cpu_model":"Apple M2"},
      "anchor": {"ns_per_op": 0.8},
      "benchmarks": {"a": {"ns_per_op": 4.0, "ratio_to_anchor": 5.0}}
    }
  }
}`
	dir := t.TempDir()
	p := write(t, dir, "20260101T000000Z-aaaaaaaaaaaa-multi.json", v2)

	if err := run(dir, true, true, false); err == nil {
		t.Fatal("verify passed on a corrupt v2 file; it must descend into machines")
	}
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Machines map[string]struct {
			Anchor struct {
				NSPerOp float64 `json:"ns_per_op"`
			} `json:"anchor"`
			Benchmarks map[string]struct {
				NSPerOp float64 `json:"ns_per_op"`
				Ratio   float64 `json:"ratio_to_anchor"`
			} `json:"benchmarks"`
		} `json:"machines"`
	}
	raw, _ := os.ReadFile(p)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Machines) != 2 {
		t.Fatalf("want 2 profiles, got %d", len(got.Machines))
	}
	// 10.0/1.25 = 8.0 (was 9.0); 4.0/0.8 = 5.0 (already correct, must not move).
	for key, m := range got.Machines {
		want := m.Benchmarks["a"].NSPerOp / m.Anchor.NSPerOp
		if math.Abs(m.Benchmarks["a"].Ratio-want)/want > 1e-12 {
			t.Errorf("%s: ratio = %g, want %g", key, m.Benchmarks["a"].Ratio, want)
		}
	}
	if err := run(dir, true, true, false); err != nil {
		t.Errorf("verify after repair: %v", err)
	}
}

// A snapshot whose test262 entry was measured in a DIFFERENT run from its micro
// benchmarks, and says so: anchor_ns_per_op records the calibration that ran
// beside it. Its ratio is correct against that anchor (3016513.72893052/1.9042)
// and would look ~53% wrong against the profile's (1.2467).
const offRunAnchor = `{
  "version": 1,
  "captured_at": "2026-06-28T02:40:00Z",
  "captured_at_sha": "976a3faa12f3",
  "machine": {"os":"linux","arch":"amd64","num_cpu":4,"cpu_model":"AMD EPYC 7763 64-Core Processor","go_version":"go1.26.0"},
  "anchor": {"name":"BenchmarkRatchetAnchor","package":"pkg/vm","ns_per_op":1.2467},
  "benchmarks": {
    "test262.total": {
      "ns_per_op": 3016513.72893052,
      "ratio_to_anchor": 1584137.028111816,
      "anchor_ns_per_op": 1.9042,
      "samples": [{"iterations": 40141, "ns_per_op": 3016513.72893052, "ratio_to_anchor": 1584137.028111816}]
    }
  }
}`

// An entry normalized against its own recorded anchor is CORRECT, not corrupt.
// Without this the backfill's output would fail the publish gate, and -fix would
// "repair" it into the foreign-anchor error the command exists to remove.
func TestOwnAnchorIsHonoured(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "20260628T023641Z-976a3faa12f3.json", offRunAnchor)
	before, _ := os.ReadFile(p)
	if err := run(dir, true, true, false); err != nil {
		t.Fatalf("verify rejected a correctly self-anchored entry: %v", err)
	}
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if !reflect.DeepEqual(before, after) {
		t.Error("repair rewrote a correct self-anchored entry")
	}
}

// The override must be enforced, not merely tolerated: a ratio that matches the
// PROFILE's anchor while the entry declares its own is a violation.
func TestOwnAnchorIsEnforcedNotIgnored(t *testing.T) {
	dir := t.TempDir()
	bad := `{
  "version": 1,
  "machine": {"os":"linux","arch":"amd64","num_cpu":4,"cpu_model":"c","go_version":"go1.26.0"},
  "anchor": {"ns_per_op":1.2467},
  "benchmarks": {
    "test262.total": {
      "ns_per_op": 3016513.72893052,
      "ratio_to_anchor": 2419598.7237751824,
      "anchor_ns_per_op": 1.9042
    }
  }
}`
	p := write(t, dir, "20260628T023641Z-aaaaaaaaaaaa.json", bad)
	if err := run(dir, true, true, false); err == nil {
		t.Fatal("verify accepted a ratio normalized against the profile anchor " +
			"while the entry declared its own")
	}
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	got := ratioOf(t, readJSON(t, p), "test262.total")
	if want := 3016513.72893052 / 1.9042; math.Abs(got-want)/want > 1e-12 {
		t.Errorf("repaired against the wrong anchor: got %v, want %v", got, want)
	}
}

// A sample inherits its ENTRY's anchor, not the profile's. Otherwise every
// sample under an off-run entry is flagged and rewritten to the wrong divisor.
func TestSamplesInheritTheEntryAnchor(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "version": 1,
  "machine": {"os":"linux","arch":"amd64","num_cpu":4,"cpu_model":"c","go_version":"go1.26.0"},
  "anchor": {"ns_per_op":1.2467},
  "benchmarks": {
    "test262.total": {
      "ns_per_op": 3016513.72893052,
      "ratio_to_anchor": 1584137.028111816,
      "anchor_ns_per_op": 1.9042,
      "samples": [{"iterations": 40141, "ns_per_op": 3016513.72893052, "ratio_to_anchor": 2419598.7237751824}]
    }
  }
}`
	p := write(t, dir, "20260628T023641Z-bbbbbbbbbbbb.json", body)
	if err := run(dir, true, true, false); err == nil {
		t.Fatal("verify accepted a sample normalized against the profile anchor")
	}
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	doc := readJSON(t, p)
	s := doc["benchmarks"].(map[string]any)["test262.total"].(map[string]any)["samples"].([]any)[0].(map[string]any)
	got := s["ratio_to_anchor"].(float64)
	if want := 3016513.72893052 / 1.9042; math.Abs(got-want)/want > 1e-12 {
		t.Errorf("sample repaired against the wrong anchor: got %v, want %v", got, want)
	}
}

// A malformed or non-positive override must fall back to the inherited anchor
// rather than dividing by it.
func TestBadOwnAnchorFallsBack(t *testing.T) {
	for _, bad := range []string{`0`, `-1.5`, `"1.9"`, `null`} {
		dir := t.TempDir()
		body := `{
  "version": 1,
  "machine": {"os":"linux","arch":"amd64","num_cpu":4,"cpu_model":"c","go_version":"go1.26.0"},
  "anchor": {"ns_per_op":1.2467},
  "benchmarks": {
    "b": {"ns_per_op": 8.109, "ratio_to_anchor": 6.504371540867892, "anchor_ns_per_op": ` + bad + `}
  }
}`
		write(t, dir, "20260628T023641Z-cccccccccccc.json", body)
		if err := run(dir, true, true, false); err != nil {
			t.Errorf("anchor_ns_per_op=%s: expected fallback to the profile anchor, got %v", bad, err)
		}
	}
}
