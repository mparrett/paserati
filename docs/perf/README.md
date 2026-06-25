# Perf ratchet

`cmd/bench-ratchet` runs the Go benchmarks, normalizes each against a frozen
calibration anchor (`BenchmarkRatchetAnchor`, a pure-CPU FMA loop in `pkg/vm`),
and compares to `baseline.json`. Normalizing against the anchor divides out the
host's raw speed, so a regression flags only when *normalized* work grows — which
is what makes the gate usable on noisy, varying CI runners.

## Local use

```bash
go run ./cmd/bench-ratchet show              # capture + print, no write
go run ./cmd/bench-ratchet check             # compare to baseline, exit 1 on regression
go run ./cmd/bench-ratchet update            # ratchet the baseline toward current (only tightens)
go run ./cmd/bench-ratchet update -force     # overwrite the baseline as-is (accept a regression)
```

`update` is a ratchet: each metric only moves toward faster / fewer allocs. A
removed benchmark keeps its bar (rename guard) unless you pass `-force`.

## CI

`.github/workflows/bench.yml` runs `check` on pull requests (anchor-normalized,
`-budget 0.15`). The baseline is seeded/re-seeded on CI by dispatching the
workflow in `update` mode, which commits `baseline.json` back — so the bar is set
on the same runner class that gates it.

**First-time seed:** the workflow must be on the default branch before
`workflow_dispatch` is available. Once it is, run it once with `mode=update` to
seed `baseline.json`; PR checks compare against that bar thereafter.

`docs/perf/.runs/` (raw `.jsonl` captures) is gitignored; only `baseline.json` is
tracked.
