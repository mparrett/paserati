# Perf session runbook

How to measure N commits on one machine, and how to get what you learn back
into the tools. Generic; a specific session adds only its question, its commit
list and its box.

Prior sessions, as worked examples:
`project-docs/docs/paserati/perf-session-microopt-run-plan.md` (the plan) and
`perf-session-microopt-results.md` (the result, including its retractions).

## When a session is the right instrument

The CI timeline measures one commit per run on a runner drawn from a pool of
four-plus CPU models. A single point carries ~2.26%, so two points must differ
by ~6.4% before the difference is visible. That noise is *between hosts*.

Measure every commit on one machine, back to back, and the between-host term is
common to all of them and cancels. Use a session when you need to resolve
changes smaller than the timeline can see, and accept that the result lives in
its own machine-keyed tier which must never be compared against a CI tier.

## 0. Decide the question before booting anything

Write down what result would change your mind. "Did these 16 commits do what
the commit messages say" is a question. "Let's see how it looks" is not, and
produces a session whose numbers get reinterpreted until they agree with
whatever was already believed.

Select commits by distinct **runtime engine**, not by commit: on this corpus
47% of commits change no runtime code at all and cannot move a benchmark.
`perf-timeline.yml`'s engine key is the same computation.

## 1. The box

Fixed-performance only: `c7a`, `c7i`, `m7a`. **Never burstable** (`t2`, `t3`,
`t3a`, `t4g`) — CPU credit throttling means the machine changes speed
mid-session, which is variance indistinguishable from signal.
`perf-session.sh` warns and continues; heed the warning.

Match the **instance type of the tier you want to join**, or you start a new
tier. Match the corpus **Go version** too.

## 2. Preflight

```bash
go version                    # must match the corpus
nproc ; jq --version

# Confirm the instance type from metadata, not from memory:
TOKEN=$(curl -s -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' \
  http://169.254.169.254/latest/api/token)
curl -s -H "X-aws-ec2-metadata-token: $TOKEN" \
  http://169.254.169.254/latest/meta-data/instance-type
```

**Tag `perf-data` before publishing anything.** A bad write to the corpus is
recoverable only from a tag, and the tags are made by hand.

```bash
git fetch origin perf-data
git tag perf-data-pre-<session> FETCH_HEAD && git push origin perf-data-pre-<session>
```

**Arm a dead-man switch.** `shutdown -h +<minutes>` in user-data,
`--instance-initiated-shutdown-behavior terminate`, and an EXIT trap in the
driver script. reg-lisp adopted this and had three consecutive runs fail; the
trap is why those failures cost nothing.

All of that — launch, the watchdog, the burstable refusal, and teardown verified
by re-query — is in `scripts/perf-session-box.sh`:

```bash
scripts/perf-session-box.sh up                 # launch, wait, print the ssh line
scripts/perf-session-box.sh status             # what is running, in every region
scripts/perf-session-box.sh down               # terminate, then verify
```

It exists because this recipe used to live only in old session transcripts;
reconstructing it on 2026-08-03 meant grepping a conversation log for
`run-instances`. Confirm the instance type from instance metadata after boot —
the script prints it — rather than from what you asked for.

## 3. Calibrate b.N — before the real run

Both packages, and they need different sweeps:

```bash
# ./tests — milliseconds per op, b.N in the ones-to-hundreds
scripts/bench-calibrate.sh -o calib-tests.json

# pkg/vm — NANOSECONDS per op, b.N in the tens of millions. The default
# 1..256 sweep measures timer overhead here and nothing else.
scripts/bench-calibrate.sh -p github.com/nooga/paserati/pkg/vm \
  -n 10000000 -m 320000000 -f 10000000 -o calib-vm.json
```

`b.N` is an *output* of the timing loop (N ≈ benchtime / per-op cost), so it
moves whenever per-op cost moves — which is exactly what a perf change does.
Measured on `c7a.2xlarge`, every `pkg/vm` benchmark's `b.N` moved 5–49% across a
single session. Two commits compared at different N were compared under
different protocols wherever `ns/op` depends on N.

**Pin both packages.** Where the curve is flat, pinning costs nothing and makes
the question moot; where it is not, pinning is the only thing that removes the
confound. Do not reason per benchmark about which case you are in — that is the
reasoning the session showed to be unreliable.

**Never pin `BenchmarkRatchetAnchor`.** Every ratio in the corpus is
`bench_ns / anchor_ns`, so moving the anchor's `b.N` shifts the whole timeline
against itself by an amount nobody measured. `bench-ratchet` rejects a pin table
that names it, as a hard error rather than a warning.

Feed the calibration straight back in:

```bash
go run ./cmd/bench-ratchet -pins calib-tests.json -count 3 -benchtime 1s snapshot ...
# or, through the session driver:
scripts/perf-session.sh --pins calib-tests.json --reducer min ...
```

`-pins` splits each package into one `go test` invocation per distinct
benchtime, so every benchmark is measured exactly once at its own pinned N, and
records the table in `method.pins` — a snapshot that used several benchtimes
must not carry a single `method.benchtime` claiming otherwise. A pin matching
no benchmark warns rather than being dropped, because a table that has silently
stopped applying still reads as if it pinned.

Treat "NO FLAT REGION" or "NOT EVALUATED" as a result to act on rather than a
line to skim past — those benchmarks stay unpinned and keep the global
`-benchtime`.

`bench-ratchet` warns on every run when a benchmark lands under its `b.N`
floor (`-min-iterations`, default 20; `-strict-iterations` to fail), and
separately when `b.N` **moves** across reps (`-iteration-tolerance`).

**The second warning is the one that invalidates a comparison.** `ns/op` is a
function of `N` wherever iterations share state, so two commits measured at
different `N` were measured under different protocols. A low `b.N` on its own
costs *averaging* — `ns/op` is the mean over `N`, variance falls as `√N`, and at
`N`=1 one slow iteration is the entire reading. It is not quantisation: Go
divides elapsed nanoseconds by `N`, so the quantum is `(1 ns)/N`, which on a
700 ms/op benchmark at `N`=1 is 1.3e-07% of the value.

This paragraph previously said the numbers were "quantised" and that any delta
smaller than `1/N` was unreal. That was wrong by nine orders of magnitude,
withdrawn in `5fb6f8ee`, and corrected publicly. `SetIndex` runs at `b.N`=2 and
is the tightest benchmark in the suite; `MatrixMult` runs at 139–261 and is the
worst. Size is not the signal — movement is.

## 4. Run a null control

Include at least one pair of commits that **cannot** differ. They must tie. If
they don't, the gap is your **per-benchmark attribution floor**, and any delta
below it is not attributable to code however clean the measurement looks.

**Check the pair can RUN the suite before checking it cannot differ.** The
2026-08-03 session died on its second commit because all four historical
controls — picked by scanning history for no-op diffs and verified to compile
byte-identically — predate the benchmark harness. Zero benchmarks in `pkg/vm`,
4 of 7 in `./tests`, and no `BenchmarkRatchetAnchor` to normalise against.
Compiling was never the question that mattered.

The reliable move is to **construct** the controls on the base you are
measuring, so they share its engine by construction:

- **noise floor** — a comment-only edit in `pkg/vm`. Verify the test binaries
  hash identically on both families.
- **layout floor** — a real but never-called function. One in `pkg/modules`
  moves the `./tests` binary; `pkg/vm` does not link `pkg/modules`, so it needs
  its own.

Measured this way on `c7a.2xlarge`: byte-identical binaries land **3.7% apart**
on `./tests` and 1.1% on `pkg/vm`, and the layout floor is *not* distinguishable
from the noise floor.

This is not optional and not an error bar. reg-lisp measured a *reproducible*
4.2% between two builds differing only at compile time, precision ±0.28% —
code placement, not noise, and per-benchmark (`fib` ±4.3% while `tak` ±0.5% in
the same run). A robust summary statistic does not remove it.

## 5. Measure

```bash
tmux new -s perf
scripts/perf-session.sh --rounds 5 --count 3 --benchtime 1s -o ~/session <sha>...
```

Commits are interleaved by round and counterbalanced within each round
(reversed on even rounds, rotation offset advancing per pair). Do not "fix"
that into a fixed order.

Use `--macro` only if the micro result earns it: it adds ~13 min per commit per
round, so 16 commits × 5 rounds is ~17 hours.

**Watch round 1.** The classic failure is a chained job nobody watched, dying
on its first measurement while the box sits at load 0.00 for 90 minutes.

## 6. Read the anchor before reading anything else

`perf-session.sh` prints anchor drift across rounds. Above 2% the machine did
not hold still and small deltas mean nothing.

Know what the anchor does and does not certify: it runs in a *different
`go test` process* from `./tests`, at whatever `b.N` the global `-benchtime`
gives it. It once read 0.031% drift across a session whose headline benchmark
moved 70% — and that 0.031% was not drift at all but the two-level quantum of a
four-significant-digit value.

What it **does** certify, measured: the same commit measured in two sessions on
boxes whose anchors read 1.084 and 1.378 ns/op — **27.1% apart** — agreed on
`ratio_to_anchor` to a **median of 0.30%** across 35 benchmarks. Absolute
`ns/op` does not survive across sessions; the ratio does.

What it does **not**: it is subject to transients of about 4% lasting several
launches. A median across rounds absorbs one; it does not absorb two of four,
which is how the 2026-08-03 session understated a real 23% win as 13%. Read the
per-round anchors, not just the summary drift — two rounds reading 1.316 against
a session median of 1.376 is the signature.

## 7. Publish

```bash
scripts/perf-session-publish.sh ~/session            # dry run — always first
scripts/perf-session-publish.sh --commit ~/session
scripts/perf-session-publish.sh --commit --push ~/session
```

The script refuses a session whose anchor drifted and runs `perf-fixratio
-verify` before committing. Let both gates work rather than reaching for
`--force`. `--force` is for deliberately replacing points from an earlier
session so the whole tier comes from one afternoon — which is what the
preflight tag exists to make safe.

## 8. Teardown, verified by re-query

Do not trust the console.

```bash
aws ec2 describe-instances --region <r> \
  --filters 'Name=instance-state-name,Values=running,stopped' --query 'length(Reservations)'
aws ec2 describe-key-pairs --region <r> --query 'length(KeyPairs)'
aws ec2 describe-security-groups --region <r> \
  --query "length(SecurityGroups[?GroupName!='default'])"
```

Expect `0 0 0`, in **every region you touched**. Check for orphaned volumes and
Elastic IPs too — a terminated instance can leave both, and both bill.

## 9. Bring the result back to the implementations

The step that has historically been tribal knowledge. A session produces two
kinds of output — numbers, and findings about the *measurement itself* — and
only the first has a scripted path home.

Where a measurement finding lands:

| finding is about | goes to |
|---|---|
| reduction across `-count` reps | `cmd/bench-ratchet` (`aggregateFromFile`, `reducerName`) |
| reduction across rounds | `scripts/perf-session.sh`, the combine step |
| `b.N` / benchtime | `cmd/bench-ratchet` defaults, `perf-pr.yml`, `perf-timeline.yml` |
| machine keying, tiers | `cmd/perf-migrate` |
| the `ratio_to_anchor` invariant | `cmd/perf-fixratio` |
| which commits get measured | `perf-timeline.yml` (`paths:` allow-list, engine key) |
| how the series is drawn | `docs/perf/index.html` |
| the corpus itself | `perf-data`, via `perf-session-publish.sh` only |

**The ordering rule, which is the whole point of this section:** a tool change
alters how a measurement is summarized, so a session run before it is not
comparable with one run after. Land every tool change *before* the session that
is meant to supersede a tier — never between a session and its re-measure. Two
tool changes and one session is one afternoon; one tool change, a session, then
a second tool change is two sessions and $3.

When a change lands, say so where the claim lives: update the issue, and mark
the draft in `project-docs/docs/paserati/bench-ratchet-issues-draft.md`.
Findings that are fork-only (anything touching `scripts/perf-session.sh` or
`scripts/ab_repeat.py`, which do not exist upstream) land as code here and are
recorded there rather than filed.

## Known failure modes

- **Small n lies.** One session's headline noise figure went 1.03% (n=6) → 13%
  (n=15) → ~6% (n=31) as the sample grew. Treat any single number as
  provisional until it replicates.
- **`max/min − 1` is poison.** It is set by the single worst observation and
  grows with n. Reported over n=5 it manufactured a four-commit "instability"
  finding and three wrong explanations, and cost two public retractions. Same
  data: MAD/median 0.15% where it said 3.30%. Use MAD or IQR and report the
  tail separately.
- **The distribution is a tight core plus a one-sided slow tail.** ~2 launches
  in 20 land +15–29% high, including on control commits. min/trimmed-median is
  therefore the *right* reducer family — do not "fix" it.
- **zsh does not word-split unquoted variables.** `$SSH 'cmd'` fails silently
  as a single command name, and `for s in $DROP` silently matches nothing. Use
  wrapper scripts and arrays.
- **Worktree registration outlives the directory.** `rm -rf` alone leaves it
  registered and the next `worktree add` dies with "missing but already
  registered". Always remove → rm → prune.

## What counts as a result

A per-commit table with error bars, and for each commit a plain statement:
moved, didn't move, or **below resolution**. "Below resolution" is a real
answer and reporting it as one is the point of measuring. So is "below this
benchmark's attribution floor" — see step 4.
