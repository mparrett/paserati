// Command perf-provenance backfills the provenance block into perf snapshots.
//
// Snapshots written before provenance existed identify their code only by
// captured_at_sha, which dangles as soon as history is rewritten. This walks a
// timeline directory, resolves each snapshot's SHA in the repo, and records the
// tree, build hash and patch-id alongside it.
//
// Time-sensitive by nature: it can only backfill commits that are still
// reachable. In this repo the 14 rebase-orphaned commits resolve today via
// refs/preserve/perf-corpus/*, which is what makes the backfill possible at all.
//
//	perf-provenance -dir timeline            # report only, writes nothing
//	perf-provenance -dir timeline -write     # backfill in place
//	perf-provenance -dir timeline -groups    # show the build groups it found
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nooga/paserati/pkg/perfdata"
)

// Snapshot filenames are <timestamp>-<sha12>-<arch>-<cpu>.json. The SHA is the
// only field with a fixed shape, so match it rather than splitting on "-",
// which the CPU model also contains.
var shaRE = regexp.MustCompile(`[0-9a-f]{12}`)

func main() {
	dir := flag.String("dir", "", "timeline directory of snapshot JSON files")
	repo := flag.String("repo", ".", "repository to resolve commits in")
	write := flag.Bool("write", false, "write the provenance block back into each snapshot")
	groups := flag.Bool("groups", false, "print the build groups (commits that must measure identically)")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "perf-provenance: -dir is required")
		os.Exit(2)
	}
	if err := run(*dir, *repo, *write, *groups); err != nil {
		fmt.Fprintf(os.Stderr, "perf-provenance: %v\n", err)
		os.Exit(1)
	}
}

func run(dir, repo string, write, showGroups bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	byBuild := map[string][]string{}
	var scanned, resolved, written, unreachable int

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		scanned++
		sha := shaRE.FindString(name)
		if sha == "" {
			fmt.Fprintf(os.Stderr, "  %s: no sha in filename, skipped\n", name)
			continue
		}
		if !commitExists(repo, sha) {
			// Nothing can be computed for a commit this clone cannot see. Say
			// so per file: a silent skip here would read as a clean backfill.
			fmt.Fprintf(os.Stderr, "  %s: commit %s unreachable, skipped\n", name, sha)
			unreachable++
			continue
		}

		prov := perfdata.ProvenanceAt(repo, sha)
		if prov.BuildHash == "" {
			fmt.Fprintf(os.Stderr, "  %s: build hash unavailable, skipped\n", name)
			continue
		}
		resolved++
		byBuild[prov.BuildHash] = append(byBuild[prov.BuildHash], sha)

		if !write {
			continue
		}
		path := filepath.Join(dir, name)
		out, err := insertProvenance(path, prov)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if out {
			written++
		}
	}

	fmt.Printf("%d snapshot(s): %d resolved, %d unreachable, %d distinct build(s)\n",
		scanned, resolved, unreachable, len(byBuild))
	if write {
		fmt.Printf("wrote provenance into %d file(s)\n", written)
	}

	if showGroups {
		type group struct {
			hash string
			shas []string
		}
		gs := make([]group, 0, len(byBuild))
		for h, s := range byBuild {
			sort.Strings(s)
			gs = append(gs, group{h, s})
		}
		sort.Slice(gs, func(i, j int) bool { return len(gs[i].shas) > len(gs[j].shas) })
		fmt.Println("\nbuild groups — commits within one group MUST measure identically:")
		for _, g := range gs {
			if len(g.shas) < 2 {
				continue
			}
			fmt.Printf("  %s  n=%d  %s\n", g.hash[:12], len(g.shas), strings.Join(g.shas, " "))
		}
	}
	return nil
}

func commitExists(repo, sha string) bool {
	return exec.Command("git", "-C", repo, "cat-file", "-e", sha+"^{commit}").Run() == nil
}

// insertProvenance rewrites one snapshot with its provenance block, leaving
// every other field byte-identical. Decoding into a map rather than the typed
// struct is deliberate: a round-trip through the struct would silently drop any
// field this binary does not know about, and these files outlive the tools.
func insertProvenance(path string, prov perfdata.Provenance) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, err
	}

	encoded, err := json.Marshal(prov)
	if err != nil {
		return false, err
	}

	// v2 nests per machine key; v1 is flat. Both carry captured_at_sha, so the
	// presence of "machines" is what distinguishes them.
	if machinesRaw, ok := doc["machines"]; ok {
		var machines map[string]map[string]json.RawMessage
		if err := json.Unmarshal(machinesRaw, &machines); err != nil {
			return false, err
		}
		for k := range machines {
			machines[k]["provenance"] = encoded
		}
		remarshalled, err := json.Marshal(machines)
		if err != nil {
			return false, err
		}
		doc["machines"] = remarshalled
	} else {
		doc["provenance"] = encoded
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	return true, os.WriteFile(path, out, 0o644)
}
