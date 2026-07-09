# Succession handoff — paserati side → let-go session (2026-07-09)

Final handoff before the paserati-side context is spent and ownership transfers.
Prior handoff `HANDOFF_2026-07-06_wasm-jump-wall-decision.md` is the deep record
(wasm effort, coordination protocol, 4-build result, the 07-09 stdgo capstone at
top). This one is the takeover checklist across all live lanes.

## Branch / PR state (all tips verified 2026-07-09)

| Branch | local == origin | Meaning |
|---|---|---|
| `feat/wasm-transpile` | `7a027ea` == `7a027ea` | Wasm transpiler + VM long-jumps + 64-bit stdgo. **Backup, not a PR.** |
| `fix/compiler-jump-overflow-graceful-error` | `f33f702` == `f33f702` | → **PR nooga#17** |
| `showcase/let-go-dev-report` | `1dda175` == `1dda175` | Dev-report `.html`/`.md` for githack |
| `perf/timeline-test262-optin` | `d4cbebc` == `d4cbebc` | → PR nooga#16 (perf lane) |
| `main` | `6c83bf9` == `6c83bf9` | == `upstream/main`, clean PR base |

Worktrees: `~/projects-new/3p/paserati` (perf/timeline-test262-optin),
`~/projects-new/3p/paserati-wasm` (feat/wasm-transpile),
`~/projects-new/3p/paserati-jumpfix` (fix/compiler-jump-overflow).

## 1. `perf/timeline-test262-optin` — in-flight

- **Working tree (checkout `~/projects-new/3p/paserati`):** clean except one
  untracked dir **`docs/project_incoming/`** — present since session start, I did
  **not** touch it or the perf lane this session. Inspect before committing; not
  mine to characterize.
- **The lane:** perf-timeline / Test262 macro-benchmark work — the "are we fast
  yet?" trend page (`docs/perf/index.html`), the `perf-timeline` +
  `perf-test262-backfill` GitHub Actions, and the opt-in `test262.total` macro
  point. Branch is 1 commit ahead of `origin/main` (`d4cbebc`) → **PR nooga#16**
  (open). The 5 perf commits also sit on `origin/main` (fork staging) — see
  gotchas re: **don't reset origin/main**.
- Not worked this session; state is as I found it.

## 2. `feat/wasm-transpile` — done, backed up, no unpushed intent

- Everything committed and **pushed to `origin` as backup** (`7a027ea`). Not a
  PR — the wasm work isn't proposed upstream (no consumer; see the strategic read
  in the prior handoff's 07-09 note).
- Contains: the full transpiler (`pkg/wasm`), additive VM long-jumps
  (`OpJumpLong`/`OpJumpIfFalseLong`, `da2a875`), the register spiller, `-profile`,
  and the 07-09 dead-code + branch-to-return fixes (`56209fc`) that made the
  **64-bit-faithful stock-Go let-go run** (exact 10^18, byte-identical to
  wasmtime). `TestScripts` + `go test ./pkg/wasm ./pkg/vm` green.
- **No pending intent.** If a polyglot-embedding consumer ever appears it could
  become a real direction; absent that, it's a research spike. Do not open it as
  a PR.

## 3. PR nooga#17 — shepherding state

- **State:** OPEN, not draft, `reviewDecision = REVIEW_REQUIRED` (awaiting nooga).
- **What it is:** `compiler.go` `patchJump`/`patchJumpToTarget` **panicked** on
  functions whose jumps exceed ±32 KB (large/bundled/generated TS). Now a graceful
  `PS3001` compile error via `recordJumpOverflow`. Addresses an in-code
  `TODO: proper error handling instead of panic`. +51/−4, one commit `f33f702`,
  regression test included. Body in `project-docs/paserati/collab/PR17-*.md`.
- **This is the one genuinely upstream-valuable output** of the whole wasm track
  (independent of wasm — a real stock-paserati robustness fix).
- **Expected response from nooga:** likely accept (small, correct, closes a TODO)
  or minor asks — error-message wording, or "why not actually *compile* big
  functions via long jumps?" The honest answer to that: the full fix needs
  branch-relaxation in the TS emitter (which emits bytecode directly, not via a
  symbolic list like the wasm side), a much larger change with real regression
  risk across every TS program; this PR is the minimal don't-crash-on-valid-input
  fix. Long-jump opcodes exist on `feat/wasm-transpile` as the future foundation.
- **Fork PR nooga#3 (mparrett-internal)** is just the githack showcase surface —
  not for upstream.

## 4. `project-docs/paserati/INVENTORY.md` — recorded, one gap

- The paserati-side index is committed (`project-docs`, commits `3db8756` →
  `71bed79`): branches, code, tests, docs, shared drop dir, artifacts, PRs, the
  full session ledger (§10), memory, loose-files-made-durable (`collab/`).
- **NOT yet recorded:** the **07-09 stdgo 64-bit result** (`56209fc`/`7a027ea`)
  and the two new codegen fixes. §1's commit list and §2 predate it. **First
  takeover task:** fold the 07-09 result into the inventory (it's in the prior
  handoff verbatim to copy from).
- The let-go side's counterpart is `project-docs/let-go/INVENTORY.md` +
  `ORIENTATION.md §7` (authoritative for their sessions; cross-referenced).

## 5. Gotchas for the next owner

- **NEVER reset/force-sync `origin/main` to upstream** — it hosts the live perf CI
  workflows + the GitHub Pages dashboard + re-seedable baseline (only run from the
  default branch). "5 ahead of upstream" is the dashboard, not drift. See
  `project-docs/docs/paserati/project_notes/key_facts.md` → "Fork remote" and
  memory `paserati-fork-role`.
- **project-docs is mid-migration** — `git status` there shows staged files +
  `MIGRATION.md` from other sessions. Always **pathspec-scope your commits**
  (`git commit -m … -- <your paths>`) so you don't sweep up others' staged work.
- **Coordination protocol** (shared `PROTOCOL.md` + memory): NOTE-*.md message bus
  in `scratch/letgo-wasi-targets/` (measured-result-first), tmux doorbell = ping
  not letter (`send-keys -l` + Enter ×2 + `capture-pane` to confirm), identify the
  let-go pane by worktree not name (`joint-xsofy-5324` this session), each side
  builds from its own tree.
- **Running the stdgo build:** `./paserati-wasm -run lg-stdgo-wasip1.wasm` (NO `-`
  arg — this build treats `-` as a filename; it's a REPL reading stdin). ~7s to
  first result, doesn't EOF-exit (reap with `timeout`). Runtime-only TinyGo builds
  DO use `- < sample.lgb`.
- **Test262:** repro with `--no-typecheck`; type-checker fixes don't move numbers;
  rebuild before retest; flaky-in-batch: `language/statements/for-of/{map,*}`.
- **Artifacts:** browser "Save Page As" of a claude.ai artifact saves the empty
  viewer *shell*; pull real self-contained HTML via `WebFetch` on the artifact URL.
- **Session discovery:** `claude-graph seed <id>`, `claude-ls info/search/resume`,
  handoff frontmatter `Session ID`. Paserati effort sessions: `00905e77`,
  `d56f4c88`, `128ec250` (this one). Inventory §10 has the full ledger.
- **Memory auto-loads** each session: `paserati-fork-role`,
  `cross-claude-coordination-protocol`, `project-wasm-interop`, etc.

## Immediate takeover checklist

1. Read the prior handoff's top (07-09 result) + this file.
2. Fold the 07-09 stdgo result into `project-docs/paserati/INVENTORY.md` (§1/§2).
3. Decide `perf/timeline-test262-optin`: is `docs/project_incoming/` meant to be
   committed? (Not mine to judge.)
4. Watch PR nooga#17 for review; respond per §3.
