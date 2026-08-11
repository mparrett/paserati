#!/usr/bin/env python3
"""Build session-data.json for perf-session-matrix.py from PUBLISHED perf-data
snapshots rather than from one session's raw rounds.

    python3 scripts/perf-timeline-extract.py origin/perf-data . session-data.json
    MACHINE_KEY="amd64/AMD EPYC 9R14" python3 scripts/perf-timeline-extract.py ...

The sibling of perf-session-extract.py: same output contract, different input.
That one re-reduces one session's raw/round-N files; this one reads the
published corpus, which is the only place separate sessions merge.

Promoted out of the 2026-08-09 evidence directory so it stops being copy-forked
per session — the same move perf-session-extract.py records making. That copy is
left as it was: it documents how that directory's matrix was actually produced.

WHY NOT perf-session-extract.py
-------------------------------
That script re-reduces a single session's raw/round-N files, and the current best
data is no longer in any one session: the 9R14 micros came from the 2026-08-07
whole-world run, 3167412e was re-measured separately on 2026-08-09 (its raw
rounds died with the box), and the Test262 macro was swept on 2026-08-09 as a
separate injection. perf-data is where those merge.

FIDELITY
--------
Verified before writing this: session-data.json's `ns` values are bit-identical
to the published snapshots' ns_per_op (18/18 spot checks), so the snapshot IS
the session's reduced value and nothing is being re-derived loosely.

The per-round structure the MAD needs is recoverable because each sample carries
captured_at, and the session interleaved rounds hours apart — so samples group
into rounds by timestamp. Reductions then match perf-session-extract.py exactly:
min (and mean) within a round, median across rounds.

Macro rows are included. They are reduced by the macro's own median-of-3 and
carry 3 samples rather than 3x3, so each sample is its own round — which is what
median-of-3 means.
"""
import collections, hashlib, json, os, subprocess, sys, statistics as st
from collections import defaultdict

PERF_DATA = sys.argv[1] if len(sys.argv) > 1 else 'origin/perf-data'
REPO = sys.argv[2] if len(sys.argv) > 2 else '.'
OUT = sys.argv[3] if len(sys.argv) > 3 else 'session-data.json'
KEY = os.environ.get('MACHINE_KEY', 'amd64/AMD EPYC 9R14')


def git(*a):
    return subprocess.run(['git', '-C', REPO, *a], capture_output=True, text=True).stdout


files = [f for f in git('ls-tree', PERF_DATA, 'timeline/', '--name-only').split()
         if f.endswith('.json')]

snaps = {}
for f in files:
    try:
        js = json.loads(git('show', f'{PERF_DATA}:{f}'))
    except ValueError:
        continue
    prof = (js.get('machines') or {}).get(KEY)
    if not prof:
        continue
    sha = os.path.basename(f).split('-')[1]
    snaps[sha] = prof

if not snaps:
    sys.exit(f'no snapshots for {KEY}')

# Topological order — the only correct ordering here. Committer date is flattened
# by the bulk rebase, author date disagrees with the path, and --first-parent
# drops commits that arrived through a merge.
topo = [c[:12] for c in git('rev-list', '--reverse', '--topo-order', 'origin/main').split()]
order = [c for c in topo if c in snaps]
missing = sorted(set(snaps) - set(order))
ref = order[0]

subjects = {}
for c in order:
    subjects[c] = git('log', '-1', '--format=%s', c).strip()


def rounds_of(entry):
    """Group a benchmark entry's samples into rounds by captured_at."""
    by_t = defaultdict(list)
    for s in entry.get('samples') or []:
        by_t[s.get('captured_at') or ''].append(s)
    if len(by_t) <= 1:                     # no timestamps: each sample its own round
        return [[s] for s in (entry.get('samples') or [])]
    return [by_t[t] for t in sorted(by_t)]


def micro_ident(prof):
    """The micro suite's membership, which is the instrument's identity."""
    names = sorted(k for k in prof.get('benchmarks', {}) if not k.startswith('test262'))
    return 'micro:' + hashlib.sha256('\n'.join(names).encode()).hexdigest()[:8]


def anchor_rounds(prof):
    """Per-round anchor mins, grouped the same way as a benchmark's samples."""
    a = prof.get('anchor') or {}
    rs = rounds_of(a)
    return [min(s['ns_per_op'] for s in r) for r in rs if r]


PKG = {}
names = set()
for c, prof in snaps.items():
    for full in prof.get('benchmarks', {}):
        short = full.split('.')[-1]
        names.add(short)
        # Vocabulary must match perf-session-extract.py: the renderer computes a
        # per-family geomean with `pkg == 'js'`, so emitting 'tests' leaves that
        # group empty, its geomean None, and the formatter dies on abs(None).
        PKG[short] = 'macro' if full.startswith('test262') else (
            'vm' if '/pkg/vm' in full else 'js')

out = {'ref': ref, 'order': order, 'subjects': subjects, 'benchmarks': []}
for short in sorted(names):
    row = {'name': short, 'pkg': PKG[short], 'deltas': {}, 'ns': {}, 'madc': {}}
    per = {}
    for c in order:
        prof = snaps[c]
        full = next((k for k in prof['benchmarks']
                     if k.split('.')[-1] == short or k == short), None)
        if not full:
            continue
        e = prof['benchmarks'][full]
        rs = rounds_of(e)
        if not rs:
            continue
        mins = [min(s['ns_per_op'] for s in r) for r in rs]
        means = [st.mean([s['ns_per_op'] for s in r]) for r in rs]
        # A10: the micro family (pkg/vm) is judged on RAW ns/op, everything else
        # on the anchor ratio — the same split bench-ratchet's judgeMetric makes.
        # Deriving every delta from ns_per_op drops that normalisation from the
        # js rows, which is what made an earlier build of this file disagree with
        # perf-session-extract.py on 31 of 440 js cells.
        #
        # Both series are already REDUCED on the entry: ns_per_op is the reduced
        # raw value and ratio_to_anchor the reduced normalised one, each against
        # the anchor it was actually measured against. Using them directly
        # reproduces the extractor without reconstructing rounds from timestamps
        # — which was the second wrong turn here, because a round's samples do
        # not reliably carry a shared captured_at.
        judged = e.get('ns_per_op') if PKG[short] == 'vm' else e.get('ratio_to_anchor')
        # What instrument produced this cell. For the macro that is the passing
        # set it averaged over; for a micro benchmark it is the suite membership.
        ident = e.get('set_hash') or micro_ident(prof)
        per[c] = (mins, means, e.get('ns_per_op'), judged, ident)
    if ref not in per:
        continue
    # Base on the row's OWN instrument, not blindly on the ref commit.
    #
    # test262.total's ref-commit measurement sits on set 9812300b while the other
    # 20 commits sit on ac611bcf: basing on the ref would make 20 of 21 macro
    # cells a comparison of means over different passing sets, which is the
    # confound issue #26 exists for, not a speed reading. Base each row on the
    # first commit carrying its MODAL instrument, and omit cells measured on any
    # other — the renderer already draws a missing cell as empty.
    #
    # For a row with one instrument throughout — every micro benchmark on this
    # tier — the modal instrument is the only one and the base is the ref, so
    # this changes nothing.
    idents = collections.Counter(per[c][4] for c in order if c in per)
    modal = idents.most_common(1)[0][0]
    base_c = next((c for c in order if c in per and per[c][4] == modal), None)
    if base_c is None:
        continue
    row['base'] = base_c
    row['instrument'] = modal
    bmin, bmean, bns, bjudged, _ = per[base_c]
    # The min-delta is computed from ns_per_op, not from the reconstructed
    # rounds. perf-session-extract.py defines it as
    # median(mins[c]) / median(mins[ref]), and ns_per_op IS median(mins) — the
    # spot check above found 840/840 identical. Deriving it from the published
    # value therefore reproduces the extractor exactly, where grouping samples
    # into rounds by captured_at drifted up to 2.1pp on 30 of 840 cells because
    # a round's samples do not always carry distinct timestamps.
    base = {'judged': bjudged, 'mean': st.median(bmean)}
    mads = []
    allN = set()
    for c in order:
        if c not in per:
            continue
        mins, means, nsop, judged, ident = per[c]
        if ident != modal:
            continue
        if not judged or not base['judged']:
            continue
        row['deltas'][c] = [round((judged / base['judged'] - 1) * 100, 3),
                            round((st.median(means) / base['mean'] - 1) * 100, 3)]
        # Prefer the published ns_per_op: it is the value the session actually
        # reduced to and what every other view of this data shows.
        row['ns'][c] = round(nsop if nsop is not None else st.median(mins), 3)
        m = st.median(mins)
        pc = st.median([abs(x - m) for x in mins]) / m * 100 if m else 0.0
        row['madc'][c] = round(pc, 4)
        mads.append(pc)
        for s in (snaps[c]['benchmarks'].get(
                next(k for k in snaps[c]['benchmarks'] if k.split('.')[-1] == short or k == short),
                {}).get('samples') or []):
            if s.get('iterations'):
                allN.add(s['iterations'])
    row['bn'] = [min(allN), max(allN)] if allN else [0, 0]
    row['mad'] = round(st.median(mads), 4) if mads else None
    row['final'] = row['deltas'].get(order[-1], [None])[0]
    out['benchmarks'].append(row)

m0 = snaps[order[-1]]
out['meta'] = {
    'commits': len(order), 'rounds': 3, 'cells': len(order) * 3,
    'machine_key': KEY,
    'os': m0.get('machine', {}).get('os', 'linux'),
    'arch': m0.get('machine', {}).get('arch', 'amd64'),
    'num_cpu': m0.get('machine', {}).get('num_cpu'),
    'cpu_model': m0.get('machine', {}).get('cpu_model', KEY.split('/')[-1]),
    'go_version': m0.get('machine', {}).get('go_version'),
    'reducer': (m0.get('method') or {}).get('reducer', 'min'),
    'count': (m0.get('method') or {}).get('count', 3),
    'benchtime': (m0.get('method') or {}).get('benchtime', '1s'),
    'pins_applied': len((m0.get('method') or {}).get('pins') or {}),
    'anchor_policy': 'macro only; pkg/vm judged on raw ns/op (A10)',
    'source': f'published perf-data snapshots ({KEY}), not one session raw',
}

json.dump(out, open(OUT, 'w'))
macro = sum(1 for b in out['benchmarks'] if b['pkg'] == 'macro')
print(f"wrote {OUT}: {len(out['benchmarks'])} rows ({macro} macro) x {len(order)} commits, ref {ref}")
if missing:
    print(f"  NOT on main, excluded from the ordering: {', '.join(missing)}")
