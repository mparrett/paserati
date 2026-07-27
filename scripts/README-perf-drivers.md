# Perf timeline drivers

Two scripts used to fill and extend the "Are we fast yet?" timeline. Neither runs
in CI — they drive it from a workstation. Parked on this branch so they survive
outside a gitignored `scratch/`; not proposed for `main`.

## perf-backfill-driver.sh — dispatch snapshots for historical commits

```bash
REPO=mparrett/paserati REF_CPU='EPYC 7763' MAX_TRIES=4 \
  ./scripts/perf-backfill-driver.sh <full-sha>...
```

Dispatches Perf Timeline one commit at a time, waits for each run, checks the
resulting snapshot's `cpu_model`, and retries when it lands off the reference tier.

Why serial, and why it matters:

- Perf Timeline serializes on the `perf-timeline-main` concurrency group with
  `cancel-in-progress: false`, and GitHub keeps **at most one pending run per
  group** — so a batch dispatch lands one snapshot and cancels the rest.
  Dispatching 9 at once landed 1.
- The same rule means **two drivers must never run at once**, and stopping a
  driver is not enough: a dispatch from driver B evicts driver A's *pending* run.
  That happened once here and cost a snapshot.
- `runs-on: ubuntu-latest` is a tier lottery. Observed rate over 16 dispatches:
  ~27% land off the reference tier. Every miss but one recovered on the next
  attempt; one commit missed three consecutive times. `MAX_TRIES=4` is comfortable.
- Resolve shas with `git rev-parse` — the driver validates and skips unknown
  objects, which is how a fabricated sha got caught instead of silently
  snapshotting `main`.
- Orphan commits (unreachable from HEAD) **cannot** be snapshotted at all: the
  workflow checks out the target ref and fails with `unable to read tree`. Those
  are tombstoned by `pages.yml` rather than plotted.

## perf-local-sweep.sh — capture snapshots on this machine

```bash
./scripts/perf-local-sweep.sh <outdir> <sha>...
```

Produces v1 snapshots with the same `commit`/`method` provenance the workflow
records, tagged `method.host: "local"`. Convert with `cmd/perf-migrate` before
publishing.

Only safe because snapshots are machine-keyed (v2): the filename carries
`<arch>-<cpumodel>`, so a local capture lands beside the CI snapshot for the same
commit. **Under v1 this would have overwritten CI data.**

It measures the first commit again at the end and reports the drift. A laptop is
not a quiet benchmark host — an early sweep here showed the anchor moving 7.5%
across nine minutes on one machine — so without that control there is no way to
separate a real between-commit difference from the machine warming up. The repeat
is a control, not a data point.

Test262 is skipped locally (needs the corpus, ~12 min/commit).
