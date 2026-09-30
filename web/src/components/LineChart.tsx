import { useEffect, useRef, useState } from "react";
import { seriesColor } from "./ui";

export interface Series {
  name: string;
  points: (number | null)[];
  band?: [number | null, number | null][]; // optional interval per point
}

// A small dependency-free SVG line chart with an optional interval band per
// series and a hover readout.
export function LineChart({
  labels,
  series,
  format,
  height = 220,
  zeroLine,
}: {
  labels: string[];
  series: Series[];
  format: (v: number) => string;
  height?: number;
  zeroLine?: boolean;
}) {
  const [hover, setHover] = useState<number | null>(null);
  // Draw at the container's real width so text stays at its CSS size.
  const box = useRef<HTMLDivElement>(null);
  const [W, setW] = useState(720);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setW(Math.max(280, Math.round(e.contentRect.width))));
    ro.observe(el);
    return () => ro.disconnect();
  }, [labels.length === 0]);
  const H = height;
  const pad = { l: 64, r: 16, t: 12, b: 26 };
  const vals: number[] = [];
  for (const s of series) {
    s.points.forEach((v) => v !== null && isFinite(v) && vals.push(v));
    s.band?.forEach(([a, b]) => {
      if (a !== null && isFinite(a)) vals.push(a);
      if (b !== null && isFinite(b)) vals.push(b);
    });
  }
  if (zeroLine) vals.push(0);
  if (!vals.length || labels.length === 0) return <div ref={box} className="empty small">No data yet</div>;
  let lo = Math.min(...vals);
  let hi = Math.max(...vals);
  if (lo === hi) {
    lo -= Math.abs(lo) * 0.1 || 1;
    hi += Math.abs(hi) * 0.1 || 1;
  }
  const span = hi - lo;
  const nonNegative = lo >= 0;
  lo -= span * 0.06;
  hi += span * 0.06;
  if (nonNegative && lo < 0) lo = 0;
  const n = labels.length;
  const x = (i: number) => pad.l + (n === 1 ? (W - pad.l - pad.r) / 2 : (i * (W - pad.l - pad.r)) / (n - 1));
  const y = (v: number) => pad.t + ((hi - v) * (H - pad.t - pad.b)) / (hi - lo);
  const ticks = Array.from({ length: 5 }, (_, i) => lo + ((hi - lo) * i) / 4);
  const labelEvery = Math.max(1, Math.ceil(n / Math.max(3, Math.floor(W / 90))));

  const path = (pts: (number | null)[]) => {
    let d = "";
    let pen = false;
    pts.forEach((v, i) => {
      if (v === null || !isFinite(v)) {
        pen = false;
        return;
      }
      d += `${pen ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`;
      pen = true;
    });
    return d;
  };
  const bandPath = (band: [number | null, number | null][]) => {
    const idx = band.map((b, i) => (b[0] !== null && b[1] !== null && isFinite(b[0]!) && isFinite(b[1]!) ? i : -1)).filter((i) => i >= 0);
    if (idx.length < 2) return "";
    const top = idx.map((i) => `${x(i).toFixed(1)},${y(band[i][1]!).toFixed(1)}`);
    const bot = idx.reverse().map((i) => `${x(i).toFixed(1)},${y(band[i][0]!).toFixed(1)}`);
    return `M${top.join("L")}L${bot.join("L")}Z`;
  };

  return (
    <div className="chart" ref={box}>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        onMouseLeave={() => setHover(null)}
        onMouseMove={(e) => {
          const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
          const px = ((e.clientX - r.left) / r.width) * W;
          const i = Math.round(((px - pad.l) / (W - pad.l - pad.r)) * (n - 1));
          setHover(Math.max(0, Math.min(n - 1, i)));
        }}
      >
        {ticks.map((t, i) => (
          <g key={i}>
            <line x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} stroke="var(--border)" />
            <text x={pad.l - 8} y={y(t) + 4} textAnchor="end">
              {format(t)}
            </text>
          </g>
        ))}
        {zeroLine && <line x1={pad.l} x2={W - pad.r} y1={y(0)} y2={y(0)} stroke="var(--text-3)" strokeDasharray="4 3" />}
        {labels.map((l, i) =>
          i % labelEvery === 0 || i === n - 1 ? (
            <text key={i} x={x(i)} y={H - 6} textAnchor="middle">
              {l.slice(5)}
            </text>
          ) : null
        )}
        {series.map((s, si) => (
          <g key={s.name}>
            {s.band && <path d={bandPath(s.band)} fill={seriesColor(si)} opacity={0.14} />}
            <path d={path(s.points)} fill="none" stroke={seriesColor(si)} strokeWidth={2} />
            {n <= 31 &&
              s.points.map((v, i) =>
                v !== null && isFinite(v) ? <circle key={i} cx={x(i)} cy={y(v)} r={hover === i ? 4 : 2.2} fill={seriesColor(si)} /> : null
              )}
          </g>
        ))}
        {hover !== null && <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={H - pad.b} stroke="var(--border-strong)" />}
      </svg>
      <div className="legend" style={{ marginTop: 6 }}>
        {series.map((s, si) => (
          <span key={s.name}>
            <i style={{ background: seriesColor(si) }} />
            {s.name}
            {hover !== null && s.points[hover] !== null && s.points[hover] !== undefined && (
              <b style={{ marginLeft: 6 }}>{format(s.points[hover] as number)}</b>
            )}
          </span>
        ))}
        {hover !== null && <span className="faint">{labels[hover]}</span>}
      </div>
    </div>
  );
}
