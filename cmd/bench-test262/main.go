// Command bench-test262 turns a `paserati-test262 -json` run into perf
// StreamRecord(s) that bench-ratchet's aggregate step normalizes against
// the calibration anchor — so the Test262 macro-benchmark lands on the
// timeline page as just another series, with no new normalization code.
//
// The metric is the SUM of per-test execution time over the tests that
// passed and did NOT time out. Two deliberate properties:
//
//   - Summing per-test durations (not wall-clock) is parallelism-invariant:
//     the runner can fan out for throughput without moving the number.
//   - Restricting to the passing, non-timed-out set decouples speed from
//     correctness: a regression that makes tests time out changes the
//     timeout COUNT (reported separately), not the timing sum.
//
// Usage:
//
//	paserati-test262 -path ./test262 -subpath language -json | bench-test262 >> run.jsonl
//	bench-test262 -in results.json -out run.jsonl
//
// Emits one record for the whole suite (test262/total) plus one per
// top-level suite (test262/<suite>) for the page's Breakdown toggle.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nooga/paserati/pkg/perfdata"
	"github.com/nooga/paserati/pkg/test262"
)

func main() {
	var (
		inPath    = flag.String("in", "", "paserati-test262 -json input (default: stdin)")
		outPath   = flag.String("out", "", "append StreamRecord JSONL here (default: stdout)")
		timestamp = flag.String("timestamp", "", "RFC3339 capture time (default: now, UTC)")
		refPath   = flag.String("refset", "", "restrict the sum to this pinned reference set (docs/perf/test262-refset.txt)")
	)
	flag.Parse()

	out := readResults(*inPath)

	capturedAt := strings.TrimSpace(*timestamp)
	if capturedAt == "" {
		capturedAt = time.Now().UTC().Format(time.RFC3339)
	}

	var ref *refset
	if *refPath != "" {
		ref = readRefset(*refPath)
	}

	records := buildRecords(out, capturedAt, ref)
	writeRecords(records, *outPath)
}

// refset is a pinned set of test paths the sum is restricted to. Nil means an
// unrestricted run, which is what every caller did before the set was pinned.
type refset struct {
	members map[string]bool
	hash    string // digest of the whole set, by the same function as setHash

	// Per-top-level-suite view. A suite record has to describe its OWN share of
	// the pinned set: sizing test262.built-ins against all 40,141 would make the
	// shortfall arithmetic report every language test as a missing member.
	suiteHash map[string]string
	suiteSize map[string]int64
}

// readRefset loads the pinned set and verifies it against its own header.
//
// The verification is the point, and it is the same argument the corpus stamp
// makes: a file that merely EXISTS proves nothing, because the failure mode is a
// set that was edited — a member deleted to "fix" a regression, a merge that
// dropped a chunk — and a silently smaller reference set moves test262.total
// exactly the way a real speedup does. Recomputing the digest turns that into a
// refusal. The header is also the only place a reader can see the intended size
// without counting 40,000 lines.
func readRefset(path string) *refset {
	raw, err := os.ReadFile(path)
	if err != nil {
		die("open -refset: %v", err)
	}
	var (
		paths     []string
		wantHash  string
		wantCount = -1
	)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			field := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			switch {
			case strings.HasPrefix(field, "set_hash:"):
				wantHash = strings.TrimSpace(strings.TrimPrefix(field, "set_hash:"))
			case strings.HasPrefix(field, "count:"):
				n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(field, "count:")))
				if err != nil {
					die("-refset %s: unreadable count header: %v", path, err)
				}
				wantCount = n
			}
			continue
		}
		paths = append(paths, line)
	}
	if len(paths) == 0 {
		die("-refset %s: no test paths", path)
	}

	members := make(map[string]bool, len(paths))
	for _, p := range paths {
		if members[p] {
			// A duplicate would inflate the header count while leaving the digest
			// (taken over the deduplicated, sorted set) unchanged, so the two
			// checks below could both pass on a file that is not what it claims.
			die("-refset %s: duplicate path %s", path, p)
		}
		members[p] = true
	}
	if wantCount >= 0 && wantCount != len(paths) {
		die("-refset %s: header says count %d, file has %d", path, wantCount, len(paths))
	}
	got := setHash(paths)
	if wantHash != "" && wantHash != got {
		die("-refset %s: header says set_hash %s, contents hash to %s — the file was edited without updating its own digest", path, wantHash, got)
	}

	bySuite := map[string][]string{}
	for _, p := range paths {
		s := topLevelSuite(p)
		bySuite[s] = append(bySuite[s], p)
	}
	suiteHash := make(map[string]string, len(bySuite))
	suiteSize := make(map[string]int64, len(bySuite))
	for s, ps := range bySuite {
		suiteHash[s] = setHash(ps)
		suiteSize[s] = int64(len(ps))
	}

	return &refset{members: members, hash: got, suiteHash: suiteHash, suiteSize: suiteSize}
}

// buildRecords sums per-test durations over the passing, non-timed-out set,
// emitting a total plus per-top-level-suite breakdown. NSPerOp carries the
// summed nanoseconds; aggregate divides it by the anchor to normalize.
//
// Each record also carries a SetHash over the exact set of tests it summed, so
// a downstream consumer can tell whether two totals cover the same workload:
// equal counts alone don't guarantee it (a test flipping pass->timeout while
// another flips the other way keeps the count but changes the set and the sum).
//
// With a refset the contributing set is the pinned one instead of "whatever
// passed", which is what makes the series a per-test-speed signal rather than a
// mixture of speed and conformance. Note what does NOT change: a member is still
// dropped if it failed or timed out. Pinning the set cannot make a test that did
// not run contribute a duration, so the refset is the ceiling on membership and
// never the floor.
func buildRecords(out test262.Output, capturedAt string, ref *refset) []perfdata.StreamRecord {
	type acc struct {
		sumNS float64
		paths []string // contributing test paths — hashed into the record's SetHash
	}
	bySuite := map[string]*acc{}
	total := &acc{}

	for _, r := range out.Results {
		if !r.Passed || r.TimedOut {
			continue
		}
		if ref != nil && !ref.members[r.Path] {
			continue
		}
		ns := float64(r.Duration) // time.Duration marshals as int64 nanoseconds
		total.sumNS += ns
		total.paths = append(total.paths, r.Path)

		suite := topLevelSuite(r.Path)
		a := bySuite[suite]
		if a == nil {
			a = &acc{}
			bySuite[suite] = a
		}
		a.sumNS += ns
		a.paths = append(a.paths, r.Path)
	}

	rec := func(name string, a *acc) perfdata.StreamRecord {
		r := perfdata.StreamRecord{
			Package:    "test262",
			Name:       name,
			Iterations: int64(len(a.paths)), // tests contributing to the sum
			NSPerOp:    a.sumNS,             // summed execution time; anchor-normalized downstream
			SetHash:    setHash(a.paths),    // identity of that contributing set
			CapturedAt: capturedAt,
		}
		if ref != nil {
			if name == "total" {
				r.RefsetHash, r.RefsetSize = ref.hash, int64(len(ref.members))
			} else {
				r.RefsetHash, r.RefsetSize = ref.suiteHash[name], ref.suiteSize[name]
			}
		}
		return r
	}

	records := []perfdata.StreamRecord{rec("total", total)}
	suites := make([]string, 0, len(bySuite))
	for s := range bySuite {
		suites = append(suites, s)
	}
	sort.Strings(suites)
	for _, s := range suites {
		records = append(records, rec(s, bySuite[s]))
	}
	return records
}

// setHash fingerprints a contributing set by its member test paths, order-
// independent. Truncated SHA-256 (64 bits) — collision odds are negligible for
// set sizes here, and it stays short in the JSON. Empty set -> empty hash.
func setHash(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, p := range sorted {
		h.Write([]byte(p))
		h.Write([]byte{'\n'}) // delimiter: no path joins with the next to forge the same digest
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// topLevelSuite extracts the chapter from a path like
// "test262/test/built-ins/Math/abs/x.js" -> "built-ins".
func topLevelSuite(path string) string {
	const marker = "/test/"
	i := strings.Index(path, marker)
	if i < 0 {
		return "unknown"
	}
	rest := path[i+len(marker):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		return rest[:j]
	}
	return "unknown"
}

func readResults(inPath string) test262.Output {
	f := os.Stdin
	if inPath != "" {
		var err error
		f, err = os.Open(inPath)
		if err != nil {
			die("open -in: %v", err)
		}
		defer f.Close()
	}
	var out test262.Output
	if err := json.NewDecoder(f).Decode(&out); err != nil {
		die("decode test262 json: %v", err)
	}
	if len(out.Results) == 0 {
		die("no results in input (did the run produce -json output?)")
	}
	return out
}

func writeRecords(records []perfdata.StreamRecord, outPath string) {
	w := os.Stdout
	if outPath != "" {
		f, err := os.OpenFile(outPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			die("open -out: %v", err)
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	enc := json.NewEncoder(bw)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			die("encode record: %v", err)
		}
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bench-test262: "+format+"\n", args...)
	os.Exit(1)
}
