package perfdata

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// BuildManifestID names the current definition of "inputs that reach the
// binary". It is stored alongside every BuildHash so that changing the
// definition invalidates comparisons instead of silently corrupting them: two
// hashes computed under different manifests describe different questions and
// must never be treated as equal.
//
// Bump this whenever buildManifestMatch changes.
const BuildManifestID = "go+testdata@1"

// buildManifestMatch reports whether a repo path is an input to the benchmark
// binary and therefore part of the BuildHash.
//
// Deliberately a function rather than a pathspec: `git ls-tree` does NOT glob
// pathspecs — `git ls-tree -r <sha> -- '*.go'` returns zero lines and :(glob)
// magic is rejected outright, while `git diff-tree` globs correctly. A first
// pass at this hashed empty output and produced a confident, entirely wrong
// grouping (86 commits into 14 builds) that only fell apart when group members
// were diffed. Matching in Go removes that trap.
//
// The benchmark scripts count: the workloads under tests/scripts are what the
// VM executes, so changing one changes the measurement as surely as changing
// the VM does.
func buildManifestMatch(path string) bool {
	switch {
	case strings.HasSuffix(path, ".go"):
		return true
	case path == "go.mod" || path == "go.sum":
		return true
	case strings.HasPrefix(path, "tests/scripts/"):
		return true
	}
	return false
}

// BuildHashAt digests the build inputs of a commit, so that two commits which
// compile to the same program share a hash regardless of what else changed
// between them — CI config, docs and the perf page all drop out.
//
// Hashes the blob SHAs rather than the file contents: git has already hashed
// every blob, so this is a cheap tree walk and identical content yields an
// identical digest by construction.
func BuildHashAt(repo, rev string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "ls-tree", "-r", rev).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-tree %s: %w", rev, err)
	}

	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		// "<mode> <type> <blobsha>\t<path>"
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		if buildManifestMatch(line[tab+1:]) {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		// Never return a hash over nothing: an empty digest is identical for
		// every commit, which would merge the whole corpus into one build.
		return "", fmt.Errorf("build manifest matched no paths at %s — manifest or revision is wrong", rev)
	}
	sort.Strings(lines)

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])[:16], nil
}

// ProvenanceAt collects the identifiers for a commit. A failure to compute any
// one of them is not fatal to the caller: provenance is additive, and a
// snapshot without it is exactly as usable as every snapshot written before
// this existed.
func ProvenanceAt(repo, rev string) Provenance {
	p := Provenance{BuildManifest: BuildManifestID}

	if out, err := exec.Command("git", "-C", repo, "rev-parse", rev+"^{tree}").Output(); err == nil {
		p.CapturedAtTree = strings.TrimSpace(string(out))
	}
	if h, err := BuildHashAt(repo, rev); err == nil {
		p.BuildHash = h
	} else {
		// The manifest not matching means the hash would be meaningless, so
		// record nothing rather than a value that compares equal to everything.
		p.BuildManifest = ""
	}
	if id, err := patchIDAt(repo, rev); err == nil {
		p.PatchID = id
	}
	return p
}

// patchIDAt returns the stable patch-id of a commit — the identifier that
// survives a rebase replaying it under a new SHA. Only a hint: see Provenance.
func patchIDAt(repo, rev string) (string, error) {
	diff := exec.Command("git", "-C", repo, "diff-tree", "-p", rev)
	pipe, err := diff.StdoutPipe()
	if err != nil {
		return "", err
	}
	id := exec.Command("git", "-C", repo, "patch-id", "--stable")
	id.Stdin = pipe
	if err := diff.Start(); err != nil {
		return "", err
	}
	out, err := id.Output()
	if werr := diff.Wait(); werr != nil && err == nil {
		err = werr
	}
	if err != nil {
		return "", err
	}
	if f := strings.Fields(string(out)); len(f) > 0 {
		return f[0], nil
	}
	// A commit with an empty diff (a merge, or an empty commit) has no
	// patch-id. That is not an error, just an absent hint.
	return "", nil
}
