#!/usr/bin/env python3
"""Re-reduce the session's raw rounds to min, and emit the data both pages render.

    python3 extract-min-reduced.py . min-reduced.json

Protocol, matching the session's own: within a round, reduce the -count 3 samples
by min; across rounds, take the median; normalize each round against THAT round's
own anchor before combining, never a foreign one. The mean column is kept beside
the min so the reducer's effect stays checkable rather than asserted.

Absolute ns/op is carried too — a percentage alone cannot say whether −0.4% is
four nanoseconds or four milliseconds, and on this suite the per-op cost spans
seven orders of magnitude.

A10 — the micro family is NOT anchor-normalised. BenchmarkRatchetAnchor resolves
to one ulp of a four-digit ~1.1ns value, 0.0923%, and most pkg/vm cells measure
tighter than that, so dividing by it can only inject a quantisation square wave.
bench-ratchet judges that family on raw ns/op for the same reason (judgeMetric),
and a page that normalised where the ratchet does not would give a different
answer from the same data. The macro family keeps the anchor, where it works.

Promoted out of the 2026-07-31 evidence directory so it stops being copy-forked
per session. That copy is left as it was: it documents how the published page of
that session was actually produced.
"""
import json, glob, os, re, sys, subprocess, statistics as st
from collections import defaultdict

SRC = sys.argv[1] if len(sys.argv) > 1 else '.'
OUT = sys.argv[2] if len(sys.argv) > 2 else 'min-reduced.json'
REPO = os.environ.get('PASERATI_REPO',
                      os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def rmin(v):
    return min(v)


def rmean(v):
    return sum(v) / len(v)


# Commit order comes from the snapshot filenames' timestamp prefix.
stamps = {}
for f in glob.glob(os.path.join(SRC, 'snapshots', '*.json')):
    base = os.path.basename(f)[:-5]
    m = re.search(r'([0-9a-f]{12})', base)
    if m:
        stamps[m.group(1)] = base.split('-')[0]
order = sorted(stamps, key=lambda s: stamps[s])
ref = order[0]

subjects = {}
for s in order:
    try:
        subjects[s] = subprocess.run(['git', '-C', REPO, 'log', '-1', '--format=%s', s],
                                     capture_output=True, text=True).stdout.strip()
    except Exception:
        subjects[s] = ''

# ratios[reducer][commit][bench] = [per-round ratio]; ns likewise, plus raw samples
R = {'min': defaultdict(lambda: defaultdict(list)), 'mean': defaultdict(lambda: defaultdict(list))}
NS = defaultdict(lambda: defaultdict(list))
ITERS = defaultdict(lambda: defaultdict(set))
RAW = defaultdict(lambda: defaultdict(list))
PKG = {}

for rd in sorted(glob.glob(os.path.join(SRC, 'raw', 'round-*'))):
    for f in glob.glob(os.path.join(rd, '*.json')):
        sha = os.path.basename(f)[:-5]
        if sha not in stamps:
            continue
        d = json.load(open(f))
        a = [x['ns_per_op'] for x in d.get('anchor', {}).get('samples', [])]
        if not a:
            continue
        A = {'min': rmin(a), 'mean': rmean(a)}
        for name, e in d.get('benchmarks', {}).items():
            if 'test262' in name:
                continue
            v = [x['ns_per_op'] for x in e.get('samples', [])]
            if len(v) < 3:
                continue
            PKG[name] = 'vm' if '/pkg/vm' in name else 'js'
            RAW[name][sha] += v
            for x in e['samples']:
                ITERS[name][sha].add(x['iterations'])
            NS[name][sha].append(rmin(v))
            # A10: micro is judged on raw ns/op, macro on the anchor ratio.
            # Same policy as bench-ratchet's judgeMetric, so the page and the
            # ratchet cannot disagree about the same measurement.
            micro = PKG[name] == 'vm'
            for k, val in (('min', rmin(v)), ('mean', rmean(v))):
                if micro:
                    R[k][sha][name].append(val)
                elif A[k] > 0:
                    R[k][sha][name].append(val / A[k])

# Session shape, so the renderer does not have to hardcode it. The page that
# first consumed this file baked in "16 commits x 5 rounds = 80 builds" and
# "micro suite only" as prose, which was true of the session it was written for
# and silently false for every other one.
meta = {'commits': len(order), 'rounds': 0, 'cells': 0}
meta['rounds'] = len(glob.glob(os.path.join(SRC, 'raw', 'round-*')))
meta['cells'] = len(glob.glob(os.path.join(SRC, 'raw', 'round-*', '*.json')))
_snaps = sorted(glob.glob(os.path.join(SRC, 'snapshots', '*.json')))
if _snaps:
    _d = json.load(open(_snaps[0]))
    _k, _m = next(iter(_d.get('machines', {}).items()), (None, {}))
    meta['machine_key'] = _k
    meta.update({k: v for k, v in _m.get('machine', {}).items()
                 if k in ('cpu_model', 'go_version', 'num_cpu', 'os', 'arch')})
    _meth = _m.get('method', {})
    meta.update({k: _meth[k] for k in ('reducer', 'count', 'benchtime') if k in _meth})
    meta['pins_applied'] = len(_meth.get('pins', {}))
# Recorded because the page must not claim otherwise: A10 means the micro family
# is NOT anchor-normalised, so a footer asserting it is would be false.
meta['anchor_policy'] = 'macro only; pkg/vm judged on raw ns/op (A10)'

out = {'meta': meta, 'ref': ref, 'order': order, 'subjects': subjects, 'benchmarks': []}
for name in sorted(R['min'][ref]):
    base = {k: st.median(R[k][ref][name]) for k in ('min', 'mean')}
    row = {'name': name.split('.')[-1], 'pkg': PKG.get(name, 'vm'),
           'deltas': {}, 'ns': {}, 'madc': {}}
    allN, mads = set(), []
    for c in order:
        if name not in R['min'][c]:
            continue
        row['deltas'][c] = [round((st.median(R[k][c][name]) / base[k] - 1) * 100, 3)
                            for k in ('min', 'mean')]
        # Absolute ns/op under the same protocol: min within a round, median across.
        row['ns'][c] = round(st.median(NS[name][c]), 3)
        allN |= ITERS[name][c]
        v = RAW[name][c]
        m = st.median(v)
        # Per-commit spread as well as the row-wide summary: they disagree by
        # orders of magnitude on some benchmarks (js.Arith ranges 0.02%–21%
        # across the sixteen), and a cell should have to clear both.
        pc = st.median([abs(x - m) for x in v]) / m * 100
        row['madc'][c] = round(pc, 4)
        mads.append(pc)
    row['bn'] = [min(allN), max(allN)] if allN else [0, 0]
    # MAD kept at 4 decimals: the most stable benchmarks sit at 0.014%, and
    # rounding those to 0.00% both hides them and makes the noise test unreadable.
    row['mad'] = round(st.median(mads), 4) if mads else None
    row['final'] = row['deltas'].get(order[-1], [None])[0]
    out['benchmarks'].append(row)

json.dump(out, open(OUT, 'w'))
print(f"wrote {OUT}: {len(out['benchmarks'])} benchmarks x {len(order)} commits, ref {ref}")
