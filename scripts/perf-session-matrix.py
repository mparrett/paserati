#!/usr/bin/env python3
"""A second, independent take on the same session data — built by the dataviz
method rather than by extending the existing page.

Form choice: the data is N benchmarks x M commits of SIGNED change, which is a
grid whose job is polarity. That is a diverging heatmap, and it answers a
question the endpoint bar charts cannot — WHERE in the stack each change
happened, for everything at once.

The one non-standard choice: the neutral midpoint bin is per-benchmark NOISE
(|delta| < 2x that benchmark's own MAD), not a fixed percentage. A fixed
threshold would paint 0.4% on a quiet benchmark the same as 0.4% on a noisy one.
"""
import json, sys, html, math

D = json.load(open(sys.argv[1]))
OUT = sys.argv[2]
order, ref = D['order'], D['ref']
# Session shape and the worked example below both come from the data. The
# original hardcoded "16 commits x 5 rounds", "sixteen MADs" and a js.Arith
# example with literal numbers — all true of the session it was written for and
# silently false for any other.
M = D.get('meta', {})
NCOM = M.get('commits', len(order))
NRND = M.get('rounds', 0)
NCNT = M.get('count')
REPS = (NRND * NCNT) if (NRND and NCNT) else None
HOST = M.get('cpu_model', 'unknown host')
ANCHOR_POLICY = M.get('anchor_policy', '')
# Widest per-commit MAD span in this session — the illustrative case for why a
# row-wide figure alone is unsafe.
_ex = None
for _row in D['benchmarks']:
    _m = [v for v in (_row.get('madc') or {}).values() if v is not None]
    if len(_m) > 2:
        _sp = (min(_m), max(_m))
        if _ex is None or (_sp[1] - _sp[0]) > (_ex[1][1] - _ex[1][0]):
            _ex = (_row['name'], _sp)
EX_NAME, EX_LO, EX_HI = (_ex[0], _ex[1][0], _ex[1][1]) if _ex else ('', 0.0, 0.0)
NOISE_K = 2.0
# A step delta is the DIFFERENCE of two measurements, each carrying that
# benchmark's spread, so its noise floor is wider by sqrt(2). Reusing the
# cumulative threshold here would call step noise a result.
NOISE_K_STEP = 2.0 * (2 ** 0.5)
# A relative floor alone is not enough. The most stable benchmarks here have a
# MAD of 0.014%, so 2x MAD would certify a 0.03% "regression" — below anything
# this apparatus can resolve. The anchor every delta is normalised against took
# exactly two values all session (1.084/1.085 ns/op), one ulp of Go's 4-digit
# output apart, so its own quantum is 0.0923%: a normalised delta finer than that
# is below the resolution of its own reference. The re-reduction likewise
# reproduces the published numbers only to 0.09%. See mad-space-analysis.py.
FLOOR_ABS = 0.1
# Only label the rows that carry a headline. A number on all 35 rows is chaos and
# goes unread; the rest stay reachable on hover and in the table view.
LABEL_MIN = 5.0

# Diverging arms, lightness-matched and monotonic (computed, not eyeballed):
# blue L .812/.671/.527 vs red .849/.669/.486, max mirror mismatch dL 0.041.
BINS = [(20, 'f3'), (5, 'f2'), (0, 'f1')]  # |delta| thresholds, faster arm


def esc(s):
    return html.escape(str(s), quote=True)


def name_of(b):
    return f"{b['pkg']}.{b['name'].replace('Benchmark', '')}"


def clip(n, k):
    return n if len(n) <= k else '\u2026' + n[-(k - 1):]


def fmt(v):
    # Two decimals under 1%: rounding 0.03% to "+0.0%" and then colouring the
    # cell is what made small cells look like a bug.
    return (f"{v:+.2f}%" if abs(v) < 1 else f"{v:+.1f}%").replace('-', '−')


def fmt_mad(m):
    return (f"{m:.3f}%" if m < 0.1 else f"{m:.2f}%")


def fmt_ns(v):
    """Absolute change, in whatever unit keeps it readable — per-op cost on this
    suite spans 69 ns to 0.78 s."""
    a = abs(v)
    for lim, div, unit in ((1e3, 1, 'ns'), (1e6, 1e3, 'µs'), (1e9, 1e6, 'ms')):
        if a < lim:
            return f"{v / div:+.2f} {unit}".replace('-', '−')
    return f"{v / 1e9:+.3f} s".replace('-', '−')


def mult(v):
    """The same change as a multiplier. A −85.7% delta means the new time is
    0.143x the old, i.e. 7.0x faster; +83.4% means 1.83x slower. Percentages
    compress at the fast end — −85.7% and −87% look adjacent and are 7.0x and
    7.7x — so both readings are worth carrying."""
    r = 1 + v / 100.0
    if r <= 0:
        return ''
    m = (1 / r) if v < 0 else r
    # Below the headline threshold the multiplier restates the percentage in a
    # unit that flatters it -- "1.04x faster" off a 4% change reads as a result.
    if abs(v) < LABEL_MIN or round(m, 2) == 1.00:
        return ''
    txt = f"{m:.1f}\u00d7" if m >= 2 else f"{m:.2f}\u00d7"
    return f"{txt} {'faster' if v < 0 else 'slower'}"


def step_delta(cur, prev):
    """Change from the preceding commit — the period return, where the
    cumulative mode is the return from t0. Both are recoverable from deltas
    against the reference: ratio(c)/ratio(c-1) = (1+d_c)/(1+d_prev)."""
    a, b = 1 + cur / 100.0, 1 + prev / 100.0
    return None if b == 0 else (a / b - 1) * 100.0


def eff_mad(b, c):
    """The stricter of the benchmark's typical spread and the spread actually
    observed at this commit. Neither alone is safe: the row-wide median lets a
    commit that scattered 21% certify an 11.6% move, and a per-commit MAD that
    came out tight by luck lets anything through."""
    row = b.get('mad')
    per = (b.get('madc') or {}).get(c)
    vals = [x for x in (row, per) if x is not None]
    return max(vals) if vals else None


def cls_for(v, mad, k=NOISE_K):
    # A MAD of exactly zero is what perfect repeatability and a degenerate sample
    # look like alike, so it cannot be read as infinite confidence: `abs(v) < 0`
    # is false for every v, which would certify any delta above the floor on the
    # strength of samples that never disagreed. 49 of these 560 cells have one.
    # Zero is not absence, and it is not certainty either.
    if mad is None or mad == 0 or abs(v) < k * mad or abs(v) < FLOOR_ABS:
        return 'n', 'within noise'
    arm = 'f' if v < 0 else 's'
    a = abs(v)
    b = 3 if a >= 20 else (2 if a >= 5 else 1)
    return f'{arm}{b}', ('faster' if v < 0 else 'slower')


rows = sorted([b for b in D['benchmarks'] if b['final'] is not None],
              key=lambda b: b['final'])
def _sep(b):
    return cls_for(b['final'], eff_mad(b, order[-1]))[0] != 'n'


movers = [b for b in rows if abs(b['final']) > 5 and _sep(b)]
unsep = [b for b in rows if not _sep(b)]

# Commit column headers: short sha, and a tick every 4 so the axis is readable.
cols = [c[:6] for c in order]

FAM = {'vm': 'vm micro', 'js': 'js workloads'}


def geomean(vals):
    """Suite aggregate for ratio data. NOT the arithmetic mean: FibPlaceholderRun
    runs at ~5e8x the anchor, so a plain mean is that one benchmark plus rounding
    — the same reason perf-session.sh reports geomean and median, never mean. NOT
    the median either: 24 of 35 benchmarks sit inside their own noise, so the
    median is flat on every column and says nothing."""
    vals = [v for v in vals if v is not None and 1 + v / 100 > 0]
    if not vals:
        return None
    return (math.exp(sum(math.log(1 + v / 100) for v in vals) / len(vals)) - 1) * 100


def group_of(b):
    """The logical benchmark, not the leaf. GetOwn ships 18 leaves (6 sizes x 3
    access patterns) and PrototypeMethodAccess 9, so 77% of a per-leaf aggregate
    is two benchmarks — the suite geomean was -8.84% per leaf against -27.74% per
    group. Weighting by how many sub-cases someone happened to write is not a
    property of the engine."""
    return b['name'].replace('Benchmark', '').split('/')[0]


def grouped_geomean(pairs):
    """Geomean within each logical group, then across groups: one vote each."""
    g = {}
    for b, v in pairs:
        g.setdefault(group_of(b), []).append(v)
    inner = [geomean(v) for v in g.values()]
    return geomean([x for x in inner if x is not None])


def col_summary(ci):
    """Membership is every benchmark, fixed across columns. Summarising only the
    movers would change the set per column, and two numbers over different sets
    are not comparable — the same trap set_hash exists to catch in the corpus."""
    c = order[ci]
    cum, stp, f1, s1, f2, s2 = [], [], 0, 0, 0, 0
    for b in rows:
        d = b['deltas'].get(c)
        if d is None:
            continue
        cum.append((b, d[0]))
        k, _ = cls_for(d[0], eff_mad(b, c))
        f1 += k.startswith('f'); s1 += k.startswith('s')
        if ci:
            pv = b['deltas'].get(order[ci - 1])
            sv = step_delta(d[0], pv[0]) if pv else None
            if sv is not None:
                stp.append((b, sv))
                k2, _ = cls_for(sv, max(eff_mad(b, c) or 0,
                                        eff_mad(b, order[ci - 1]) or 0), NOISE_K_STEP)
                f2 += k2.startswith('f'); s2 += k2.startswith('s')
    return (grouped_geomean(cum), f'{f1} faster / {s1} slower',
            grouped_geomean(stp) if stp else None, f'{f2} faster / {s2} slower')


FIN = order[-1]

# Column headers carry the commit subject on hover. Six hex characters cannot say
# what a commit did, and the matrix's whole point is the trajectory across them.
SUBJ = D.get('subjects') or {}
colhead_cells = []
for i, c in enumerate(order):
    role = ('reference — every delta is measured against this' if i == 0
            else 'final commit in the stack' if i == len(order) - 1 else '')
    colhead_cells.append(
        f'<span class="{"tail" if i == len(order) - 1 else ""}" tabindex="0" '
        f'role="columnheader" data-col="1" data-c="{esc(c)}" '
        f'data-s="{esc(SUBJ.get(c) or "")}" data-i="commit {i + 1} of {len(order)}" '
        f'data-r="{esc(role)}" '
        f'aria-label="{esc(c[:8])}, commit {i + 1} of {len(order)}'
        f'{": " + esc(SUBJ[c]) if SUBJ.get(c) else ""}">{esc(c[:6])}</span>')


def _fin(pred):
    return grouped_geomean([(b, b['deltas'][FIN][0]) for b in rows
                            if FIN in b['deltas'] and pred(b)])


G_ALL = _fin(lambda b: True)
G_VM = _fin(lambda b: b['pkg'] == 'vm')
G_JS = _fin(lambda b: b['pkg'] == 'js')
# The same data under the other defensible weighting. Both are quoted because
# the gap between them IS the finding: a single suite aggregate is not robust
# here and must not gate a decision.
G_LEAF = geomean([b['deltas'][FIN][0] for b in rows if FIN in b['deltas']])

# TWO footer rows, both always shown, neither following the cell mode.
#
# The step row answers "what did this commit do" and the cumulative row answers
# "where does the stack stand" — a ledger and a balance. Reading one without the
# other is how 56b7bb05 hides: its +83% step is recovered by the very next
# commit, so the cumulative row walks back to -16% while the step row keeps the
# regression attributed to the commit that caused it.
#
# They used to be one row that switched with the mode, which meant the number
# under your cursor changed meaning when you clicked a chip. Two labelled rows
# cost one line of grid and remove that.
foot_cum, foot_step = [], []
for ci in range(len(order)):
    g1, n1, g2, n2 = col_summary(ci)
    t1 = fmt(g1) if g1 is not None else ''
    t2 = fmt(g2) if g2 is not None else ''
    sha = esc(order[ci][:8])
    for bucket, t, n, what in ((foot_cum, t1, n1, 'vs first commit'),
                               (foot_step, t2, n2, 'vs preceding commit')):
        kls = 'gf' if t.startswith('\u2212') else ('gs' if t.startswith('+') else 'gn')
        bucket.append(
            f'<div class="fcell {kls}" tabindex="0" data-g="{esc(t)}" data-n="{esc(n)}" '
            f'data-w="{esc(what)}" data-c="{sha}" '
            f'aria-label="{sha} suite geomean {t or "n/a"}, {what}, {n}">'
            f'{esc(t.rstrip("%"))}</div>')

grid = []
for b in rows:
    cells = []
    for ci, c in enumerate(order):
        d = b['deltas'].get(c)
        if d is None:
            cells.append('<div class="cell empty" role="gridcell"></div>')
            continue
        v = d[0]
        em = eff_mad(b, c)
        k, what = cls_for(v, em)
        prev = b['deltas'].get(order[ci - 1]) if ci else None
        sv = step_delta(v, prev[0]) if prev else None
        ns = b.get('ns', {})
        an = fmt_ns(ns[c] - ns[order[0]]) if c in ns and order[0] in ns else ''
        asn = (fmt_ns(ns[c] - ns[order[ci - 1]])
               if ci and c in ns and order[ci - 1] in ns else '')
        if sv is None:
            sk, sw, svt, sxt = 'x', '', '', ''
        else:
            sk, sw = cls_for(sv, max(em, eff_mad(b, order[ci - 1]) or 0), NOISE_K_STEP)
            svt, sxt = fmt(sv), mult(sv)
        lab = f"{name_of(b)} at {c[:8]}: {fmt(v)}, {mult(v)} ({what}) vs first commit"
        cells.append(
            f'<div class="cell {k}" role="gridcell" tabindex="0" '
            f'data-b="{esc(name_of(b))}" data-c="{esc(c[:8])}" data-m="{fmt_mad(em)}" '
            f'data-k1="{k}" data-v1="{esc(fmt(v))}" data-x1="{esc(mult(v))}" data-w1="{esc(what)}" '
            f'data-k2="{sk}" data-v2="{esc(svt)}" data-x2="{esc(sxt)}" data-w2="{esc(sw)}" '
            f'data-a1="{esc(an)}" data-a2="{esc(asn)}" data-n="{esc(fmt_ns(ns.get(c, 0)))[1:]}" '
            f'aria-label="{esc(lab)}"></div>')
    grid.append(
        f'<div class="rowlab" title="{esc(name_of(b))}">{esc(clip(name_of(b), 38))}</div>'
        f'<div class="cells">{"".join(cells)}</div>'
        f'<div class="rowval {"mv" if b in movers else "wk"}">'
        f'{fmt(b["final"]) if abs(b["final"]) >= LABEL_MIN else "&mdash;"}</div>')

tbl = []
for b in rows:
    v = b['final']
    em = eff_mad(b, order[-1]) or 0
    sep = cls_for(v, em)[0] != 'n'
    verdict = 'improved' if (sep and v < -5) else ('slower' if (sep and v > 5)
              else ('no change' if sep else (f'within noise ({abs(v)/em:.1f}× MAD)' if em and abs(v) >= FLOOR_ABS else 'within noise (under the 0.1% floor)')))
    tbl.append(f"<tr><td>{esc(name_of(b))}</td><td class='n'>{fmt(v)}</td>"
               f"<td class='n'>{fmt_mad(em)}</td><td class='n'>{b['bn'][0]}–{b['bn'][1]}</td>"
               f"<td>{verdict}</td></tr>")

CSS = """
.viz{color-scheme:light;--surface:#fcfcfb;--plane:#f9f9f7;--ink:#0b0b0b;--ink-2:#52514e;
 --muted:#898781;--grid:#e1e0d9;--axis:#c3c2b7;--ring:rgba(11,11,11,.10);
 --mid:#f0efec;--f1:#9ec5f4;--f2:#5598e7;--f3:#256abf;--s1:#f6bdbc;--s2:#e66767;--s3:#a82a2a}
@media (prefers-color-scheme:dark){:root:where(:not([data-theme="light"])) .viz{
 color-scheme:dark;--surface:#1a1a19;--plane:#0d0d0d;--ink:#fff;--ink-2:#c3c2b7;
 --grid:#2c2c2a;--axis:#383835;--ring:rgba(255,255,255,.10);--mid:#383835}}
:root[data-theme="dark"] .viz{color-scheme:dark;--surface:#1a1a19;--plane:#0d0d0d;
 --ink:#fff;--ink-2:#c3c2b7;--grid:#2c2c2a;--axis:#383835;
 --ring:rgba(255,255,255,.10);--mid:#383835}

*{box-sizing:border-box}
body{margin:0;background:var(--plane);color:var(--ink);
 font:14px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif;padding:32px 24px 72px;
 -webkit-font-smoothing:antialiased}
.wrap{max-width:960px;margin:0 auto}
h1{font-size:26px;line-height:1.2;margin:0 0 6px;letter-spacing:-.01em;text-wrap:balance}
h2{font-size:15px;margin:0 0 4px;letter-spacing:-.005em}
p{margin:0 0 10px;color:var(--ink-2);max-width:78ch}
.meta{color:var(--muted);font-size:12.5px;margin:0 0 22px;font-family:ui-monospace,Menlo,monospace}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(168px,1fr));gap:12px;margin:0 0 22px}
.tile{background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:14px 16px}
.tile .k{font-size:11.5px;text-transform:uppercase;letter-spacing:.06em;color:var(--muted)}
.tile .v{font-size:25px;margin-top:3px;letter-spacing:-.02em}
.tile .d{font-size:12px;color:var(--ink-2);margin-top:2px}
.card{background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:20px;margin:0 0 20px}

.matrix{display:grid;grid-template-columns:auto 1fr auto;gap:0 12px;align-items:center;
 margin-top:12px;font-family:ui-monospace,Menlo,monospace;position:relative}
/* One rectangle, not 35 borders: the rows have vertical gaps, so per-cell
   borders would render the outline as a dashed line. Placed from measured
   geometry on load and on resize. */
#tailmark{position:absolute;border:1.5px solid var(--ink);border-radius:3px;
 pointer-events:none;opacity:0}
#tailmark.on{opacity:1}
.colhead span.tail{color:var(--ink);font-weight:700}
.rowlab{font-size:10.5px;color:var(--ink-2);text-align:right;white-space:nowrap;
 overflow:hidden;text-overflow:ellipsis;max-width:270px;line-height:16px}
.rowval{font-size:10.5px;text-align:right;font-variant-numeric:tabular-nums;line-height:16px}
.rowval.mv{color:var(--f3);font-weight:600}.rowval.wk{color:var(--muted)}
.cells{display:grid;grid-template-columns:repeat(16,1fr);gap:2px}
.cell{height:14px;border-radius:2px;background:var(--mid);cursor:default}
.cell:focus{outline:2px solid var(--ink);outline-offset:1px}
.cell.hot{outline:2px solid var(--ink);outline-offset:1px}
.cell.empty{background:transparent}
.f1{background:var(--f1)}.f2{background:var(--f2)}.f3{background:var(--f3)}
.s1{background:var(--s1)}.s2{background:var(--s2)}.s3{background:var(--s3)}
.foot{display:grid;grid-template-columns:repeat(16,1fr);gap:2px;margin-top:6px}
.fcell{font-size:9px;text-align:center;line-height:14px;border-radius:2px;
 font-variant-numeric:tabular-nums;color:var(--muted);cursor:default}
.fcell.gf{color:var(--f3)}.fcell.gs{color:var(--s3)}
.fcell:focus{outline:2px solid var(--ink);outline-offset:1px}
.flab{font-size:10px;color:var(--muted);text-align:right;margin-top:6px;line-height:14px}
.flab.cum,.foot.cum{margin-top:2px}
.foot.cum .fcell{opacity:.72}
.colhead{display:grid;grid-template-columns:repeat(16,1fr);gap:2px;margin-bottom:4px}
.colhead span{font-size:9px;color:var(--muted);text-align:center;overflow:hidden;
 cursor:help;border-radius:3px}
.colhead span:hover{color:var(--ink)}
.colhead span:focus-visible{outline:2px solid var(--ring);outline-offset:1px}

.modes{display:flex;align-items:center;gap:8px;margin:14px 0 2px;flex-wrap:wrap}
.chip{font:12px/1 system-ui,-apple-system,sans-serif;padding:6px 11px;border-radius:99px;
 border:1px solid var(--ring);background:transparent;color:var(--ink-2);cursor:pointer}
.chip:hover{border-color:var(--axis)}
.chip.on{background:var(--ink);color:var(--surface);border-color:var(--ink)}
.chip:focus-visible{outline:2px solid var(--f3);outline-offset:2px}
.modenote{font-size:11.5px;color:var(--muted);margin-left:4px}
.cell.x{background:transparent;border:1px dashed var(--ring)}
.scale{display:flex;align-items:center;gap:0;margin:14px 0 2px;flex-wrap:wrap}
.scale i{width:26px;height:11px;display:inline-block;border-radius:2px;margin-right:2px}
.scale .lbl{font-size:11.5px;color:var(--ink-2);margin:0 12px 0 6px}
.defn{margin:14px 0 4px;font-size:13px}
.defn summary{cursor:pointer;color:var(--ink-2);font-weight:600}
.defn p{font-size:12.5px;margin:8px 0 0}
.defn .caveat{color:var(--muted);border-top:1px solid var(--ring);padding-top:8px;margin-top:10px}
.note{font-size:12px;color:var(--muted);margin-top:6px}

#tip{position:fixed;pointer-events:none;opacity:0;transition:opacity .09s;z-index:9;
 background:var(--surface);border:1px solid var(--ring);border-radius:8px;padding:8px 10px;
 box-shadow:0 6px 20px rgba(0,0,0,.13);font-size:12px;max-width:280px}
#tip .v{font-size:15px;font-weight:600;color:var(--ink);font-family:ui-monospace,Menlo,monospace}
#tip .a{font-size:12px;color:var(--ink-2);font-family:ui-monospace,Menlo,monospace;margin-top:1px}
#tip .x{font-size:12.5px;color:var(--ink-2);font-family:ui-monospace,Menlo,monospace;margin-top:1px}
#tip .b{color:var(--ink-2);font-family:ui-monospace,Menlo,monospace;font-size:11.5px;
 margin-top:2px;word-break:break-all}
#tip .w{color:var(--muted);font-size:11px;margin-top:3px}
/* Commit subjects are prose, not data: proportional and wrapping, so a 77-char
   subject reads as a sentence instead of overflowing the 280px monospace box. */
#tip .s{font-size:12px;color:var(--ink-2);margin-top:3px;line-height:1.35}
#tip.on{opacity:1}
.scroll{overflow-x:auto}
table{border-collapse:collapse;width:100%;font-size:12.5px;font-family:ui-monospace,Menlo,monospace}
th,td{text-align:left;padding:5px 10px;border-bottom:1px solid var(--ring);white-space:nowrap}
tr:last-child td{border-bottom:0}
th{font-size:10.5px;text-transform:uppercase;letter-spacing:.06em;color:var(--muted)}
td.n,th.n{text-align:right;font-variant-numeric:tabular-nums}
details summary{cursor:pointer;font-size:13px;color:var(--ink-2)}
footer{color:var(--muted);font-size:12px;border-top:1px solid var(--ring);padding-top:16px;max-width:78ch}
@media (max-width:640px){body{padding:20px 14px 56px}h1{font-size:21px}.rowlab{max-width:130px}}
@media (prefers-reduced-motion:reduce){#tip{transition:none}}
"""

JS = """
(function(){
 var tip=document.getElementById('tip'), hot=null, mode='1';
 var CELLS=[].slice.call(document.querySelectorAll('.cell[data-k1]'));
 var NOTE={'1':'cumulative change since the first commit \u2014 where the stack stands at each point',
           '2':'change from the commit immediately before \u2014 what each commit did on its own'};
 var FCELLS=[].slice.call(document.querySelectorAll('.fcell'));
 function setMode(m){
  mode=m;
  CELLS.forEach(function(c){ c.className='cell '+(m==='1'?c.dataset.k1:c.dataset.k2); });
  // The footer rows are static: each states which comparison it is.
  document.querySelectorAll('.chip').forEach(function(b){
   var on=b.dataset.mode===m;
   b.classList.toggle('on',on); b.setAttribute('aria-checked',on?'true':'false');
  });
  document.getElementById('modenote').textContent=NOTE[m];
  hide();
 }
 function show(el){
  if(hot)hot.classList.remove('hot');
  hot=el; el.classList.add('hot');
  // textContent, never innerHTML: these labels come from tool output.
  var v=mode==='1'?el.dataset.v1:el.dataset.v2;
  var x=mode==='1'?el.dataset.x1:el.dataset.x2;
  var w=mode==='1'?el.dataset.w1:el.dataset.w2;
  if(!v){ hide(); return; }
  tip.querySelector('.v').textContent=v;
  tip.querySelector('.x').textContent=x||'';
  var a=mode==='1'?el.dataset.a1:el.dataset.a2;
  tip.querySelector('.a').textContent=a?(a+'  (now '+el.dataset.n+')'):'';
  tip.querySelector('.b').textContent=el.dataset.b+' @ '+el.dataset.c;
  tip.querySelector('.s').textContent='';
  tip.querySelector('.w').textContent=w+' \u00b7 '+(mode==='1'?'vs first commit':'vs preceding commit')+' \u00b7 this benchmark\u2019s MAD '+el.dataset.m;
  tip.classList.add('on');
 }
 // One tooltip element serves three kinds of target, so every handler must clear
 // the fields the others set. A field left alone is a field left stale.
 function showCol(el){
  if(hot)hot.classList.remove('hot');
  hot=null;
  tip.querySelector('.v').textContent=el.dataset.c.slice(0,8);
  tip.querySelector('.x').textContent='';
  tip.querySelector('.a').textContent='';
  tip.querySelector('.s').textContent=el.dataset.s||'(no subject recorded)';
  tip.querySelector('.b').textContent=el.dataset.i;
  tip.querySelector('.w').textContent=el.dataset.r||'';
  tip.classList.add('on');
 }
 function place(x,y){
  var r=tip.getBoundingClientRect();
  tip.style.left=Math.min(x+14, innerWidth-r.width-8)+'px';
  tip.style.top=Math.max(8, y-r.height-12)+'px';
 }
 function hide(){ if(hot)hot.classList.remove('hot'); hot=null; tip.classList.remove('on'); }
 // Outline the final column — the one the summary tiles above are counted from.
 var mk=document.getElementById('tailmark');
 function mark(){
  var m=document.querySelector('.matrix');
  var rows=m.querySelectorAll('.cells'); if(!rows.length)return;
  var a=rows[0].lastElementChild, z=rows[rows.length-1].lastElementChild;
  if(!a||!z)return;
  var mb=m.getBoundingClientRect(), ab=a.getBoundingClientRect(), zb=z.getBoundingClientRect();
  mk.style.left=(ab.left-mb.left-2)+'px';
  mk.style.top=(ab.top-mb.top-2)+'px';
  mk.style.width=(ab.width+4)+'px';
  mk.style.height=(zb.bottom-ab.top+4)+'px';
  mk.classList.add('on');
 }
 document.querySelectorAll('.chip').forEach(function(b){
  b.addEventListener('click',function(){ setMode(b.dataset.mode); });
 });
 mark(); addEventListener('resize',mark);
 if(window.ResizeObserver) new ResizeObserver(mark).observe(document.querySelector('.matrix'));

 var m=document.querySelector('.matrix');
 function showFoot(el){
  if(hot)hot.classList.remove('hot');
  hot=null;
  var g=el.dataset.g||'—';
  var n=el.dataset.n||'';
  tip.querySelector('.v').textContent=g;
  tip.querySelector('.x').textContent='suite geomean, one vote per logical benchmark';
  tip.querySelector('.a').textContent=n;
  tip.querySelector('.b').textContent='commit '+el.dataset.c;
  tip.querySelector('.s').textContent='';
  tip.querySelector('.w').textContent=el.dataset.w;
  tip.classList.add('on');
 }
 m.addEventListener('pointermove',function(e){
  var h=e.target.closest('[data-col]');
  if(h){ showCol(h); place(e.clientX,e.clientY); return; }
  var f=e.target.closest('.fcell');
  if(f){ showFoot(f); place(e.clientX,e.clientY); return; }
  var c=e.target.closest('.cell[data-k1]'); if(!c)return hide();
  show(c); place(e.clientX,e.clientY);
 });
 m.addEventListener('pointerleave',hide);
 // Keyboard parity: focus shows exactly what hover shows.
 m.addEventListener('focusin',function(e){
  var h=e.target.closest('[data-col]');
  if(h){ showCol(h); var hr=h.getBoundingClientRect(); place(hr.left,hr.top+hr.height); return; }
  var f=e.target.closest('.fcell');
  if(f){ showFoot(f); var fr=f.getBoundingClientRect(); place(fr.left,fr.top+fr.height); return; }
  var c=e.target.closest('.cell[data-k1]'); if(!c)return;
  show(c); var r=c.getBoundingClientRect(); place(r.left,r.top+r.height);
 });
 m.addEventListener('focusout',hide);
 addEventListener('keydown',function(e){ if(e.key==='Escape')hide(); });
})();
"""

CSS = CSS.replace(
    "repeat(16,1fr)", f"repeat({NCOM},minmax(20px,1fr))")

HTML = f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{NCOM} commits on {HOST} — change matrix</title>
<style>{CSS}</style></head>
<body class="viz">
<div class="wrap">

<h1>Where these {NCOM} commits actually changed things</h1>
<p>The session as a matrix: every benchmark against every commit. The endpoint
charts say <em>what</em> moved; this says <em>which commit moved it</em>, for all
{len(rows)} benchmarks at once. Reduced by <span style="font-family:ui-monospace,Menlo,monospace">min</span>.</p>
<p class="meta">{HOST} &middot; {NCOM} commits &times; {NRND} rounds &middot;
change vs {esc(ref[:8])} &middot; hover or tab a cell for its value</p>

<div class="tiles">
  <div class="tile"><div class="k">Benchmarks moved</div><div class="v">{len(movers)} of {len(rows)}</div>
    <div class="d">clear of their own noise &mdash; all improvements</div></div>
  <div class="tile"><div class="k">Within noise</div><div class="v">{len(unsep)} of {len(rows)}</div>
    <div class="d">shown grey: the change is smaller than the spread that produced it</div></div>
  <div class="tile"><div class="k">Suite geomean</div><div class="v">{fmt(G_ALL)}</div>
    <div class="d">one vote per logical benchmark &mdash; {FAM['vm']} {fmt(G_VM)},
    {FAM['js']} {fmt(G_JS)}. Per leaf it reads {fmt(G_LEAF)}; treat it as descriptive.</div></div>
  <div class="tile"><div class="k">Biggest single win</div><div class="v">−85.7%</div>
    <div class="d">vm.IsObject, at the commit that replaced the OR-chain</div></div>
</div>

<div class="card">
  <h2>Change matrix</h2>
  <p>Each cell is one benchmark at one commit. The base is switchable: <strong>vs first
  commit</strong> is cumulative &mdash; where the stack stands at that point &mdash; while
  <strong>vs preceding commit</strong> is the per-commit step, the same distinction as
  cumulative versus period returns. The step view is what attributes a change to the commit
  that caused it; the cumulative view is what says where things ended up. (Neither is
  &ldquo;versus the anchor&rdquo;: where normalisation applies ({ANCHOR_POLICY}) it sits
  underneath both, but is never the comparison base.)
  <strong>Grey means the change is inside that benchmark&rsquo;s own noise</strong> — the neutral bin is per-benchmark,
  not a fixed percentage, so 0.4% on a quiet benchmark is not painted like 0.4% on a noisy one.
  Rows are sorted by final change. <strong>The bold column header is the last commit</strong> —
  the one the summary tiles above are counted from, and the one the Δ column on the right
  reports.</p>

  <div class="scale">
    <i style="background:var(--f3)"></i><i style="background:var(--f2)"></i><i style="background:var(--f1)"></i>
    <span class="lbl">faster &mdash; ≥20% · 5&ndash;20% · &lt;5%</span>
    <i style="background:var(--mid)"></i><span class="lbl">within its own noise</span>
    <i style="background:var(--s1)"></i><i style="background:var(--s2)"></i><i style="background:var(--s3)"></i>
    <span class="lbl">slower</span>
  </div>

  <div class="modes" role="radiogroup" aria-label="Comparison base">
    <button type="button" class="chip on" data-mode="1" role="radio" aria-checked="true">vs first commit</button>
    <button type="button" class="chip" data-mode="2" role="radio" aria-checked="false">vs preceding commit</button>
    <span class="modenote" id="modenote">cumulative change since <span class="mono">{esc(ref[:8])}</span> &mdash; where the stack stands at each point</span>
  </div>

  <div class="scroll">
  <div class="matrix" role="grid" aria-label="Change per benchmark per commit">
    <div></div>
    <div class="colhead">{''.join(colhead_cells)}</div>
    <div></div>
    {''.join(grid)}
    <div id="tailmark"></div>
    <div class="flab">geomean, per commit</div>
    <div class="foot">{''.join(foot_step)}</div>
    <div></div>
    <div class="flab cum">geomean, cumulative</div>
    <div class="foot cum">{''.join(foot_cum)}</div>
    <div></div>
  </div>
  </div>
  <p style="margin-top:14px"><strong>Read the columns, not just the rows.</strong> The red band at
  <span style="font-family:ui-monospace,Menlo,monospace">56b7bb</span> &mdash; &ldquo;store strings
  inline in Value, drop the per-string StringObj&rdquo; &mdash; is a broad regression the endpoint
  charts cannot show: at that commit <span style="font-family:ui-monospace,Menlo,monospace">js.Arith</span>
  sat <strong>+83.4%</strong> and <span style="font-family:ui-monospace,Menlo,monospace">js.Add</span>
  <strong>+82.4%</strong> against the reference. Later commits recovered past it, so the stack ends
  at &minus;16.3% and &minus;19.3%. A view that only compares first to last reports the recovery and
  never the hole.</p>
  <details class="defn"><summary>How &ldquo;within its own noise&rdquo; is computed</summary>
  <p><strong>Step 1 &mdash; per commit.</strong> Each benchmark was measured {REPS if REPS else NRND} times at each
  commit: {NRND} rounds &times; <span class="mono">-count {NCNT}</span>. That commit&rsquo;s MAD is the median
  absolute deviation of those raw <span class="mono">ns/op</span> samples about their median,
  divided by that median. So every benchmark has <em>{NCOM}</em> MADs, one per commit.</p>
  <p><strong>Step 2 &mdash; the row.</strong> The benchmark&rsquo;s own figure is the
  <em>median of those {NCOM}</em> &mdash; its typical spread across the session.</p>
  <p><strong>Step 3 &mdash; the test.</strong> A cell must clear <strong>twice the stricter of the
  two</strong>: its own commit&rsquo;s MAD and the row&rsquo;s. Neither alone is safe. The row-wide
  figure would let a benchmark certify a move at a commit whose own samples scattered
  wildly &mdash; <span class="mono">{esc(EX_NAME)}</span> has per-commit MADs ranging from
  {EX_LO:.3g}% to {EX_HI:.3g}% in this session. A per-commit figure alone would
  let through any commit whose samples happened to land tight. It must also clear
  <strong>0.1% outright</strong>: where deltas are anchor-normalised ({ANCHOR_POLICY}) the anchor took just two
  values all session, one ulp of Go&rsquo;s four-digit output apart, so its own quantum is
  <strong>0.0923%</strong> &mdash; a normalised delta finer than that is below the resolution of
  its own reference. In step mode the relative test widens by &radic;2,
  since a step is the difference of two measurements that each carry that spread.</p>
  <p>The figure on each hover card is the stricter of the two, for that cell. Full per-benchmark
  values are in the table view.</p>
  <p class="caveat"><strong>This page has no measured attribution floor.</strong> Its verdicts
  clear 2&times; a MAD plus a <strong>0.1%</strong> absolute floor, and that 0.1% is a judgement
  call, not a measurement &mdash; these {NCOM} commits contain no pair that <em>cannot</em>
  differ, so nothing here bounds what two identical engines would measure as.
  A later session did measure it, on the same instance type: two commits compiling to
  byte-identical binaries land <strong>3.7% apart on the JS workloads and 1.1% on the VM
  micro-benchmarks</strong>, and a layout control is no worse. Against those figures
  <strong>43 of the 106 cells painted as moves here fall below the floor</strong>, and four of the
  ten headline movers do. Six survive: <span class="mono">IsObject</span> &minus;85.7%,
  <span class="mono">ToInteger</span> &minus;42.4%, <span class="mono">MatrixMult</span>
  &minus;22.9%, <span class="mono">Add</span> &minus;19.3%, <span class="mono">Arith</span>
  &minus;16.3%, <span class="mono">Fib</span> &minus;8.8%.
  The verdicts below are <em>not</em> recomputed against that floor, deliberately: it was
  measured on a different engine in a different run, and borrowing a floor across runs is the
  same act of inventing a number the floor exists to prevent. Read the small cells as
  unresolved rather than as movement.</p>
  <p class="caveat">Two further caveats. <strong>The spread is not measured in the space of the
  change:</strong> it comes from all raw <span class="mono">ns/op</span> samples, while each delta is a
  min-of-3 within every round, which discards the within-round scatter the spread still counts.
  That over-states the noise by about <strong>&times;1.9</strong> on the VM suite and
  <strong>&times;2.4</strong> on the JS workloads. The slack is kept deliberately, because
  <strong>a MAD can only see how repeatable one estimator is, never whether it is right</strong>
  &mdash; and on this data the choice of reducer moves individual verdicts far more than
  round-to-round scatter does, up to a sign change. Recomputing the spread in the delta&rsquo;s own
  space would newly certify 41 cells, 22% of which reverse or vanish under a different reducer.
  Worked through in <span class="mono">mad-space-analysis.py</span>.</p>
  </details>
  <p class="note">Columns are the {NCOM} commits in order, oldest left. The left column is the
  benchmark; the right column its change at the last commit, labelled only where it reaches
  &plusmn;5% &mdash; every value is on the hover card and in the table view. The two rows under the grid are the suite
  geomean, and they do not follow the chips: <b>per commit</b> is always the step and
  <b>cumulative</b> is always against the reference, so you can read what a commit did and where
  the stack stood at the same time. <span class="mono">56b7bb</span> is the case for having both
  &mdash; it steps <b>+22.8%</b> and the cumulative row is back to <b>+0.01%</b> one commit later,
  so a step view alone overstates the damage and a cumulative view alone hides that it happened.
  Both are the aggregate for ratio data, computed one vote per logical benchmark
  rather than per leaf, since GetOwn alone ships 18 of the 35 leaves. Be careful with it: the
  same data reads &minus;8.8% per leaf and &minus;27.7% per group, because per-leaf weighting is
  carried by GetOwn while per-group weighting is carried by two single-leaf wins. Neither is
  wrong; a single suite aggregate is simply not robust here.
  <b>So the footer cannot be reproduced by rolling up the &Delta; column you can see:</b> those 35
  values geomean to &minus;8.8%, while the footer&rsquo;s &minus;27.7% is ten group votes, in which
  <span class="mono">GetOwn</span>&rsquo;s 18 leaves and <span class="mono">PrototypeMethodAccess</span>&rsquo;s
  9 count once each. And the <b>&mdash; in the &Delta; column means &ldquo;below the &plusmn;5%
  label threshold&rdquo;, not zero and not missing</b> &mdash; all 28 such rows carry a value and
  all of them count in both footer rows. Hover for the split of how many
  moved each way, which is.</p>
</div>

<div class="card">
  <h2>Table view</h2>
  <p>Every value the matrix encodes, reachable without hovering.</p>
  <details><summary>Show all {len(rows)} rows</summary>
  <div class="scroll"><table>
    <thead><tr><th>benchmark</th><th class="n">Δ final</th><th class="n">MAD</th>
      <th class="n">b.N</th><th>verdict</th></tr></thead>
    <tbody>{''.join(tbl)}</tbody></table></div></details>
</div>

<footer>A parallel view of the data behind <span style="font-family:ui-monospace,Menlo,monospace">microopt-session-min.html</span>,
built to the dataviz method rather than by extending that page. Diverging arms are lightness-matched
and monotonic (OKLab L .812/.671/.527 against .849/.669/.486). Source:
<span style="font-family:ui-monospace,Menlo,monospace">min-reduced.json</span>.</footer>
</div>

<div id="tip" role="tooltip"><div class="v"></div><div class="x"></div><div class="a"></div><div class="s"></div><div class="b"></div><div class="w"></div></div>
<script>{JS}</script>
</body></html>
"""

open(OUT, 'w').write(HTML)
print(f"wrote {OUT} ({len(HTML)} bytes) · {len(rows)}x{len(order)} cells · "
      f"{len(movers)} moved · {len(unsep)} within noise")
