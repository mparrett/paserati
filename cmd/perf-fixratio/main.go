// Command perf-fixratio repairs and verifies one invariant in perf snapshots:
//
//	ratio_to_anchor == ns_per_op / anchor.ns_per_op
//
// The invariant is the whole point of the field — ratios exist so measurements
// normalize against that snapshot's own calibration run. Where it doesn't hold,
// the number is normalized against something else and is not comparable to
// anything, which is worse than a missing value because it still plots.
//
// It did not hold. Every timeline snapshot before c4c332f had its
// `test262.total` ratio computed against a FOREIGN anchor — neither its own min
// nor its own mean, but some other run's. Errors reach 15%, and because they're
// systematic rather than random they read as a step in the trend: five
// consecutive commits sat ~13% low with a hard boundary at both ends, which looks
// exactly like a real regression and recovery. Micro benchmarks were never
// affected; only the jq-injected test262 entry.
//
// Repair is possible without re-measuring because each snapshot already stores
// both operands: its own anchor and the raw ns_per_op. The corrected ratio is
// arithmetic on data already committed to perf-data.
//
//	perf-fixratio -dir timeline            # repair in place
//	perf-fixratio -dir timeline -dry-run   # report what would change
//	perf-fixratio -dir timeline -verify    # exit non-zero if the invariant is violated
//
// -verify is the durable half: run it in CI and this class of corruption can
// never land silently again.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nooga/paserati/pkg/perfdata"
)

// Relative tolerance for "already correct". Well above float64 round-trip noise
// and far below any real inconsistency: the smallest genuine error in the corpus
// is ~0.3%, and correct entries reproduce to ~1e-15.
const tol = 1e-9

type fix struct {
	path   string // e.g. `benchmarks["test262.total"].ratio_to_anchor`
	stored float64
	want   float64
}

func main() {
	var (
		dir     = flag.String("dir", "", "directory of snapshot .json files (required)")
		dryRun  = flag.Bool("dry-run", false, "report changes without writing")
		verify  = flag.Bool("verify", false, "exit non-zero if any ratio violates the invariant; implies -dry-run")
		verbose = flag.Bool("v", false, "list every violation, not just per-file counts")
	)
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "perf-fixratio: -dir is required")
		os.Exit(2)
	}
	if err := run(*dir, *dryRun || *verify, *verify, *verbose); err != nil {
		fmt.Fprintf(os.Stderr, "perf-fixratio: %v\n", err)
		os.Exit(1)
	}
}

func run(dir string, dryRun, verify, verbose bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "index.json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	totalFixes, filesTouched := 0, 0
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out, fixes, err := repair(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if len(fixes) == 0 {
			continue
		}
		filesTouched++
		totalFixes += len(fixes)
		fmt.Printf("  %s: %d ratio(s)\n", name, len(fixes))
		if verbose {
			for _, f := range fixes {
				fmt.Printf("      %s  stored=%.6g want=%.6g  (%.2f%% off)\n",
					f.path, f.stored, f.want, (f.stored-f.want)/f.want*100)
			}
		}
		if !dryRun {
			if err := os.WriteFile(path, out, 0o644); err != nil {
				return err
			}
		}
	}

	fmt.Printf("%d file(s) scanned: %d with violations, %d ratio(s) total\n",
		len(names), filesTouched, totalFixes)

	if verify {
		if totalFixes > 0 {
			return fmt.Errorf("invariant violated: %d ratio(s) across %d file(s)", totalFixes, filesTouched)
		}
		fmt.Println("verified: every ratio_to_anchor equals ns_per_op / anchor.ns_per_op")
		return nil
	}
	if !dryRun && totalFixes > 0 {
		fmt.Printf("repaired %d ratio(s) in %d file(s)\n", totalFixes, filesTouched)
	}
	return nil
}

// repair returns the corrected document and the list of fixes applied. When no
// fix is needed it returns nil bytes and an empty list, so callers leave the file
// untouched — which is what makes a second run byte-identical to the first.
//
// Handles both layouts. v1 carries anchor+benchmarks at the top level; v2 nests
// one such profile per machine under `machines`. The invariant is per profile
// either way — a ratio is normalized against the anchor measured on ITS machine —
// so v2 is just the same repair applied once per profile.
func repair(raw []byte) ([]byte, []fix, error) {
	root, err := perfdata.ParseObject(raw)
	if err != nil {
		return nil, nil, err
	}

	if machinesRaw, isV2 := root.Get("machines"); isV2 {
		machines, err := perfdata.ParseObject(machinesRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("machines: %w", err)
		}
		var all []fix
		changed := false
		for _, key := range machines.Keys {
			profRaw, _ := machines.Get(key)
			out, fixes, err := repairProfile(profRaw, "machines["+key+"].")
			if err != nil {
				return nil, nil, fmt.Errorf("machines[%q]: %w", key, err)
			}
			if len(fixes) > 0 {
				machines.Set(key, out)
				all = append(all, fixes...)
				changed = true
			}
		}
		if !changed {
			return nil, nil, nil
		}
		root.Set("machines", machines.Encode())
		pretty, err := indent(root.Encode())
		if err != nil {
			return nil, nil, err
		}
		return pretty, all, nil
	}

	out, fixes, err := repairProfile(raw, "")
	if err != nil || len(fixes) == 0 {
		return nil, nil, err
	}
	pretty, err := indent(out)
	if err != nil {
		return nil, nil, err
	}
	return pretty, fixes, nil
}

// repairProfile fixes one anchor+benchmarks profile, returning COMPACT JSON (the
// caller indents once, at whichever level the profile sits).
func repairProfile(raw []byte, prefix string) ([]byte, []fix, error) {
	root, err := perfdata.ParseObject(raw)
	if err != nil {
		return nil, nil, err
	}

	anchorRaw, ok := root.Get("anchor")
	if !ok {
		return nil, nil, fmt.Errorf("no anchor object")
	}
	var anchor struct {
		NSPerOp float64 `json:"ns_per_op"`
	}
	if err := json.Unmarshal(anchorRaw, &anchor); err != nil {
		return nil, nil, err
	}
	if !(anchor.NSPerOp > 0) {
		return nil, nil, fmt.Errorf("anchor.ns_per_op is %v; cannot normalize", anchor.NSPerOp)
	}

	benchRaw, ok := root.Get("benchmarks")
	if !ok {
		return nil, nil, fmt.Errorf("no benchmarks object")
	}
	benchmarks, err := perfdata.ParseObject(benchRaw)
	if err != nil {
		return nil, nil, err
	}

	var fixes []fix
	for _, name := range benchmarks.Keys {
		entryRaw, _ := benchmarks.Get(name)
		entry, err := perfdata.ParseObject(entryRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("benchmarks[%q]: %w", name, err)
		}
		changed := false

		if f, ok := checkEntry(entry, anchor.NSPerOp, fmt.Sprintf("%sbenchmarks[%q]", prefix, name)); ok {
			fixes = append(fixes, f)
			entry.Set("ratio_to_anchor", mustNum(f.want))
			changed = true
		}

		// samples[] carry their own ratio and were injected by the same step, so
		// they carry the same error; leaving them would make the file internally
		// inconsistent in a new way.
		if samplesRaw, has := entry.Get("samples"); has {
			items, err := perfdata.ParseArray(samplesRaw)
			if err != nil {
				return nil, nil, fmt.Errorf("benchmarks[%q].samples: %w", name, err)
			}
			sChanged := false
			for i, itRaw := range items {
				sample, err := perfdata.ParseObject(itRaw)
				if err != nil {
					return nil, nil, fmt.Errorf("benchmarks[%q].samples[%d]: %w", name, i, err)
				}
				if f, ok := checkEntry(sample, anchor.NSPerOp, fmt.Sprintf("%sbenchmarks[%q].samples[%d]", prefix, name, i)); ok {
					fixes = append(fixes, f)
					sample.Set("ratio_to_anchor", mustNum(f.want))
					items[i] = sample.Encode()
					sChanged = true
				}
			}
			if sChanged {
				entry.Set("samples", perfdata.EncodeArray(items))
				changed = true
			}
		}

		if changed {
			benchmarks.Set(name, entry.Encode())
		}
	}

	if len(fixes) == 0 {
		return nil, nil, nil
	}

	root.Set("benchmarks", benchmarks.Encode())
	return root.Encode(), fixes, nil
}

// checkEntry reports a fix when an object carries a ratio_to_anchor that doesn't
// match its own ns_per_op over the anchor. Absent ratio (omitempty) is not a
// violation — there's nothing claiming to be normalized.
func checkEntry(o *perfdata.OrderedObject, anchorNS float64, path string) (fix, bool) {
	ratioRaw, hasRatio := o.Get("ratio_to_anchor")
	nsRaw, hasNS := o.Get("ns_per_op")
	if !hasRatio || !hasNS {
		return fix{}, false
	}
	var stored, ns float64
	if json.Unmarshal(ratioRaw, &stored) != nil || json.Unmarshal(nsRaw, &ns) != nil {
		return fix{}, false
	}
	want := ns / anchorNS
	if want == 0 || math.Abs(stored-want)/math.Abs(want) <= tol {
		return fix{}, false
	}
	return fix{path: path + ".ratio_to_anchor", stored: stored, want: want}, true
}

func mustNum(v float64) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil { // unreachable for a finite float64
		panic(err)
	}
	return b
}

func indent(compact []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
