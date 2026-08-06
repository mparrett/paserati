package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nooga/paserati/pkg/driver"
	"github.com/nooga/paserati/pkg/lexer"
	"github.com/nooga/paserati/pkg/parser"
	"github.com/nooga/paserati/pkg/source"
	"github.com/nooga/paserati/pkg/vm"
)

// Workload benchmarks over the Octane suite, vendored by scripts/octane-vendor.sh.
//
// WHAT THESE ADD
//
// The corpus was almost entirely micro-benchmarks plus two workloads, so a
// change to exception handling or destructuring had nothing that touched it and
// its verdict came from benchmarks that do not exercise it. These run real
// programs: OO dispatch and closures (Richards, DeltaBlue), constraint solving,
// arrays and floats (NavierStokes, RayTrace), bignum arithmetic (Crypto),
// allocation and GC (Splay), and symbolic/string-heavy code (EarleyBoyer).
//
// LESSONS CARRIED OVER FROM THE MICROS — AND ONE THAT DOES NOT CARRY
//
//  1. Measure the workload, not the startup (nooga#51). Definitions and setup
//     run in a separate chunk before the timer. This matters more here than it
//     did there: EarleyBoyer is 200KB of top-level definitions and SplaySetup
//     alone costs ~0.6s against a ~14ms iteration.
//  2. Name the benchmark for what it runs (nooga#53). Each name matches its
//     Octane workload, and the iteration counts live in the fixtures.
//  3. No b.Run sub-benchmarks. `go test -list` does not enumerate them and
//     -benchtime is per top-level invocation, so sub-benchmarks cannot be
//     pinned and their b.N moves between commits. Every workload here is a
//     top-level Benchmark func for that reason alone.
//  4. Fixed work, externally timed. Octane scores itself off a ~1s wall clock,
//     which puts host noise straight into the number — it read 101 and then 178
//     on two runs of one binary. The fixtures do a set number of iterations
//     instead and let go test do the timing.
//  5. Determinism has to be asked for. Octane installs its seeded Math.random
//     in BenchmarkSuite.ResetRNG(), not at load; the setup fixtures call it.
//
// Dead-code elimination — the trap that inflated BenchmarkIsObject — does NOT
// carry over. The work here happens inside the interpreter over data the Go
// compiler cannot see through, and the workloads verify their own results
// (Crypto compares the round trip, Splay validates its tree in teardown). What
// replaces it is a subtler exposure: because Interpret() does not Reset(),
// state persists across ops, so a workload that leaked would get slower as b.N
// rose. ReportAllocs is on so that shows up rather than hiding.
//
// A caveat for whoever reduces these: min-of-N is right for the micros, where
// noise is one-sided. For Splay it is not obviously right, because the minimum
// systematically selects the run where GC happened not to fire. Reduce these
// with the same reducer as the rest of a session for comparability, but do not
// read a Splay delta as engine speed without checking allocs alongside it.

const octaneDir = "scripts/octane"

// compileOctaneChunk compiles one vendored fixture within an existing session,
// so the run chunk resolves globals the setup chunk defined.
//
// It deliberately avoids driver.CompileFile: that helper builds its own session
// and type-checks, and these are plain JavaScript. This mirrors what
// cmd/paserati-v8bench already does, using only API that exists across the
// whole measured history — the corpus overlays this file onto old commits, so
// anything newer than the oldest commit would report N/A instead of measuring.
func compileOctaneChunk(tb testing.TB, p *driver.Paserati, name string) *vm.Chunk {
	tb.Helper()
	path := filepath.Join(octaneDir, name)
	src, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("reading %s: %v", path, err)
	}
	sf := source.FromFile(name, string(src))
	lx := lexer.NewLexerWithSource(sf)
	ps := parser.NewParser(lx)
	program, parseErrs := ps.ParseProgram()
	if len(parseErrs) > 0 {
		tb.Fatalf("parse errors in %s: %v", path, parseErrs[0])
	}
	chunk, compileErrs := p.CompileProgram(program)
	if len(compileErrs) > 0 {
		tb.Fatalf("compile errors in %s: %v", path, compileErrs[0])
	}
	if chunk == nil {
		tb.Fatalf("compiling %s returned a nil chunk", path)
	}
	return chunk
}

// runOctane is the whole harness: build a session, execute the setup chunk once
// outside the timer, then measure only the run chunk.
func runOctane(b *testing.B, workload string) {
	b.Helper()

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0644)
	if err != nil {
		b.Fatalf("Failed to open os.DevNull: %v", err)
	}
	defer devNull.Close()
	oldStdout := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = oldStdout }()

	p := driver.NewPaserati()
	// These are JavaScript, not TypeScript. Without this the checker rejects
	// them, exactly as cmd/paserati-v8bench found.
	p.SetIgnoreTypeErrors(true)

	setupChunk := compileOctaneChunk(b, p, workload+".js")
	runChunk := compileOctaneChunk(b, p, workload+"-run.js")

	// Everything expensive that is not the workload happens here: 200KB of
	// definitions, the seeded RNG, and any setup the workload needs.
	if _, errs := p.InterpretChunk(setupChunk); len(errs) > 0 {
		b.Fatalf("setup failed for %s: %v", workload, errs[0])
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, errs := p.InterpretChunk(runChunk); len(errs) > 0 {
			// A workload that self-checks reports here — Crypto's round trip
			// and RegExp's checksum both surface as runtime errors — so a
			// broken engine fails the benchmark instead of scoring well on it.
			var msgs strings.Builder
			for _, e := range errs {
				msgs.WriteString(e.Error() + "\n")
			}
			b.Fatalf("%s failed at iteration %d:\n%s", workload, i, msgs.String())
		}
	}
	b.StopTimer()

	// Teardown validates workload state (SplayTearDown checks the tree it
	// built). Not every workload has one, and none of it is measured.
	if _, err := os.Stat(filepath.Join(octaneDir, workload+"-teardown.js")); err == nil {
		tearChunk := compileOctaneChunk(b, p, workload+"-teardown.js")
		if _, errs := p.InterpretChunk(tearChunk); len(errs) > 0 {
			b.Fatalf("teardown failed for %s — the workload left bad state: %v", workload, errs[0])
		}
	}
}

func BenchmarkOctaneRichards(b *testing.B)     { runOctane(b, "richards") }
func BenchmarkOctaneDeltaBlue(b *testing.B)    { runOctane(b, "deltablue") }
func BenchmarkOctaneCrypto(b *testing.B)       { runOctane(b, "crypto") }
func BenchmarkOctaneRayTrace(b *testing.B)     { runOctane(b, "raytrace") }
func BenchmarkOctaneEarleyBoyer(b *testing.B)  { runOctane(b, "earley-boyer") }
func BenchmarkOctaneSplay(b *testing.B)        { runOctane(b, "splay") }
func BenchmarkOctaneNavierStokes(b *testing.B) { runOctane(b, "navier-stokes") }
