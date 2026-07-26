// Command perf-migrate converts a directory of v1 perf snapshots to the
// machine-partitioned v2 format, in place.
//
// v1 names each snapshot "<stamp>-<sha>.json" and carries one flat `machine`, so a
// commit can hold exactly one snapshot: a re-run on a different CPU tier silently
// overwrites the previous tier's measurement. v2 keys both the filename and the
// content by machine profile, so tiers coexist.
//
// The conversion is a pure restructure — every field carries over untouched and
// nothing is re-measured — and it is idempotent: an already-v2 file is detected
// and left byte-for-byte alone, so running this twice is indistinguishable from
// running it once. `-verify` asserts exactly that without writing anything.
//
//	perf-migrate -dir timeline            # convert in place
//	perf-migrate -dir timeline -dry-run   # report what would change
//	perf-migrate -dir timeline -verify    # exit non-zero unless already converged
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nooga/paserati/pkg/perfdata"
)

type plan struct {
	oldPath string
	newPath string
	data    []byte // v2 JSON to write
}

func main() {
	var (
		dir     = flag.String("dir", "", "directory of snapshot .json files (required)")
		dryRun  = flag.Bool("dry-run", false, "report changes without writing")
		verify  = flag.Bool("verify", false, "exit non-zero if any file would change; implies -dry-run")
		verbose = flag.Bool("v", false, "list every file, not just the summary")
	)
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "perf-migrate: -dir is required")
		os.Exit(2)
	}
	if err := run(*dir, *dryRun || *verify, *verify, *verbose); err != nil {
		fmt.Fprintf(os.Stderr, "perf-migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(dir string, dryRun, verify, verbose bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	var (
		plans     []plan
		alreadyV2 int
		total     int
	)
	// Collisions are only detectable across the whole set, so plan everything
	// before writing anything. A partial migration is worse than none.
	claimed := map[string]string{} // newPath -> oldPath that claimed it

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "index.json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // deterministic output and deterministic collision reporting

	for _, name := range names {
		total++
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if perfdata.IsV2(raw) {
			alreadyV2++
			if verbose {
				fmt.Printf("  skip (already v2)  %s\n", name)
			}
			continue
		}

		out, machine, err := perfdata.ConvertV1ToV2(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if machine.Arch == "" || machine.CPUModel == "" {
			return fmt.Errorf("%s: missing machine.arch/cpu_model — cannot derive a machine key", name)
		}

		newName := strings.TrimSuffix(name, ".json") + "-" + perfdata.MachineSlug(machine) + ".json"
		newPath := filepath.Join(dir, newName)
		if prev, dup := claimed[newPath]; dup {
			return fmt.Errorf("collision: %s and %s both map to %s", prev, name, newName)
		}
		claimed[newPath] = name

		plans = append(plans, plan{oldPath: path, newPath: newPath, data: out})
		if verbose {
			fmt.Printf("  convert  %s -> %s\n", name, newName)
		}
	}

	fmt.Printf("%d file(s): %d to convert, %d already v2\n", total, len(plans), alreadyV2)

	if verify {
		if len(plans) > 0 {
			return fmt.Errorf("not converged: %d file(s) still in v1 format", len(plans))
		}
		fmt.Println("verified: fully converged (a further run would change nothing)")
		return nil
	}
	if dryRun || len(plans) == 0 {
		return nil
	}

	for _, p := range plans {
		if err := os.WriteFile(p.newPath, p.data, 0o644); err != nil {
			return err
		}
		// Only after the replacement is safely on disk. If the names happen to
		// match (they can't today, but don't rely on it) removing would delete it.
		if p.newPath != p.oldPath {
			if err := os.Remove(p.oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	fmt.Printf("converted %d file(s)\n", len(plans))
	return nil
}
