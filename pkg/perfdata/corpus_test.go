package perfdata

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeTree lays out files under a fresh dir and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func stamp(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, CorpusStampFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCorpusAtVerifiesAgainstTheTree(t *testing.T) {
	files := map[string]string{
		"tests/bench_test.go":   "package tests // v4\n",
		"pkg/vm/anchor_test.go": "package vm\n",
	}
	dir := writeTree(t, files)
	list := []string{"tests/bench_test.go", "pkg/vm/anchor_test.go"}

	want, err := HashFiles(dir, list)
	if err != nil {
		t.Fatal(err)
	}
	stamp(t, dir, `{"version":"v4","ref":"perf/bench-corpus-v2","sha":"abc1234",
	                "files_hash":"`+want+`","files":["tests/bench_test.go","pkg/vm/anchor_test.go"]}`)

	got := CorpusAt(dir)
	if got == nil {
		t.Fatal("a stamp that matches the tree must be trusted")
	}
	if got.Version != "v4" || got.SHA != "abc1234" || got.FilesHash != want {
		t.Fatalf("got %+v", got)
	}
}

// The failure this whole mechanism exists for: `git checkout --force` between
// rounds discards the overlay but not the untracked stamp, so a snapshot can be
// measured with the commit's own benchmarks while a stale stamp still claims the
// pinned corpus. That reads as an engine regression of several thousand percent.
func TestCorpusAtRejectsStampThatNoLongerDescribesTheTree(t *testing.T) {
	dir := writeTree(t, map[string]string{"tests/bench_test.go": "package tests // v4\n"})
	list := []string{"tests/bench_test.go"}
	h, err := HashFiles(dir, list)
	if err != nil {
		t.Fatal(err)
	}
	stamp(t, dir, `{"version":"v4","files_hash":"`+h+`","files":["tests/bench_test.go"]}`)

	// The overlay is reverted; the stamp survives.
	if err := os.WriteFile(filepath.Join(dir, "tests/bench_test.go"),
		[]byte("package tests // whatever this commit shipped\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if c := CorpusAt(dir); c != nil {
		t.Fatalf("stale stamp must not be reported as a pinned corpus, got %+v", c)
	}
}

func TestCorpusAtAbsentOrUnusable(t *testing.T) {
	dir := writeTree(t, map[string]string{"tests/bench_test.go": "package tests\n"})
	if c := CorpusAt(dir); c != nil {
		t.Fatalf("no stamp must mean no corpus, got %+v", c)
	}

	// A stamp naming a file that is not there is unverifiable, not "pinned".
	stamp(t, dir, `{"version":"v4","files_hash":"deadbeef","files":["tests/gone_test.go"]}`)
	if c := CorpusAt(dir); c != nil {
		t.Fatalf("unverifiable stamp must not be reported as pinned, got %+v", c)
	}

	// Neither is one that records no digest to check.
	stamp(t, dir, `{"version":"v4","files":["tests/bench_test.go"]}`)
	if c := CorpusAt(dir); c != nil {
		t.Fatalf("digest-less stamp must not be reported as pinned, got %+v", c)
	}
}

func TestHashFilesIgnoresListOrderAndSeesContent(t *testing.T) {
	dir := writeTree(t, map[string]string{"a_test.go": "aa", "b_test.go": "bb"})

	one, err := HashFiles(dir, []string{"a_test.go", "b_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := HashFiles(dir, []string{"b_test.go", "a_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatalf("digest must not depend on config order: %s vs %s", one, two)
	}

	// Length prefixing: "aa"+"bb" and "a"+"abb" are the same byte stream.
	other := writeTree(t, map[string]string{"a_test.go": "a", "b_test.go": "abb"})
	shifted, err := HashFiles(other, []string{"a_test.go", "b_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if one == shifted {
		t.Fatal("a different split of the same bytes must not collide")
	}
}

// bench-corpus.sh computes files_hash in python and CorpusAt re-computes it in
// Go. Two implementations of one digest drift silently — the stamp would simply
// stop verifying and every snapshot would quietly record no corpus, which is the
// failure this is supposed to detect. Pin them against each other.
func TestShellStampAgreesWithHashFiles(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	files := map[string]string{
		"tests/bench_test.go":        "package tests\n\nfunc BenchmarkX(){}\n",
		"tests/scripts/bench_add.ts": "let x = 1;\n",
	}
	for name, body := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `{"version":"vtest","ref":"HEAD","files":["tests/bench_test.go","tests/scripts/bench_add.ts"]}`
	if err := os.WriteFile(filepath.Join(repo, "corpus.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "init", "-q")
	run("git", "add", "-A")
	run("git", "commit", "-qm", "seed")

	into := t.TempDir()
	script, err := filepath.Abs("../../scripts/bench-corpus.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, "--config", "corpus.json", "--ref", "HEAD", "--into", into)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bench-corpus.sh: %v\n%s", err, out)
	}

	c := CorpusAt(into)
	if c == nil {
		t.Fatal("Go could not verify the digest python wrote — the two implementations have drifted")
	}
	if c.Version != "vtest" {
		t.Fatalf("version: got %q want %q", c.Version, "vtest")
	}
}
