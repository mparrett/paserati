// Recursion/arithmetic macro-benchmark for BenchmarkFibPlaceholderRun.
// Sized so one InterpretChunk is ~30ms: at the CI's 500ms benchtime that yields
// b.N >= ~15, restoring within-sample averaging (the 1e6 variant ran b.N=1, a
// single-shot measurement that scattered ~15% on shared runners). bench-ratchet
// is anchor-normalized, so the smaller absolute time doesn't change the ratio's
// meaning. Kept a separate script from factorial.ts so the smoke test is
// independent of benchmark sizing.
// expect: 2272623587
function factorial(n) {
  return n === 1 ? n : n * factorial(--n);
}
let i = 0;
let output = 0;
while (i++ < 5e4) {
  output += factorial((i % 9) + 1);
}
output;
