# Measurement report review and action plan

**Reviewed:** 2026-08-03  
**Source report:** `/Users/matt/projects-new/project-docs/docs/paserati/measurement-findings-and-open-questions.md`  
**Scope:** Measurement protocol, visualization, CI ratchet, and operational findings

## Executive summary

The source report is strong, evidence-rich, and directionally correct, but the
measurement tooling is not ready to publish or push unchanged.

Do not publish the corrected tier or push the seven local performance commits as
one bundle yet. First make a focused protocol-correction pass, then run an
amended four-round version of the proposed 10-commit session. Based on the
three-round estimate in the run plan, the four-round session should cost roughly
$1.04 instead of $0.78.

The principal conclusions are:

- The existing EPYC tier is historical evidence, not a trustworthy current
  baseline. It is unlabeled mean-reduced, incomplete at 12 of 16 commits, and
  predates the corrected protocol.
- Marginal classifications remain provisional. The 0.1% floor, 2x MAD,
  per-commit/row MAD maximum, and sqrt(2) step adjustment are defensible
  visualization heuristics, but they have not been empirically calibrated as
  decision thresholds.
- No Paserati-specific attribution floor exists yet. That measurement should
  gate claims near the margin.
- The report correctly separates VM microbenchmarks from JS workloads, but the
  aggregate still overweights benchmark groups with many leaf cases, especially
  `GetOwn`.
- Endpoint CI is suitable for merge-head decisions, but not for preserving
  intermediate-commit bisectability. Supporting intermediate trajectories
  should be an explicit policy choice rather than a universal CI expansion.

## P0 defects found during review

### 1. The `b.N` resolution calculation is wrong

`cmd/bench-ratchet/iterations.go` reports resolution as approximately `1/N`,
including 100% at `N=1`. Go computes `ns/op` as elapsed nanoseconds divided by
`N`; at `N=1`, the quantization is one nanosecond, not 100%.

Low `N` reduces averaging and can expose state-dependent behavior, but the
warning's mathematical claim is false. Its displayed percentage and supporting
comments must be removed or replaced before the commit is published.

### 2. The instability check watches the wrong dimension

`unstableIterations` compares `b.N` across repetitions within one snapshot. The
report's causal concern is movement between commits. A benchmark can have stable
repetitions at every commit while moving from one `N` to another across the
stack, and the current check will remain silent.

The `#48` correction should distinguish three phenomena:

1. Low `N`: limited averaging, not coarse percentage quantization.
2. Changing `N` across commits: an inconsistent protocol when cost depends on
   `N`.
3. Slow-tail launch variance: demonstrably not removed by pinning.

This distinction also reconciles the report's A3 conclusion with the fixed-N
experiment, which found that pinning did not consistently reduce launch
instability.

## Prioritized action plan

| Priority | Action | Suggested workstream | Acceptance criteria |
|---|---|---|---|
| P0 | Fix or remove the `1/N` resolution warning; redesign the movement check to compare baseline/current commits or require identical pin metadata | Performance tooling | No percentage-resolution claim based solely on `N`; tests cover stable-within-commit but different-between-commit cases |
| P0 | Rewrite the `#48` correction around averaging, cross-commit protocol consistency, and slow-tail variance | Measurement/report owner | The correction agrees with both the fixed-N experiment and Go timing semantics |
| P0 | Keep the 12-snapshot tier labeled as historical; do not silently replace it with min-reduced data | Performance data owner | Published artifacts state reducer, count, benchtime, pins, tool SHA, and 12/16 completeness |
| P0 | Review the seven local Paserati commits as separate protocol and CI changes; isolate `07764f8c` because its path allow-list can silently suppress runs | Repository maintainer | Core protocol commits are reviewed independently from trigger and engine-key policy |
| P1 | Amend the 10-commit session to four rounds | Measurement operator | Every commit has identical mean run position; no odd-round residual remains |
| P1 | Verify noise controls separately for `pkg/vm` and `./tests` | Measurement operator | Byte-identical binaries are demonstrated for each family before claiming a floor for it |
| P1 | Pre-register the classification rule before examining PR heads | Measurement owner | The rule combines a family-specific attribution floor with normalized-ratio uncertainty and is recorded before results are inspected |
| P1 | Compute MAD on matched per-round benchmark/anchor ratios and test the anchor's regime mismatch | Measurement analysis | Raw-`ns/op` and normalized uncertainty are compared; a family-local canary is evaluated as a detector before becoming a normalizer |
| P1 | Fix aggregate weighting | Visualization/data owner | VM and JS geomeans remain separate; logical benchmark groups, rather than raw leaf count, define weights |
| P2 | Make CI protocol flags explicit and graduate to strict enforcement only after compliance | CI owner | Workflows pin reducer and iteration policy rather than inheriting mutable defaults |
| P2 | Add generated-page regression tests | Visualization owner | Tooltip text, zero values, summary/cell consistency, tail-preserving labels, and overflow have automated checks |
| P2 | Add optional trajectory reporting for performance stacks | CI/product owner | Intermediate regressions are visible when bisectability or partial cherry-picking matters; ordinary PRs remain head-focused |

## Recommended execution sequence

### Phase 1: Correct and freeze the protocol

1. Correct the `b.N` warning and cross-commit comparison behavior.
2. Reconcile the `#48` language and the reducer policy for same-host sessions
   versus the persistent ratchet.
3. Split the runtime measurement changes from the workflow trigger and engine-key
   changes.
4. Review and land the protocol commits.
5. Freeze the measurement path until the validation run completes.

### Phase 2: Run the amended validation session

1. Verify null controls for both benchmark families.
2. Calibrate pins on the measurement host.
3. Record the classification rule before inspecting PR results.
4. Run four counterbalanced rounds across the shared base, control pairs, and
   six PR heads.
5. Compute raw and anchor-normalized uncertainty per benchmark.
6. Publish raw data, reducer inputs, the run manifest, and family-specific
   floors together.

The four independent PRs `#40` through `#43` should be reported individually.
Measure a synthetic combined head only if the combined landing decision is in
scope; do not add or average their separate deltas.

### Phase 3: Apply the results

1. Replace provisional visualization thresholds with the pre-registered,
   measured rule.
2. Publish corrected historical data with explicit provenance, without silently
   overwriting the record of what was originally published.
3. Make protocol settings explicit in CI.
4. Implement benchmark-family-specific engine identity and hierarchical
   aggregation.
5. Add trajectory reporting where commit-level bisectability is a requirement.

## Verification performed during review

- `go build ./cmd/bench-ratchet` passed.
- `go test ./cmd/bench-ratchet` passed.
- Repository-wide `go build ./...` was blocked by an unrelated empty ignored
  file at `scratch/oldrunners/5b19d5a9.go`.
- At review time, Paserati `main` was seven commits ahead of `origin/main`, and
  the project-docs repository was eight commits ahead.

## Decision gate

The measurement pipeline is ready to support marginal performance decisions
when all of the following are true:

- the `b.N` warning and cross-commit protocol check are corrected;
- the reducer, pin table, tool SHA, sample count, benchtime, host, and run order
  are recorded;
- family-specific noise and attribution floors have been measured;
- uncertainty is computed in the same normalized space as the reported delta;
- aggregate weighting is explicit and does not accidentally weight a benchmark
  by its number of leaf cases; and
- the raw measurements and reduction inputs are published with the result.
