package vm

import (
	"runtime"
	"sync"
	"testing"
)

// BenchmarkRatchetMemAnchor is a memory-latency calibration benchmark — a
// companion to the register-only BenchmarkRatchetAnchor. Where the CPU anchor
// is deliberately memory-free (so it stays flat and normalizes compute), this
// one is deliberately DRAM-latency-bound: it chases a random pointer cycle
// through a working set far larger than last-level cache, so every step is a
// dependent, unpredictable load the prefetcher cannot hide.
//
// Purpose: the perf-pr repeat-A/B gate (nooga/paserati#21) divides each
// benchmark's ratio_to_anchor by THIS benchmark's ratio_to_anchor to cancel
// common-mode memory-bandwidth contention on shared CI runners. Memory-bound
// families (GetOwn deep-object lookups, PrototypeMethodAccess chain walks)
// co-vary with it under contention; register-bound families do not, so they
// pass through roughly unchanged. Because both ratios divide out the CPU
// anchor, what remains is family_ns / memanchor_ns — a contention-normalized
// figure. Crucially the family and this anchor are captured in the SAME
// snapshot, so they share a contention environment even though the gate's base
// and head snapshots run in different cycles.
//
// Design constraints (mirror the CPU anchor):
//   - Access pattern is a RANDOM Hamiltonian cycle (deterministic splitmix64
//     shuffle) so the chase is data-dependent and irregular — a constant
//     stride would be caught by hardware stride prefetchers and stop being
//     latency-bound. Dependent loads (idx = ring[idx]) match the object /
//     prototype chain walks this anchor normalizes.
//   - Working set sized past LLC (memAnchorWords*8 bytes = 128 MiB) so a
//     single-threaded chase lands in DRAM on any current CI runner.
//   - The ring is built ONCE via sync.Once (go test re-enters the benchmark
//     func as it grows b.N; rebuilding the 16M-element cycle each time would
//     dominate and add noise). The timed loop only chases, never allocates.
//   - Result escaped via runtime.KeepAlive so DCE can't fold the chase away.
//   - No project-specific code; stays valid across repo rewrites.
const memAnchorWords = 1 << 24 // 16M uint64 = 128 MiB working set (>> any LLC)

var (
	memAnchorOnce sync.Once
	memAnchorRing []uint64
)

func buildMemAnchorRing() {
	// Identity, then Fisher-Yates shuffle into a random order, then wire each
	// element to point at the next — one Hamiltonian cycle over the whole set.
	perm := make([]uint64, memAnchorWords)
	for i := range perm {
		perm[i] = uint64(i)
	}
	var s uint64 = 0x9E3779B97F4A7C15 // fixed seed → reproducible ring
	next := func() uint64 {
		s += 0x9E3779B97F4A7C15
		z := s
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		return z ^ (z >> 31)
	}
	for i := memAnchorWords - 1; i > 0; i-- {
		j := next() % uint64(i+1)
		perm[i], perm[j] = perm[j], perm[i]
	}
	ring := make([]uint64, memAnchorWords)
	for i := 0; i < memAnchorWords; i++ {
		ring[perm[i]] = perm[(i+1)%memAnchorWords]
	}
	memAnchorRing = ring
	// Drop the setup array and collect before timing so a pending GC of 128 MiB
	// doesn't land inside a measured run.
	perm = nil
	runtime.GC()
}

func BenchmarkRatchetMemAnchor(b *testing.B) {
	memAnchorOnce.Do(buildMemAnchorRing)
	ring := memAnchorRing
	var idx uint64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx = ring[idx]
	}
	runtime.KeepAlive(idx)
}
