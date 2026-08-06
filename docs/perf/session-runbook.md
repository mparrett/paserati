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

**Read the benchmark register first**
(`project-docs/docs/paserati/benchmark-defects.md`). Ten of the thirty-five
benchmarks in this corpus measure engine startup rather than what they are named
for, one compiles non-deterministically, and a few depend on `b.N`. Knowing
which before you interpret a number is cheaper than discovering it after.

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

**The Octane workloads (corpus v4) need their own treatment.** They run at
116–489 ms per op, so the default 1..256 sweep is the wrong range in the other
direction from `pkg/vm`: at a 1s benchtime `go test` picks `b.N` of 1 or 2, and
which one it picks can differ between commits. They are top-level `Benchmark`
funcs specifically so they *can* be pinned — pin them at 1 and take samples with
`-count` instead. Their per-op iteration counts, baked into
`tests/scripts/octane/*-run.js`, were derived on a laptop and are provisional;
re-derive them here if any workload lands outside roughly 0.3–0.5s.

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

## 5b. Bisecting instead, when the history is long

Measuring every commit spends most of its cells inside flat stretches. If the
question is *which commit did this* rather than *what does the whole history
look like*, bisect: `scripts/perf-bisect.sh` measures three points, classifies
each benchmark as step, ramp or flat, and then spends one cell per level on
whichever commit splits the most outstanding change.

Steps 1, 2, 3, 4, 6 and 8 are unchanged. What differs is that **the box stays up
between levels**, because that is what makes a level cost one cell instead of
three. Everything below is about protecting that.

### Before you boot: two things that will otherwise fail mid-run

**Push the orphaned commits.** As of 2026-08-06, two of the whole-world run's 21
commits (`1a3857ac`, `21c5e929`) exist only in one laptop's clone under
`refs/preserve/perf-corpus/*` — and `1a3857ac` is the older endpoint, so a fresh
box cannot check out level 0. Push them somewhere the box can fetch first:

```bash
git for-each-ref refs/preserve/perf-corpus --format='%(refname)' \
  | while read -r r; do git push origin "$r:$r"; done
git ls-remote origin 'refs/preserve/*' | wc -l     # expect 14
```

`perf-bisect.sh` refuses to start when a listed commit is unresolvable, so this
fails at the preflight rather than three hours in — but only in a clone that
already has them.

**Supply the commit list; do not let it be derived.** `1a3857ac` is *not* an
ancestor of `20f7bf60`; the perf branches were rebased, so a `rev-list` range
returns 91 commits off a divergent line and every bisect position after that is
meaningless. `perf-bisect.sh` refuses to guess when the endpoints are not
linearly related. Pass the measured set:

```bash
scripts/perf-bisect.sh --commits <evidence>/2026-08-06-whole-world/measure-list.txt ...
```

The script sorts it by **author** date. Do not sort by commit date and do not
trust the file's own order: the bulk rebase stamped most of the corpus with a
single commit date, `measure-list.txt` is not stored in history order, and
author date is the only monotone axis that survived.

### The run

```bash
tmux new -s bisect
EV=<evidence>/2026-08-06-whole-world

# Level 0 — the three-point probe. ~1h at 3 rounds. Stop and read it.
scripts/perf-bisect.sh -o ~/bisect --probe \
  --commits "$EV/measure-list.txt" \
  --corpus docs/perf/bench-corpus.json --pins calib-tests.json \
  1a3857acaa6b 20f7bf60402d
```

Read the **shape** table before spending anything else. A *step* has a culprit
commit and subdividing will find it. A *ramp* is accumulated change with no
single cause, and bisection will subdivide it until `--threshold` stops it and
still hand you nothing — against a ramp, stop as soon as the resolution is
enough. Knowing which costs one session rather than five.

Then subdivide, a level at a time, re-reading between:

```bash
scripts/perf-bisect.sh -o ~/bisect --levels 1 \
  --commits "$EV/measure-list.txt" \
  --corpus docs/perf/bench-corpus.json --pins calib-tests.json \
  1a3857acaa6b 20f7bf60402d
```

Same command every time; it picks up where it left off. `--levels N` runs N of
them unattended.

### Keeping the box alive is the whole design

The dead-man switch defaults to **300 minutes and a real bisect exceeds it**.
Extend it *before* starting, not at 3am:

```bash
sudo shutdown -c
sudo shutdown -h +600
```

`--instance-initiated-shutdown-behavior terminate` is an instance attribute and
survives, so the extended switch still tears the box down rather than stopping
it. Do not disable the switch outright; a forgotten box is a live failure mode
in this account.

### What makes cross-level comparison legitimate

Levels are separate sessions, so they are only comparable because they are the
same physical box and because `ratio_to_anchor` is what gets compared —
`perf-micro-noise-and-anchor-results.md` measured the anchor absorbing 100% of a
22.4% hardware split inside one CPU model. That is an assumption, so it is
checked: `perf-bisect-select.py` **refuses to run** when levels span machine
keys, and each level's anchor drift is carried into the report. If the box has
to be replaced mid-bisect, the earlier levels are gone — start over rather than
mixing keys.

### What this run should find, if it is working

`1c6c7f66` ("replace IsObject OR-chain with a range check") sits at author
position 11 of 21, and the whole-world run put `IsObject` at −85.66%. A working
bisect should localise `BenchmarkIsObject` to that commit. It is worth writing
that prediction down before the run, because a bisect that confirms whatever you
already believed is not evidence.

Expect the magnitude to come in **lower than −85.66%**: that figure was measured
under corpus v2, where the benchmark's loop body was dead-code eliminated at
commits where `IsObject` inlines. Corpus v3 escaped the accumulator, and a
laptop check put the DCE-proof figure near −78%. If v4 reproduces ~−86%, the
corpus overlay is not taking effect — check the resolved corpus SHA in the
session metadata before believing the number.

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
