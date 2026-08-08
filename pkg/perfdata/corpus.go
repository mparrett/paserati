package perfdata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// CorpusStampFile is where bench-corpus.sh records what it overlaid, relative to
// the worktree it overlaid onto. Read back by the measuring run rather than
// passed to it as a flag, for the reason Method gives: provenance a caller can
// assert independently of the thing it describes will eventually disagree with
// it.
const CorpusStampFile = ".bench-corpus-stamp.json"

// Corpus identifies the benchmark corpus a snapshot was measured with.
//
// Without it a corpus change is indistinguishable from an engine change, and it
// does not fail quietly: on 2026-07-25 a single 9R14 snapshot missed the pinned
// overlay and fell back to the commit's own bench_test.go, whose
// PrototypeMethodAccess workload is unsized. Every tests.* benchmark landed
// ~3500x off, the suite aggregate read +253816%, and the page reported it as a
// regression of the commit rather than of the instrument — twice, once entering
// the bad point and once leaving it.
//
// Version alone is not enough to catch that, because the failure mode is an
// overlay that did not happen: a stale stamp from an earlier round would still
// name a version. FilesHash is what makes the record checkable — it digests the
// overlaid files as they sit on disk, so a stamp that no longer describes the
// tree is detected instead of believed.
type Corpus struct {
	Version   string `json:"version,omitempty"`
	Ref       string `json:"ref,omitempty"`
	SHA       string `json:"sha,omitempty"`
	FilesHash string `json:"files_hash,omitempty"`
}

// corpusStamp is the on-disk form. It carries the file list the digest was taken
// over, which the verifier needs and the snapshot does not.
type corpusStamp struct {
	Corpus
	Files []string `json:"files"`
}

// CorpusAt reports the corpus overlaid onto dir, or nil if there is no stamp or
// the stamp no longer describes the tree.
//
// Returning nil for an unverifiable stamp is deliberate: "measured with an
// unknown corpus" is a true statement that a later reader can act on, whereas a
// stamp inherited from a previous round is a false one that reads as pinned.
// perf-session.sh re-overlays after every checkout precisely because
// `git checkout --force` discards the overlay but not the untracked stamp.
func CorpusAt(dir string) *Corpus {
	raw, err := os.ReadFile(filepath.Join(dir, CorpusStampFile))
	if err != nil {
		return nil
	}
	var st corpusStamp
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil
	}
	if len(st.Files) == 0 || st.FilesHash == "" {
		return nil
	}
	have, err := HashFiles(dir, st.Files)
	if err != nil || have != st.FilesHash {
		return nil
	}
	c := st.Corpus
	return &c
}

// HashFiles digests the named files as they exist under dir. Sorted first so the
// digest depends on content and not on the order the config happened to list.
func HashFiles(dir string, files []string) (string, error) {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	h := sha256.New()
	for _, f := range sorted {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return "", fmt.Errorf("corpus file %s: %w", f, err)
		}
		// Length-prefixed so two different file splits cannot collide.
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
