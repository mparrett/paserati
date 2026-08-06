#!/usr/bin/env python3
"""Render a session page from extracted session data.

Deliberately reuses the visual system of microopt-session.html (the mean-reduced
page this supersedes) — same tokens, same 12px card surfaces, same tile row — so
the two read as one body of work. reg-lisp's wallclock page matches it too.

    python3 regenerate-viz.py min-reduced.json microopt-session-min.html
"""
import json, sys, html, statistics as st

D = json.load(open(sys.argv[1]))
OUT = sys.argv[2]
order, ref = D['order'], D['ref']
# Session shape comes from the data. The original of this file hardcoded "16
# commits x 5 rounds = 80 builds" and "micro suite only" as prose — true of the
# session it was written for, silently false for every other one, which is
# exactly how a generated page starts lying about which run it describes.
M = D.get('meta', {})
_fam = {r.get('pkg') for r in D['benchmarks']}
SUITE = ('micro suite only' if _fam == {'vm'}
         else 'macro suite only' if _fam == {'js'}
         else 'micro + macro')
NCOM = M.get('commits', len(order))
NRND = M.get('rounds', 0)
NCELL = M.get('cells', NCOM * NRND)
TITLE = f"{NCOM} commits on {M.get('cpu_model', 'unknown host')}, reduced by {M.get('reducer', 'min')}"
W = 880

# A delta is "separated" only once it clears twice its own MAD. One MAD is too
# weak a bar: at 1.0x MAD the number is indistinguishable from the spread that
# produced it, which is how a noise reading becomes a regression in a changelog.
NOISE_K = 2.0


def esc(s):
    return html.escape(str(s), quote=True)


def name_of(b):
    """vm.IsObject / js.MatrixMult — the suite prefix disambiguates two families
    that behave differently and must not be read as one list."""
    return f"{b['pkg']}.{b['name'].replace('Benchmark', '')}"


def clip(n, k):
    # Trim from the FRONT: these names disambiguate on their tail
    # (…/WithPrototypeCache/StringPrototypeMethod).
    return n if len(n) <= k else '…' + n[-(k - 1):]


# An absolute floor as well as the relative one. The steadiest benchmark here has
# a MAD of 0.014%, so 2x MAD alone certifies a 0.03% "regression" — below
# anything this apparatus resolves (A4). And a MAD of EXACTLY zero is what
# perfect repeatability and a degenerate sample look like alike: `abs(v) >= 0` is
# true for every v, so without the guard two rows here are certified purely
# because their fifteen samples never disagreed (B2). Zero is not absence, and it
# is not certainty either.
FLOOR_ABS = 0.1


def separated(v, mad):
    if mad is None or mad == 0:
        return False
    return abs(v) >= NOISE_K * mad and abs(v) >= FLOOR_ABS


def fmt(v, plus=True):
    return (f"{v:+.1f}%" if plus else f"{v:.1f}%").replace('-', '−')


rows_all = sorted([b for b in D['benchmarks'] if b['final'] is not None],
                  key=lambda b: b['final'])
movers = [b for b in rows_all if abs(b['final']) > 5 and separated(b['final'], b['mad'])]
unsep = [b for b in rows_all if not separated(b['final'], b['mad'])]

# The stack's worst moment, computed: the commit at which the benchmarks that
# eventually improve are collectively furthest ABOVE the reference. A
# first-versus-last view cannot show this, because the recovery hides it.
def _peak():
    js = [b for b in rows_all if abs(b['final']) > 5]
    if not js:
        return None, 0, []
    c = max(order, key=lambda c: st.median(
        [b['deltas'][c][0] for b in js if c in b['deltas']] or [-1e9]))
    hit = sorted([(b, b['deltas'][c][0]) for b in js if c in b['deltas']],
                 key=lambda t: -t[1])
    return c, order.index(c), hit


PEAK_C, PEAK_I, PEAK_HIT = _peak()
PEAK_SUBJ = (D.get('subjects', {}).get(PEAK_C, '') or '').split(' - ')[0]
PEAK_UP = [(b, v) for b, v in PEAK_HIT if v > 5]

verdicts = [(c, b, mu, mn) for b in D['benchmarks'] for c, (mn, mu) in b['deltas'].items()
            if abs(mu) > 5 or abs(mn) > 5]
crossing = sorted([v for v in verdicts if (abs(v[2]) > 5) != (abs(v[3]) > 5)],
                  key=lambda r: -abs(r[2] - r[3]))
pairs = crossing[:8]


# ------------------------------------------------------------ chart: all 35
def chart_all():
    L, R, rh, top = 348, 66, 19, 16
    pw = W - L - R
    lo = min(min(b['final'] for b in rows_all) * 1.08, -95)
    hi = max(max(b['final'] for b in rows_all) * 1.3, 6)
    h = top + rh * len(rows_all) + 26

    def x(v):
        return L + (v - lo) / (hi - lo) * pw

    p = [f'<svg viewBox="0 0 {W} {h}" width="100%" role="img" aria-label="Every benchmark '
         f'at the last commit versus the first, reduced by min">']
    for gv in (-75, -50, -25, 0):
        gx = x(gv)
        p.append(f'<line x1="{gx:.1f}" y1="{top - 4}" x2="{gx:.1f}" y2="{h - 22}" '
                 f'class="{"zero" if gv == 0 else "grid"}"/>')
        p.append(f'<text x="{gx:.1f}" y="{h - 8}" class="tick" text-anchor="middle">{fmt(gv, False)}</text>')
    for i, b in enumerate(rows_all):
        cy = top + i * rh + rh / 2
        mad, v = b['mad'] or 0, b['final']
        bx0, bx1 = x(-NOISE_K * mad), x(NOISE_K * mad)
        p.append(f'<rect x="{bx0:.1f}" y="{cy - 7:.1f}" width="{max(bx1 - bx0, .8):.1f}" '
                 f'height="14" class="band"/>')
        cls = 'faster' if v < 0 else 'slower'
        x0, x1 = (x(v), x(0)) if v < 0 else (x(0), x(v))
        p.append(f'<rect x="{x0:.1f}" y="{cy - 4:.1f}" width="{max(x1 - x0, 1.5):.1f}" '
                 f'height="8" rx="3" class="bar {cls}"/>')
        p.append(f'<text x="{L - 12}" y="{cy + 3.5:.1f}" class="rowlab" '
                 f'text-anchor="end">{esc(clip(name_of(b), 40))}</text>')
        weak = '' if separated(v, mad) else ' weak'
        if v < 0:
            p.append(f'<text x="{x0 - 7:.1f}" y="{cy + 3.5:.1f}" class="val {cls}{weak}" '
                     f'text-anchor="end">{fmt(v)}</text>')
        else:
            p.append(f'<text x="{x1 + 7:.1f}" y="{cy + 3.5:.1f}" class="val {cls}{weak}" '
                     f'text-anchor="start">{fmt(v)}</text>')
    p.append('</svg>')
    return '\n'.join(p)


# ------------------------------------------------------- chart: mean -> min
def chart_shift():
    L, R, rh, top = 320, 96, 28, 18
    pw = W - L - R
    vals = [v for _, _, a, b in pairs for v in (a, b)]
    lo, hi = min(vals + [-3]), max(vals + [10])
    pad = (hi - lo) * .09
    lo, hi = lo - pad, hi + pad
    h = top + rh * len(pairs) + 26

    def x(v):
        return L + (v - lo) / (hi - lo) * pw

    p = [f'<svg viewBox="0 0 {W} {h}" width="100%" role="img" aria-label="Verdicts that change '
         f'side when the session is re-reduced from mean to min">']
    for gv, cls in ((0, 'zero'), (5, 'thresh')):
        gx = x(gv)
        p.append(f'<line x1="{gx:.1f}" y1="{top - 4}" x2="{gx:.1f}" y2="{h - 22}" class="{cls}"/>')
        p.append(f'<text x="{gx:.1f}" y="{h - 8}" class="tick" text-anchor="middle">'
                 f'{"0" if gv == 0 else "5% line"}</text>')
    for i, (c, b, was, now) in enumerate(pairs):
        y = top + i * rh + rh / 2
        xa, xb = x(was), x(now)
        p.append(f'<line x1="{xa:.1f}" y1="{y:.1f}" x2="{xb:.1f}" y2="{y:.1f}" class="conn"/>')
        p.append(f'<circle cx="{xa:.1f}" cy="{y:.1f}" r="5" class="dot was"/>')
        p.append(f'<circle cx="{xb:.1f}" cy="{y:.1f}" r="5" class="dot now"/>')
        p.append(f'<text x="{L - 12}" y="{y + 4:.1f}" class="rowlab" text-anchor="end">'
                 f'<tspan class="sha">{esc(c[:8])}</tspan>  {esc(clip(name_of(b), 30))}</text>')
        p.append(f'<text x="{W - R + 10}" y="{y + 4:.1f}" class="val" text-anchor="start">'
                 f'{fmt(was)} &#8594; {fmt(now)}</text>')
    p.append('</svg>')
    return '\n'.join(p)


# ----------------------------------------------------------- chart: traces
def chart_traces():
    ms = [b for b in movers if b['final'] < 0]
    cols, cw, ch, gap = 3, 262, 112, 26
    rows_n = (len(ms) + cols - 1) // cols
    h = rows_n * (ch + gap)
    p = [f'<svg viewBox="0 0 {W} {h}" width="100%" role="img" '
         f'aria-label="Per-commit trace for each improved benchmark">']
    for i, b in enumerate(ms):
        ox, oy = (i % cols) * (cw + 46), (i // cols) * (ch + gap)
        ds = [b['deltas'].get(c, [None])[0] for c in order]
        pts = [(j, v) for j, v in enumerate(ds) if v is not None]
        lo = min([v for _, v in pts] + [0])
        hi = max([v for _, v in pts] + [0])
        rng = max(hi - lo, 1)
        pw, ph = cw - 8, ch - 42

        def X(j):
            return ox + 4 + j / max(len(order) - 1, 1) * pw

        def Y(v):
            return oy + 28 + (hi - v) / rng * ph

        p.append(f'<text x="{ox}" y="{oy + 12}" class="small">{esc(clip(name_of(b), 26))}</text>')
        p.append(f'<text x="{ox + 4 + pw}" y="{oy + 12}" class="val faster" '
                 f'text-anchor="end">{fmt(pts[-1][1])}</text>')
        p.append(f'<line x1="{ox + 4}" y1="{Y(0):.1f}" x2="{ox + 4 + pw}" y2="{Y(0):.1f}" class="zero"/>')
        if PEAK_C is not None:
            px = X(PEAK_I)
            p.append(f'<line x1="{px:.1f}" y1="{oy + 20}" x2="{px:.1f}" y2="{oy + 28 + ph}" class="peakline"/>')
        p.append('<path d="M' + ' L'.join(f'{X(j):.1f},{Y(v):.1f}' for j, v in pts) + '" class="trace"/>')
        p.append(f'<circle cx="{X(pts[-1][0]):.1f}" cy="{Y(pts[-1][1]):.1f}" r="3.5" class="endpt"/>')
    p.append('</svg>')
    return '\n'.join(p)


tbl = []
for b in rows_all:
    mad = b['mad'] or 0
    v = b['final']
    if not separated(v, mad):
        verdict = f'not separated &middot; {abs(v) / mad:.1f}&times; MAD' if mad else 'not separated'
        pill = 'weak'
    elif v < -5:
        verdict, pill = 'improved', 'good'
    elif v > 5:
        verdict, pill = 'slower', 'bad'
    else:
        verdict, pill = 'no change', 'weak'
    tbl.append(f"<tr><td class='mono'>{esc(name_of(b))}</td><td class='n mono'>{fmt(v)}</td>"
               f"<td class='n mono'>{mad:.2f}%</td><td class='n mono'>{b['bn'][0]}&ndash;{b['bn'][1]}</td>"
               f"<td><span class='pill {pill}'>{verdict}</span></td></tr>")

CSS = """
:root{--page:#f9f9f7;--surface:#fcfcfb;--ink:#0b0b0b;--ink-2:#52514e;--muted:#898781;
 --grid:#e1e0d9;--axis:#c3c2b7;--faster:#2a78d6;--slower:#e34948;
 --band:rgba(137,135,129,.18);--ring:rgba(11,11,11,.10)}
@media (prefers-color-scheme:dark){:root{--page:#0d0d0d;--surface:#1a1a19;--ink:#fff;
 --ink-2:#c3c2b7;--grid:#2c2c2a;--axis:#383835;--faster:#3987e5;--slower:#e66767;
 --band:rgba(137,135,129,.22);--ring:rgba(255,255,255,.10)}}
:root[data-theme="dark"]{--page:#0d0d0d;--surface:#1a1a19;--ink:#fff;--ink-2:#c3c2b7;
 --grid:#2c2c2a;--axis:#383835;--faster:#3987e5;--slower:#e66767;
 --band:rgba(137,135,129,.22);--ring:rgba(255,255,255,.10)}
:root[data-theme="light"]{--page:#f9f9f7;--surface:#fcfcfb;--ink:#0b0b0b;--ink-2:#52514e;
 --grid:#e1e0d9;--axis:#c3c2b7;--faster:#2a78d6;--slower:#e34948;
 --band:rgba(137,135,129,.18);--ring:rgba(11,11,11,.10)}

*{box-sizing:border-box}
body{margin:0;background:var(--page);color:var(--ink);
 font:14px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif;
 padding:32px 24px 72px;-webkit-font-smoothing:antialiased}
.wrap{max-width:920px;margin:0 auto}
h1{font-size:26px;line-height:1.2;margin:0 0 6px;letter-spacing:-.01em;text-wrap:balance}
h2{font-size:15px;margin:0 0 4px;letter-spacing:-.005em}
p{margin:0 0 10px;color:var(--ink-2);max-width:78ch}
p:last-child{margin-bottom:0}
strong{color:var(--ink);font-weight:600}
.mono,.sha,td.n,th.n{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;
 font-variant-numeric:tabular-nums}
.meta{color:var(--muted);font-size:12.5px;margin:0 0 22px;
 font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
.warn{border:1px solid var(--ring);border-left:3px solid var(--slower);border-radius:8px;
 background:var(--surface);padding:12px 14px;margin:12px 0 20px;font-size:13px;
 color:var(--ink-2);max-width:78ch}
.warn strong{color:var(--ink)}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(168px,1fr));gap:12px;margin:0 0 22px}
.tile{background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:14px 16px}
.tile .k{font-size:11.5px;text-transform:uppercase;letter-spacing:.06em;color:var(--muted)}
.tile .v{font-size:25px;margin-top:3px;letter-spacing:-.02em}
.tile .d{font-size:12px;color:var(--ink-2);margin-top:2px}
.card{background:var(--surface);border:1px solid var(--ring);border-radius:12px;
 padding:20px;margin:0 0 20px}
.legend{display:flex;gap:16px;flex-wrap:wrap;align-items:center;margin:10px 0 6px;
 font-size:12.5px;color:var(--ink-2)}
.sw{width:11px;height:11px;border-radius:3px;display:inline-block;margin-right:6px;vertical-align:-1px}
.pill{display:inline-block;font-size:10.5px;padding:1px 7px;border-radius:99px;
 border:1px solid currentColor;white-space:nowrap}
.pill.good{color:var(--faster)}.pill.bad{color:var(--slower)}.pill.weak{color:var(--muted)}
.rowlab{font-size:11px;fill:var(--ink-2);font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
.sha{fill:var(--muted)}
.val{font-size:11px;fill:var(--ink);font-family:ui-monospace,SFMono-Regular,Menlo,monospace;
 font-variant-numeric:tabular-nums;font-weight:600}
.val.faster{fill:var(--faster)}.val.slower{fill:var(--slower)}
.val.weak{fill:var(--muted);font-weight:400}
.tick{font-size:10.5px;fill:var(--muted);font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
.small{font-size:11px;fill:var(--ink-2);font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
.grid{stroke:var(--grid);stroke-width:1}
.zero{stroke:var(--axis);stroke-width:1}
.thresh{stroke:var(--slower);stroke-width:1;stroke-dasharray:3 3;opacity:.5}
.band{fill:var(--band)}
.bar.faster{fill:var(--faster)}.bar.slower{fill:var(--slower)}
.conn{stroke:var(--axis);stroke-width:2}
.dot{stroke-width:2}
.dot.was{fill:var(--surface);stroke:var(--muted)}
.dot.now{fill:var(--faster);stroke:var(--surface)}
.peakline{stroke:var(--slower);stroke-width:1;opacity:.42}
.trace{fill:none;stroke:var(--faster);stroke-width:1.75;stroke-linejoin:round}
.endpt{fill:var(--faster);stroke:var(--surface);stroke-width:1.5}
.scroll{overflow-x:auto}
table{border-collapse:collapse;width:100%;font-size:12.5px}
th,td{text-align:left;padding:5px 10px;border-bottom:1px solid var(--ring)}
tr:last-child td{border-bottom:0}
th{font-size:10.5px;text-transform:uppercase;letter-spacing:.06em;color:var(--muted);font-weight:600}
td.n,th.n{text-align:right}
p.floornote{color:var(--ink-2);font-size:12.5px;line-height:1.5;margin:26px 0 0;
 border-top:1px solid var(--ring);padding-top:14px}
p.floornote b{color:var(--ink)}
footer{color:var(--muted);font-size:12px;border-top:1px solid var(--ring);
 padding-top:16px;margin-top:8px;max-width:78ch}
@media (max-width:640px){body{padding:20px 14px 56px}h1{font-size:21px}.tile .v{font-size:22px}}
"""

HTML = f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{TITLE}</title>
<style>{CSS}</style></head><body>
<div class="wrap">

<h1>{NCOM} commits on {M.get("cpu_model","unknown host")}, reduced by <span class="mono">{M.get("reducer","min")}</span></h1>
<p>{NCOM} commits measured back to back on one host, {NRND} counterbalanced rounds, {NCELL} cells. {len(movers)} benchmarks moved clear of their own noise.</p>

<p class="meta">{M.get("cpu_model","unknown host")} &middot; tier key {M.get("machine_key","?")} &middot;
{NCOM} commits &times; {NRND} rounds = {NCELL} cells &middot; {SUITE} &middot; {M.get("go_version","")} {M.get("os","")}/{M.get("arch","")}
&middot; {M.get("reducer","min")} of each count-{M.get("count","?")} triple, then median across rounds &middot; {M.get("pins_applied",0)} pin(s) applied</p>

<div class="tiles">
  <div class="tile"><div class="k">Benchmarks moved</div><div class="v">{len(movers)} of {len(rows_all)}</div>
    <div class="d">beyond &plusmn;5% and clear of their own noise &mdash; all improvements</div></div>
  <div class="tile"><div class="k">Not separated</div><div class="v">{len(unsep)} of {len(rows_all)}</div>
    <div class="d">delta smaller than 2&times; that benchmark&rsquo;s own MAD</div></div>
  <div class="tile"><div class="k">Verdicts changed side</div><div class="v">{len(crossing)}</div>
    <div class="d">of {len(verdicts)} that either reduction calls a move</div></div>
  <div class="tile"><div class="k">Reference</div><div class="v">{ref[:8]}</div>
    <div class="d">every delta is against this commit</div></div>
</div>

<div class="card">
  <h2>Net change across the stack</h2>
  <p>Every benchmark at the last commit versus <span class="mono">{esc(ref[:8])}</span>. Negative is
  faster. The grey band is twice that benchmark&rsquo;s own median absolute deviation &mdash; a bar
  that does not clear its band has not been shown to move, however large the number reads.</p>
  <div class="legend">
    <span><span class="sw" style="background:var(--faster)"></span>faster</span>
    <span><span class="sw" style="background:var(--slower)"></span>slower</span>
    <span><span class="sw" style="background:var(--band)"></span>&plusmn;2&times; MAD</span>
    <span style="color:var(--muted)">greyed values are inside the band</span>
  </div>
  <div class="scroll">{chart_all()}</div>
</div>

<div class="card">
  <h2>What changed when the reducer did</h2>
  <p>Of the {len(verdicts)} verdicts either reduction calls a move, <strong>{len(crossing)} cross the
  5% line</strong> &mdash; the published number said the benchmark moved and the corrected number
  says it did not, or the reverse. The large wins are untouched; only the marginal band moves.
  {"Showing the eight largest shifts." if len(crossing) > 8 else ""}</p>
  <div class="legend">
    <span><span class="sw" style="background:var(--surface);border:2px solid var(--muted);border-radius:99px"></span>as published (mean)</span>
    <span><span class="sw" style="background:var(--faster);border-radius:99px"></span>corrected (min)</span>
  </div>
  <div class="scroll">{chart_shift()}</div>
  <p><span class="mono">GetOwn</span> and <span class="mono">PrototypeMethodAccess</span> again
  &mdash; the same two families whose verdicts flip under a change of reducer elsewhere, and whose
  <span class="mono">b.N</span> is the least stable in the suite.</p>
</div>

<div class="card">
  <h2>Where each win landed</h2>
  <p>Delta against the reference at every commit in the stack, so a win can be attributed to the
  commit that produced it rather than to the stack as a whole.</p>
  <div class="scroll">{chart_traces()}</div>
  <p style="margin-top:12px"><strong>The red rule marks
  <span class="mono">{esc(PEAK_C[:8])}</span>, and it is the thing a first-versus-last comparison
  cannot report.</strong> {esc(PEAK_SUBJ)} &mdash; at that commit
  {", ".join(f'<span class="mono">{esc(name_of(b))}</span> sat at <strong>{fmt(v)}</strong>'
             for b, v in PEAK_UP[:3])}, against the reference. Later commits recovered past it, so
  the stack <em>ends</em> at {", ".join(fmt(b["final"]) for b, _ in PEAK_UP[:3])} respectively.
  Every endpoint view on this page reports the recovery and none of them the hole.</p>
</div>

<div class="card">
  <h2>All {len(rows_all)} benchmarks</h2>
  <div class="scroll"><table>
    <thead><tr><th>benchmark</th><th class="n">&Delta;</th><th class="n">MAD</th>
      <th class="n">b.N</th><th>verdict</th></tr></thead>
    <tbody>{''.join(tbl)}</tbody></table></div>
</div>

<p class="floornote"><b>Separation here is relative, and this session measured no floor.</b>
A row is called separated when it clears twice its own MAD and 0.1% outright &mdash; and
that 0.1% is a judgement call, because these commits contain no pair that
<em>cannot</em> differ, so nothing here bounds what two identical engines would measure as.
A later session did measure it on the same instance type: byte-identical binaries land
<b>3.7%</b> apart on the JS workloads and <b>1.1%</b> on the VM micro-benchmarks, with a layout
control no better. Against those figures only <b>6 of the 35</b> rows separate, rather than the
{{len(rows_all) - len(unsep)}} shown here. The verdicts below are not recomputed against that
floor deliberately: it was measured on a different engine in a different run, and borrowing a
floor across runs is the same act of inventing a number the floor exists to prevent.</p>

<footer>Reduced by <span class="mono">min</span> of each <span class="mono">-count 3</span> triple,
then median across {NRND} rounds; anchor policy: {M.get('anchor_policy','macro only')}. The
recomputation reproduces the published values to within 0.09% under <span class="mono">mean</span>,
which is what makes the <span class="mono">mean</span>&rarr;<span class="mono">min</span> difference
attributable to the reducer alone. Regenerated by <span class="mono">regenerate-viz.py</span> from
<span class="mono">min-reduced.json</span>; raw rounds in <span class="mono">raw/</span>.</footer>
</div>
</body></html>
"""

open(OUT, 'w').write(HTML)
print(f"wrote {OUT} ({len(HTML)} bytes) · {len(movers)} moved · {len(unsep)} not separated · "
      f"{len(crossing)}/{len(verdicts)} crossing")
