package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/nooga/paserati/pkg/perfdata"
)

// v1 snapshot fixture. Two machine tiers capturing the SAME commit is the case v1
// could not represent — under v1 these share a filename and the second overwrites
// the first — so it's the case the migration has to get right.
func v1Snapshot(sha, arch, cpu string, ns float64) []byte {
	b := perfdata.Baseline{
		Version:       1,
		CapturedAt:    "2026-07-26T00:52:17Z",
		CapturedAtSHA: sha,
		Machine:       perfdata.Machine{OS: "linux", Arch: arch, NumCPU: 4, CPUModel: cpu, GoVersion: "go1.26.0"},
		Anchor:        perfdata.Anchor{Name: "BenchmarkRatchetAnchor", Package: "pkg/vm", NSPerOp: 1.25},
		Benchmarks: map[string]perfdata.BenchmarkEntry{
			"pkg/vm.BenchmarkGetOwn/n=64/last": {
				NSPerOp: ns, RatioToAnchor: ns / 1.25,
				Samples: []perfdata.BenchmarkSample{{Iterations: 1000, NSPerOp: ns}},
			},
		},
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(raw, '\n')
}

func seed(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("20260726T001156Z-2d3005b4a786.json", v1Snapshot("2d3005b4a786", "amd64", "AMD EPYC 7763 64-Core Processor", 8.109))
	write("20260725T175233Z-24f6f81784a0.json", v1Snapshot("24f6f81784a0", "amd64", "AMD EPYC 9V74 80-Core Processor", 9.44))
	// index.json is generated, not a snapshot — it must be left alone.
	write("index.json", []byte(`["20260726T001156Z-2d3005b4a786.json"]`+"\n"))
	return dir
}

func listJSON(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func snapshotDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, n := range listJSON(t, dir) {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		out[n] = b
	}
	return out
}

// The core property: converting twice is indistinguishable from converting once,
// byte for byte. Without it a migration can't be re-run safely, which in turn
// means it can't be trusted against a corpus that's still being appended to.
func TestMigrationIsIdempotent(t *testing.T) {
	dir := seed(t)

	if err := run(dir, false, false, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	afterFirst := snapshotDir(t, dir)

	if err := run(dir, false, false, false); err != nil {
		t.Fatalf("second run: %v", err)
	}
	afterSecond := snapshotDir(t, dir)

	if !reflect.DeepEqual(afterFirst, afterSecond) {
		t.Errorf("second run changed the directory\n first:  %v\n second: %v",
			listJSON(t, dir), listJSON(t, dir))
	}
	// And -verify agrees the corpus has converged.
	if err := run(dir, true, true, false); err != nil {
		t.Errorf("verify after convergence: %v", err)
	}
}

// -verify must FAIL on un-migrated input, otherwise it proves nothing.
func TestVerifyFailsBeforeMigration(t *testing.T) {
	dir := seed(t)
	if err := run(dir, true, true, false); err == nil {
		t.Fatal("verify passed on a v1 corpus; it must report non-convergence")
	}
	// verify must not have written anything
	if got := listJSON(t, dir); len(got) != 3 {
		t.Errorf("verify mutated the directory: %v", got)
	}
}

// Filenames gain the machine slug, and index.json is untouched.
func TestMigrationRenamesByMachine(t *testing.T) {
	dir := seed(t)
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"20260725T175233Z-24f6f81784a0-amd64-amd-epyc-9v74-80-core-processor.json",
		"20260726T001156Z-2d3005b4a786-amd64-amd-epyc-7763-64-core-processor.json",
		"index.json",
	}
	if got := listJSON(t, dir); !reflect.DeepEqual(got, want) {
		t.Errorf("filenames:\n got  %v\n want %v", got, want)
	}
}

// The restructure must not lose or alter a single field — it's a reinterpretation
// of bytes already on disk, not a recomputation.
func TestMigrationIsLossless(t *testing.T) {
	dir := seed(t)
	before := map[string]perfdata.Baseline{}
	for _, n := range listJSON(t, dir) {
		if n == "index.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		var b perfdata.Baseline
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		before[b.CapturedAtSHA] = b
	}

	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}

	seen := 0
	for _, n := range listJSON(t, dir) {
		if n == "index.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		var v2 perfdata.BaselineV2
		if err := json.Unmarshal(raw, &v2); err != nil {
			t.Fatal(err)
		}
		if v2.Version != 2 {
			t.Errorf("%s: version = %d, want 2", n, v2.Version)
		}
		if len(v2.Machines) != 1 {
			t.Fatalf("%s: got %d profiles, want 1", n, len(v2.Machines))
		}
		for key, mb := range v2.Machines {
			orig, ok := before[mb.CapturedAtSHA]
			if !ok {
				t.Fatalf("%s: unknown sha %q after migration", n, mb.CapturedAtSHA)
			}
			if key != perfdata.MachineKey(orig.Machine) {
				t.Errorf("%s: key = %q, want %q", n, key, perfdata.MachineKey(orig.Machine))
			}
			if !reflect.DeepEqual(mb, orig.ToMachineBaseline()) {
				t.Errorf("%s: payload changed across migration", n)
			}
			seen++
		}
	}
	if seen != len(before) {
		t.Errorf("migrated %d profiles, seeded %d", seen, len(before))
	}
}

// Stronger than TestMigrationIsLossless: the converted payload must equal the
// ORIGINAL JSON minus `version`, compared as JSON rather than through the typed
// structs. Round-tripping through Go structs passes a struct-level check while
// still rewriting the text — `samples` written by the workflow's jq step omit
// allocs_per_op/bytes_per_op, and typed marshalling re-emits them as explicit
// zeros. This test is what catches that.
func TestMigrationPayloadIsVerbatim(t *testing.T) {
	// A field the typed structs would normalize: a samples entry missing the
	// zero-valued keys, exactly as the jq-built test262 entry arrives.
	raw := []byte(`{
  "version": 1,
  "captured_at": "2026-07-26T00:52:17Z",
  "captured_at_sha": "2d3005b4a786",
  "machine": {"os":"linux","arch":"amd64","num_cpu":4,"cpu_model":"AMD EPYC 7763 64-Core Processor","go_version":"go1.26.0"},
  "anchor": {"name":"BenchmarkRatchetAnchor","package":"pkg/vm","ns_per_op":1.25},
  "benchmarks": {
    "test262.total": {
      "ns_per_op": 1234.5678901234,
      "allocs_per_op": 0,
      "bytes_per_op": 0,
      "ratio_to_anchor": 987.6543,
      "set_hash": "abc123",
      "samples": [{"iterations": 42, "ns_per_op": 1234.5678901234}]
    }
  }
}`)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "20260726T001156Z-2d3005b4a786.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}

	names := listJSON(t, dir)
	if len(names) != 1 {
		t.Fatalf("want 1 file, got %v", names)
	}
	got, err := os.ReadFile(filepath.Join(dir, names[0]))
	if err != nil {
		t.Fatal(err)
	}

	var v2 struct {
		Version  int                        `json:"version"`
		Machines map[string]json.RawMessage `json:"machines"`
	}
	if err := json.Unmarshal(got, &v2); err != nil {
		t.Fatal(err)
	}
	if len(v2.Machines) != 1 {
		t.Fatalf("want 1 profile, got %d", len(v2.Machines))
	}
	var payload map[string]any
	for _, p := range v2.Machines {
		if err := json.Unmarshal(p, &payload); err != nil {
			t.Fatal(err)
		}
	}
	if payload == nil {
		t.Fatal("payload decoded to nil")
	}

	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	delete(want, "version")

	if !reflect.DeepEqual(payload, want) {
		t.Errorf("payload is not verbatim\n got:  %v\n want: %v", payload, want)
	}

	// Textual check, independent of the structural one: the sample object the
	// source wrote with only two keys must still have exactly two keys. Typed
	// marshalling would have grown it to four.
	sample := regexp.MustCompile(`(?s)"samples":\s*\[\s*\{(.*?)\}`).FindSubmatch(got)
	if sample == nil {
		t.Fatal("no samples object in output")
	}
	if n := bytes.Count(sample[1], []byte(":")); n != 2 {
		t.Errorf("samples entry has %d keys, want 2 (converter re-emitted omitted fields): %s", n, sample[1])
	}
}

// The same commit measured on two tiers must produce two files, not a collision.
// This is the whole point of keying by machine, and v1 could not express it.
func TestSameCommitTwoTiersCoexist(t *testing.T) {
	dir := t.TempDir()
	// Same stamp+sha, different tiers — under v1 these are literally the same name,
	// so seed them as v1 would have had to: one file, then the other "arriving".
	for i, cpu := range []string{"AMD EPYC 7763 64-Core Processor", "AMD EPYC 9V74 80-Core Processor"} {
		name := "20260726T001156Z-2d3005b4a786.json"
		if i == 1 {
			name = "20260726T001156Z-2d3005b4a786.v9v74.json" // stand-in for the overwritten capture
		}
		if err := os.WriteFile(filepath.Join(dir, name),
			v1Snapshot("2d3005b4a786", "amd64", cpu, 8.1), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(dir, false, false, false); err != nil {
		t.Fatal(err)
	}
	got := listJSON(t, dir)
	if len(got) != 2 {
		t.Fatalf("want 2 coexisting files, got %v", got)
	}
	for _, n := range got {
		if !contains(n, "7763") && !contains(n, "9v74") {
			t.Errorf("filename lacks a machine slug: %s", n)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestMachineSlugMatchesLetGoShellDerivation(t *testing.T) {
	cases := []struct{ arch, cpu, want string }{
		{"amd64", "AMD EPYC 7763 64-Core Processor", "amd64-amd-epyc-7763-64-core-processor"},
		{"amd64", "AMD EPYC 9V74 80-Core Processor", "amd64-amd-epyc-9v74-80-core-processor"},
		{"amd64", "Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz", "amd64-intel-r-xeon-r-platinum-8370c-cpu-2-80ghz"},
		{"arm64", "Apple M1 (Virtual)", "arm64-apple-m1-virtual"},
		{"", "", "unknown"},
	}
	for _, c := range cases {
		got := perfdata.MachineSlug(perfdata.Machine{Arch: c.arch, CPUModel: c.cpu})
		if got != c.want {
			t.Errorf("MachineSlug(%q, %q) = %q, want %q", c.arch, c.cpu, got, c.want)
		}
	}
}
