import { useEffect, useRef, useState, type ReactNode } from "react";
import type { SurfacePoint, Tuning, TuningArm, TuningArmResult } from "../lib/types";

// Shared bits ---------------------------------------------------------------

function useWidth(min = 280): [React.RefObject<HTMLDivElement>, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(640);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setW(Math.max(min, Math.round(e.contentRect.width))));
    ro.observe(el);
    return () => ro.disconnect();
  }, [min]);
  return [ref, w];
}

function cssVar(name: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

function hexToRgb(h: string): [number, number, number] {
  const m = h.replace("#", "");
  const n = m.length === 3 ? m.split("").map((c) => c + c).join("") : m;
  const v = parseInt(n, 16);
  return [(v >> 16) & 255, (v >> 8) & 255, v & 255];
}

function mix(a: string, b: string, t: number): string {
  const x = hexToRgb(a);
  const y = hexToRgb(b);
  const c = x.map((v, i) => Math.round(v + (y[i] - v) * Math.max(0, Math.min(1, t))));
  return `rgb(${c[0]},${c[1]},${c[2]})`;
}

// diverging maps a lift to red (worse) — neutral — green (better).
function diverging(v: number, max: number): string {
  const good = cssVar("--good", "#14854f");
  const bad = cssVar("--bad", "#c9362c");
  const mid = cssVar("--surface-2", "#f4f5f7");
  const t = max > 0 ? Math.max(-1, Math.min(1, v / max)) : 0;
  return t >= 0 ? mix(mid, good, t) : mix(mid, bad, -t);
}

const pct = (v: number, d = 1) => `${v > 0 ? "+" : v < 0 ? "−" : ""}${Math.abs(v * 100).toFixed(d)}%`;

function niceTicks(lo: number, hi: number, n = 5): number[] {
  const span = hi - lo || 1;
  const raw = span / n;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => span / s <= n) ?? raw;
  const out: number[] = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi + 1e-9; v += step) out.push(+v.toFixed(10));
  return out;
}

function Tooltip({ x, y, w, children }: { x: number; y: number; w: number; children: ReactNode }) {
  const left = Math.min(Math.max(8, x + 14), w - 248);
  return (
    <div className="chart-tip" style={{ left, top: Math.max(4, y - 10) }}>
      {children}
    </div>
  );
}

const fmtVal = (v: number) => (Number.isInteger(v) ? String(v) : Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 1 ? v.toFixed(2) : v.toPrecision(3));

export function shortPath(p: string) {
  const parts = p.split(".");
  return parts[parts.length - 1];
}

// Progress ------------------------------------------------------------------

interface Dot {
  round: number;
  arm: TuningArm;
  res: TuningArmResult;
  lift: number; // oriented: + is better
  holds: boolean;
  cx: number;
  cy: number;
}

// ProgressChart: every arm's lift vs v0, round by round, with the best
// result that respected the guardrails so far.
export function ProgressChart({ t }: { t: Tuning }) {
  const [box, W] = useWidth();
  const [hover, setHover] = useState<Dot | null>(null);
  const H = 260;
  const pad = { l: 56, r: 16, t: 14, b: 30 };
  const flip = t.config.objective_direction === "decrease" ? -1 : 1;
  const rounds = t.rounds.filter((r) => r.results);
  const maxRound = Math.max(t.config.max_rounds, ...t.rounds.map((r) => r.round));
  const dots: Dot[] = [];
  for (const r of rounds) {
    for (const res of r.results!.arms) {
      const arm = r.arms.find((a) => a.variant_id === res.variant_id);
      if (!arm || !res.objective?.testable) continue;
      dots.push({ round: r.round, arm, res, lift: flip * res.objective.rel_diff, holds: res.guardrails.every((g) => g.holds !== false), cx: 0, cy: 0 });
    }
  }
  if (dots.length === 0)
    return (
      <div ref={box} className="empty small">
        The first round's results appear here when it ends.
      </div>
    );
  let lo = Math.min(0, ...dots.map((d) => d.lift));
  let hi = Math.max(0, ...dots.map((d) => d.lift));
  const span = hi - lo || 0.01;
  lo -= span * 0.08;
  hi += span * 0.08;
  const bandW = (W - pad.l - pad.r) / maxRound;
  const x = (round: number) => pad.l + (round - 0.5) * bandW;
  const y = (v: number) => pad.t + ((hi - v) / (hi - lo)) * (H - pad.t - pad.b);
  // Spread a round's arms across its band, ordered by lift.
  for (const r of rounds) {
    const ds = dots.filter((d) => d.round === r.round).sort((a, b) => a.lift - b.lift);
    ds.forEach((d, i) => {
      d.cx = x(r.round) + (ds.length > 1 ? (i / (ds.length - 1) - 0.5) * Math.min(bandW * 0.6, 60) : 0);
      d.cy = y(d.lift);
    });
  }
  // Best that held its guardrails, as of each round.
  const best: { round: number; v: number }[] = [];
  let b = -Infinity;
  for (const r of rounds) {
    for (const d of dots.filter((d) => d.round === r.round && d.holds)) b = Math.max(b, d.lift);
    if (isFinite(b)) best.push({ round: r.round, v: b });
  }
  const good = cssVar("--good", "#14854f");
  const bad = cssVar("--bad", "#c9362c");
  const accent = cssVar("--accent", "#3b6ef5");
  const ticks = niceTicks(lo, hi);
  const step = best.map((p, i) => `${i === 0 ? "M" : "L"}${x(p.round) - bandW / 2 + 4},${y(p.v)} L${x(p.round) + bandW / 2 - 4},${y(p.v)}`).join(" ");

  const onMove = (ev: React.MouseEvent<SVGSVGElement>) => {
    const r = ev.currentTarget.getBoundingClientRect();
    const mx = ev.clientX - r.left;
    const my = ev.clientY - r.top;
    let near: Dot | null = null;
    let dist = 24 * 24;
    for (const d of dots) {
      const dd = (d.cx - mx) ** 2 + (d.cy - my) ** 2;
      if (dd < dist) {
        dist = dd;
        near = d;
      }
    }
    setHover(near);
  };

  return (
    <div ref={box} style={{ position: "relative" }}>
      <svg width={W} height={H} onMouseMove={onMove} onMouseLeave={() => setHover(null)} role="img" aria-label="Objective lift of every arm by round">
        {ticks.map((v) => (
          <g key={v}>
            <line x1={pad.l} x2={W - pad.r} y1={y(v)} y2={y(v)} className="chart-grid" />
            <text x={pad.l - 8} y={y(v) + 4} textAnchor="end" className="chart-axis">
              {pct(v, Math.abs(hi - lo) < 0.05 ? 1 : 0)}
            </text>
          </g>
        ))}
        <line x1={pad.l} x2={W - pad.r} y1={y(0)} y2={y(0)} className="chart-zero" />
        <text x={W - pad.r} y={y(0) - 5} textAnchor="end" className="chart-axis">
          v0 (control)
        </text>
        {Array.from({ length: maxRound }, (_, i) => i + 1).map((r) => (
          <text key={r} x={x(r)} y={H - 10} textAnchor="middle" className="chart-axis" style={{ fontWeight: r === t.round && !t.finished_at ? 700 : 400 }}>
            {r}
          </text>
        ))}
        <path d={step} fill="none" stroke={accent} strokeWidth={2} />
        {hover && (
          <line
            x1={hover.cx}
            x2={hover.cx}
            y1={y(flip * hover.res.objective!.rel_ci_low)}
            y2={y(flip * hover.res.objective!.rel_ci_high)}
            stroke="currentColor"
            strokeWidth={1.5}
            className="chart-ci"
          />
        )}
        {dots.map((d) => {
          const c = d.lift >= 0 ? good : bad;
          const sig = d.res.objective!.significant;
          return (
            <circle
              key={`${d.round}-${d.arm.variant_id}`}
              cx={d.cx}
              cy={d.cy}
              r={hover === d ? 6 : 4.5}
              fill={d.holds ? c : "var(--surface)"}
              fillOpacity={d.holds ? (sig ? 1 : 0.45) : 1}
              stroke={c}
              strokeWidth={d.holds ? 1 : 2}
              className="chart-dot"
            />
          );
        })}
      </svg>
      <div className="legend" style={{ marginTop: 4 }}>
        <span>
          <i style={{ background: good }} />
          better than v0
        </span>
        <span>
          <i style={{ background: bad }} />
          worse than v0
        </span>
        <span>
          <i style={{ background: good, opacity: 0.45 }} />
          faded: not significant
        </span>
        <span>
          <i style={{ background: "transparent", border: `2px solid ${good}` }} />
          hollow: breaks a guardrail
        </span>
        <span>
          <i style={{ background: accent, height: 2, borderRadius: 0 }} />
          best so far (guardrails held)
        </span>
        <span className="faint">x: round</span>
      </div>
      {hover && (
        <Tooltip x={hover.cx} y={hover.cy} w={W}>
          <ArmTip t={t} round={hover.round} arm={hover.arm} res={hover.res} />
        </Tooltip>
      )}
    </div>
  );
}

export function ArmTip({ t, round, arm, res }: { t: Tuning; round: number; arm: TuningArm; res?: TuningArmResult }) {
  return (
    <>
      <div style={{ fontWeight: 600 }}>
        Round {round} · {arm.name || arm.key}
        {arm.source === "best_so_far" && <span className="faint"> · re-test of the best</span>}
      </div>
      {t.config.params.map((p, i) => (
        <div key={p.path} className="row-between" style={{ gap: 12 }}>
          <span className="mono small">{shortPath(p.path)}</span>
          <b>{fmtVal(arm.values[i])}</b>
        </div>
      ))}
      {res?.objective && (
        <div style={{ marginTop: 4 }}>
          {t.objective.name}: <b>{pct(res.objective.rel_diff)}</b>{" "}
          <span className="faint">
            [{pct(res.objective.rel_ci_low)}, {pct(res.objective.rel_ci_high)}] · {res.units.toLocaleString()} units
          </span>
        </div>
      )}
      {res?.guardrails.map((g, i) => (
        <div key={g.metric_id} className="small">
          {g.holds === false ? "✗" : "✓"} {t.guardrails[i]?.name}: {pct(g.rel_diff)}
        </div>
      ))}
      {!res && arm.predicted && (
        <div className="small">
          Model expects {pct(arm.predicted.mean)} ± {(arm.predicted.sd * 100).toFixed(1)}%
          {t.guardrails.length > 0 && ` · guardrails hold: ${Math.round(arm.predicted.feasible * 100)}%`}
        </div>
      )}
      <div className="faint small mono">variant {arm.variant_id}</div>
    </>
  );
}

// Search space --------------------------------------------------------------

interface SpacePt {
  kind: "tested" | "next" | "control" | "best";
  round?: number;
  arm?: TuningArm;
  res?: TuningArmResult;
  vx: number;
  vy: number;
  px: number;
  py: number;
}

// SpaceChart: two parameters' plane. Background: the model's expected lift
// (others held at the recommendation); dots: tested points, darker for
// later rounds; diamonds: the current round's candidates.
export function SpaceChart({ t, xi, yi, surface }: { t: Tuning; xi: number; yi: number; surface: SurfacePoint[] | null }) {
  const [box, W0] = useWidth();
  const [hover, setHover] = useState<SpacePt | null>(null);
  const W = Math.min(W0, 720);
  const pad = { l: 56, r: 16, t: 12, b: 40 };
  const H = Math.round(Math.min(460, (W - pad.l - pad.r) * 0.8) + pad.t + pad.b);
  const px = t.config.params[xi];
  const py = t.config.params[yi];
  const xs = (v: number) => {
    const u = px.scale === "log" ? (Math.log(v) - Math.log(px.min)) / (Math.log(px.max) - Math.log(px.min)) : (v - px.min) / (px.max - px.min);
    return pad.l + u * (W - pad.l - pad.r);
  };
  const ys = (v: number) => {
    const u = py.scale === "log" ? (Math.log(v) - Math.log(py.min)) / (Math.log(py.max) - Math.log(py.min)) : (v - py.min) / (py.max - py.min);
    return H - pad.b - u * (H - pad.t - pad.b);
  };
  const pts: SpacePt[] = [];
  const current = t.rounds.find((r) => r.round === t.round);
  for (const r of t.rounds) {
    for (const arm of r.arms) {
      if (arm.is_control) continue;
      const res = r.results?.arms.find((a) => a.variant_id === arm.variant_id);
      if (r.results) pts.push({ kind: "tested", round: r.round, arm, res, vx: arm.values[xi], vy: arm.values[yi], px: 0, py: 0 });
      else if (r === current && !t.finished_at) pts.push({ kind: "next", round: r.round, arm, vx: arm.values[xi], vy: arm.values[yi], px: 0, py: 0 });
    }
  }
  const ctl = { kind: "control" as const, vx: px.control, vy: py.control, px: 0, py: 0 };
  pts.push(ctl);
  for (const p of pts) {
    p.px = xs(Math.min(Math.max(p.vx, px.min), px.max));
    p.py = ys(Math.min(Math.max(p.vy, py.min), py.max));
  }
  const accent = cssVar("--accent", "#3b6ef5");
  const ink = cssVar("--text", "#1b1f24");
  const n = surface ? Math.round(Math.sqrt(surface.length)) : 0;
  const maxAbs = surface ? Math.max(0.005, ...surface.map((s) => Math.abs(s.mean))) : 0;
  const cw = n > 1 ? (W - pad.l - pad.r) / (n - 1) : 0;
  const ch = n > 1 ? (H - pad.t - pad.b) / (n - 1) : 0;
  const maxRound = Math.max(1, ...t.rounds.map((r) => r.round));
  const bestPt = t.best ? { x: xs(t.best.values[xi]), y: ys(t.best.values[yi]) } : null;
  const hasGuards = t.guardrails.length > 0;

  const onMove = (ev: React.MouseEvent<SVGSVGElement>) => {
    const r = ev.currentTarget.getBoundingClientRect();
    const mx = ev.clientX - r.left;
    const my = ev.clientY - r.top;
    let near: SpacePt | null = null;
    let dist = 24 * 24;
    for (const p of pts) {
      const dd = (p.px - mx) ** 2 + (p.py - my) ** 2;
      if (dd < dist) {
        dist = dd;
        near = p;
      }
    }
    setHover(near);
  };

  return (
    <div ref={box} style={{ position: "relative" }}>
      <svg width={W} height={H} onMouseMove={onMove} onMouseLeave={() => setHover(null)} role="img" aria-label={`Search space: ${px.path} by ${py.path}`}>
        <defs>
          <pattern id="infeasible" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
            <line x1="0" y1="0" x2="0" y2="6" stroke={ink} strokeOpacity="0.28" strokeWidth="1.5" />
          </pattern>
          <clipPath id="plot">
            <rect x={pad.l} y={pad.t} width={W - pad.l - pad.r} height={H - pad.t - pad.b} />
          </clipPath>
        </defs>
        <rect x={pad.l} y={pad.t} width={W - pad.l - pad.r} height={H - pad.t - pad.b} className="chart-plot" />
        {surface && n > 1 && (
          <g clipPath="url(#plot)">
            {surface.map((s, k) => {
              const i = k % n;
              const j = Math.floor(k / n);
              const cx = pad.l + i * cw;
              const cy = H - pad.b - j * ch;
              return (
                <g key={k}>
                  <rect x={cx - cw / 2} y={cy - ch / 2} width={cw + 0.6} height={ch + 0.6} fill={diverging(s.mean, maxAbs)} />
                  {hasGuards && s.feasible < 0.5 && <rect x={cx - cw / 2} y={cy - ch / 2} width={cw + 0.6} height={ch + 0.6} fill="url(#infeasible)" />}
                </g>
              );
            })}
          </g>
        )}
        {niceTicks(px.min, px.max).map((v) => (
          <text key={`x${v}`} x={xs(v)} y={H - pad.b + 16} textAnchor="middle" className="chart-axis">
            {fmtVal(v)}
          </text>
        ))}
        {niceTicks(py.min, py.max).map((v) => (
          <text key={`y${v}`} x={pad.l - 8} y={ys(v) + 4} textAnchor="end" className="chart-axis">
            {fmtVal(v)}
          </text>
        ))}
        <text x={(pad.l + W - pad.r) / 2} y={H - 6} textAnchor="middle" className="chart-axis chart-axis-title">
          {px.path}
        </text>
        <text x={14} y={(pad.t + H - pad.b) / 2} textAnchor="middle" className="chart-axis chart-axis-title" transform={`rotate(-90 14 ${(pad.t + H - pad.b) / 2})`}>
          {py.path}
        </text>
        {pts
          .filter((p) => p.kind === "tested")
          .map((p, k) => (
            <circle
              key={`t${k}`}
              cx={p.px}
              cy={p.py}
              r={hover === p ? 6 : 4}
              fill={accent}
              fillOpacity={0.25 + 0.75 * ((p.round ?? 1) / maxRound)}
              stroke="var(--surface)"
              strokeWidth={1.5}
            />
          ))}
        {pts
          .filter((p) => p.kind === "next")
          .map((p, k) => (
            <rect
              key={`n${k}`}
              x={p.px - 5}
              y={p.py - 5}
              width={10}
              height={10}
              transform={`rotate(45 ${p.px} ${p.py})`}
              fill="var(--surface)"
              stroke={ink}
              strokeWidth={hover === p ? 2.5 : 1.75}
            />
          ))}
        <g stroke={ink} strokeWidth={2.5}>
          <line x1={ctl.px - 6} x2={ctl.px + 6} y1={ctl.py - 6} y2={ctl.py + 6} />
          <line x1={ctl.px - 6} x2={ctl.px + 6} y1={ctl.py + 6} y2={ctl.py - 6} />
        </g>
        <text x={ctl.px + 9} y={ctl.py - 8} className="chart-label">
          v0
        </text>
        {bestPt && (
          <g>
            <circle cx={bestPt.x} cy={bestPt.y} r={10} fill="none" stroke={ink} strokeWidth={2} />
            <text x={bestPt.x + 13} y={bestPt.y + 4} className="chart-label">
              best
            </text>
          </g>
        )}
      </svg>
      <div className="legend" style={{ marginTop: 4 }}>
        {surface ? (
          <>
            <span>
              <i style={{ background: diverging(maxAbs, maxAbs) }} />
              model expects better than v0
            </span>
            <span>
              <i style={{ background: diverging(-maxAbs, maxAbs) }} />
              worse
            </span>
            {hasGuards && (
              <span>
                <i style={{ background: "repeating-linear-gradient(45deg, var(--text-3) 0 1.5px, transparent 1.5px 5px)" }} />
                hatched: guardrails likely broken
              </span>
            )}
          </>
        ) : (
          <span className="faint">The model map appears once there are enough results.</span>
        )}
        <span>
          <i style={{ background: accent, borderRadius: 99 }} />
          tested (darker = later round)
        </span>
        {!t.finished_at && (
          <span>
            <i style={{ background: "transparent", border: `1.75px solid ${ink}`, transform: "rotate(45deg) scale(0.8)" }} />
            this round's candidates
          </span>
        )}
        <span>✕ v0 · ◯ best</span>
      </div>
      {hover && (
        <Tooltip x={hover.px} y={hover.py} w={W}>
          {hover.kind === "control" ? (
            <>
              <div style={{ fontWeight: 600 }}>v0 · control</div>
              {t.config.params.map((p) => (
                <div key={p.path} className="row-between" style={{ gap: 12 }}>
                  <span className="mono small">{shortPath(p.path)}</span>
                  <b>{fmtVal(p.control)}</b>
                </div>
              ))}
            </>
          ) : (
            <ArmTip t={t} round={hover.round!} arm={hover.arm!} res={hover.res} />
          )}
        </Tooltip>
      )}
    </div>
  );
}

// CurveChart: one parameter: expected lift with its uncertainty band, and
// the tested points.
export function CurveChart({ t, surface }: { t: Tuning; surface: SurfacePoint[] | null }) {
  const [box, W] = useWidth();
  const [hover, setHover] = useState<{ round: number; arm: TuningArm; res?: TuningArmResult; px: number; py: number } | null>(null);
  const H = 280;
  const pad = { l: 56, r: 16, t: 12, b: 40 };
  const p = t.config.params[0];
  const flip = t.config.objective_direction === "decrease" ? -1 : 1;
  const tested: { round: number; arm: TuningArm; res?: TuningArmResult; v: number; lift: number }[] = [];
  for (const r of t.rounds)
    for (const arm of r.arms) {
      const res = r.results?.arms.find((a) => a.variant_id === arm.variant_id);
      if (!arm.is_control && res?.objective?.testable) tested.push({ round: r.round, arm, res, v: arm.values[0], lift: flip * res.objective.rel_diff });
    }
  const vals = [0, ...tested.map((d) => d.lift), ...(surface ?? []).flatMap((s) => [s.mean - 2 * s.sd, s.mean + 2 * s.sd])];
  let lo = Math.min(...vals);
  let hi = Math.max(...vals);
  const span = hi - lo || 0.01;
  lo -= span * 0.06;
  hi += span * 0.06;
  const xs = (v: number) => pad.l + ((p.scale === "log" ? Math.log(v / p.min) / Math.log(p.max / p.min) : (v - p.min) / (p.max - p.min)) * (W - pad.l - pad.r));
  const ys = (v: number) => pad.t + ((hi - v) / (hi - lo)) * (H - pad.t - pad.b);
  const accent = cssVar("--accent", "#3b6ef5");
  const band = surface ? surface.map((s) => `${xs(s.x)},${ys(s.mean + 2 * s.sd)}`).join(" ") + " " + [...surface].reverse().map((s) => `${xs(s.x)},${ys(s.mean - 2 * s.sd)}`).join(" ") : "";
  const pts = tested.map((d) => ({ ...d, px: xs(d.v), py: ys(d.lift) }));
  return (
    <div ref={box} style={{ position: "relative" }}>
      <svg
        width={W}
        height={H}
        onMouseLeave={() => setHover(null)}
        onMouseMove={(ev) => {
          const r = ev.currentTarget.getBoundingClientRect();
          let best: (typeof pts)[number] | null = null;
          let dist = 24 * 24;
          for (const q of pts) {
            const dd = (q.px - (ev.clientX - r.left)) ** 2 + (q.py - (ev.clientY - r.top)) ** 2;
            if (dd < dist) {
              dist = dd;
              best = q;
            }
          }
          setHover(best);
        }}
      >
        {niceTicks(lo, hi).map((v) => (
          <g key={v}>
            <line x1={pad.l} x2={W - pad.r} y1={ys(v)} y2={ys(v)} className="chart-grid" />
            <text x={pad.l - 8} y={ys(v) + 4} textAnchor="end" className="chart-axis">
              {pct(v, 0)}
            </text>
          </g>
        ))}
        <line x1={pad.l} x2={W - pad.r} y1={ys(0)} y2={ys(0)} className="chart-zero" />
        {surface && <polygon points={band} fill={accent} fillOpacity={0.12} />}
        {surface && <polyline points={surface.map((s) => `${xs(s.x)},${ys(s.mean)}`).join(" ")} fill="none" stroke={accent} strokeWidth={2} />}
        {niceTicks(p.min, p.max).map((v) => (
          <text key={v} x={xs(v)} y={H - pad.b + 16} textAnchor="middle" className="chart-axis">
            {fmtVal(v)}
          </text>
        ))}
        <text x={(pad.l + W - pad.r) / 2} y={H - 6} textAnchor="middle" className="chart-axis chart-axis-title">
          {p.path}
        </text>
        {pts.map((q, k) => (
          <circle key={k} cx={q.px} cy={q.py} r={hover === q ? 6 : 4} fill={q.lift >= 0 ? "var(--good)" : "var(--bad)"} stroke="var(--surface)" strokeWidth={1.5} />
        ))}
        <line x1={xs(p.control)} x2={xs(p.control)} y1={pad.t} y2={H - pad.b} stroke="currentColor" strokeDasharray="3 3" className="chart-axis" />
        <text x={xs(p.control) + 4} y={pad.t + 12} className="chart-label">
          v0
        </text>
      </svg>
      <div className="legend" style={{ marginTop: 4 }}>
        <span>
          <i style={{ background: accent, height: 2, borderRadius: 0 }} />
          model's expected lift (band: ±2 sd)
        </span>
        <span>
          <i style={{ background: "var(--good)", borderRadius: 99 }} />
          tested arms (red: worse than v0)
        </span>
      </div>
      {hover && (
        <Tooltip x={hover.px} y={hover.py} w={W}>
          <ArmTip t={t} round={hover.round} arm={hover.arm} res={hover.res} />
        </Tooltip>
      )}
    </div>
  );
}
