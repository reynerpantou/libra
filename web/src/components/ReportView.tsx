import { Fragment, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import { ago, fmtInt, fmtP, fmtPct, fmtValue } from "../lib/format";
import { useAsync } from "../lib/hooks";
import { copyText, downloadCSV, toTSV, type Cell } from "../lib/exportTable";
import type { Comparison, Experiment, MetricBrief, MetricGroup, MetricResult, Report, ReportVariant } from "../lib/types";
import { LineChart } from "./LineChart";
import { GroupPicker } from "./GroupPicker";
import { Empty, ErrorBox, Field, Loading, Popover, Segmented, Stat, seriesColor } from "./ui";

// verdict names a comparison's result. "Positive"/"negative" say whether
// the change is good for the metric (its direction: higher or lower is
// better); the arrow says which way it moved.
export function verdict(c: Comparison, m: Pick<MetricResult, "direction">): { text: string; cls: string; title: string } {
  const up = (c.rel_diff ?? c.abs_diff ?? 0) > 0;
  const arrow = up ? "↑" : "↓";
  const moved = up ? "increase" : "decrease";
  const pref = m.direction === "increase" ? "higher is better" : m.direction === "decrease" ? "lower is better" : "no preferred direction";
  switch (c.verdict) {
    case "better":
      return { text: `Significant positive ${arrow}`, cls: "b-good", title: `Significant ${moved} — good for this metric (${pref})` };
    case "worse":
      return { text: `Significant negative ${arrow}`, cls: "b-bad", title: `Significant ${moved} — bad for this metric (${pref})` };
    case "changed":
      return { text: `Significant ${moved} ${arrow}`, cls: "b-accent", title: "Significant change; this metric has no preferred direction" };
    case "flat":
      return { text: "Not significant", cls: "", title: "The confidence interval includes zero: no detectable difference" };
    default:
      return { text: "Too little data", cls: "", title: "Not enough units or variance to test yet" };
  }
}

// CopyButton copies text and says so for a moment.
function CopyButton({ text, label = "Copy", title, className = "copy-btn" }: { text: () => string; label?: string; title?: string; className?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className={className}
      title={title ?? "Copy as a table (pastes into sheets)"}
      onClick={async (e) => {
        e.stopPropagation();
        await copyText(text());
        setDone(true);
        setTimeout(() => setDone(false), 1400);
      }}
    >
      {done ? "Copied ✓" : label}
    </button>
  );
}

const pctCell = (v: number | null | undefined) => (v === null || v === undefined || !isFinite(v) ? "" : (v * 100).toFixed(2) + "%");
const numCell = (v: number | null | undefined, d = 4) => (v === null || v === undefined || !isFinite(v) ? "" : +v.toFixed(d));

// rowsFor lays a set of metric results out as a table: one row per metric
// and variant compared with control.
function rowsFor(metrics: MetricResult[], control: ReportVariant, treatments: ReportVariant[], extra: Cell[] = []): Cell[][] {
  const out: Cell[][] = [];
  for (const m of metrics) {
    const cv = m.values.find((v) => v.variant_id === control.id);
    for (const t of treatments) {
      const tv = m.values.find((v) => v.variant_id === t.id);
      const c = m.comparisons.find((x) => x.variant_id === t.id);
      out.push([
        ...extra,
        m.name,
        m.key,
        t.name || t.key,
        numCell(cv?.value),
        numCell(tv?.value),
        pctCell(c?.rel_diff),
        pctCell(c?.rel_ci_low),
        pctCell(c?.rel_ci_high),
        c?.p_value === null || c?.p_value === undefined ? "" : +c.p_value.toPrecision(3),
        c ? verdict(c, m).text : m.error ?? "",
      ]);
    }
  }
  return out;
}
const rowHeader = ["Metric", "Key", "Variant", "Control value", "Variant value", "Relative lift", "CI low", "CI high", "p-value", "Result"];

type Layout = "detailed" | "matrix";
type Selection = { kind: "experiment" } | { kind: "all" } | { kind: "groups"; ids: number[] } | { kind: "metrics"; ids: number[] };

export default function ReportView({ experiment: e }: { experiment: Experiment }) {
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  // Metric selection: the experiment's own groups (with defaults), a custom
  // set of groups, or every metric the business can see.
  const [pick, setPick] = useState<Selection>({ kind: "experiment" });
  const [q, setQ] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [picker, setPicker] = useState<HTMLElement | null>(null);
  const [dimension, setDimension] = useState("");
  const [alpha, setAlpha] = useState("0.05");
  const [selected, setSelected] = useState<number | null>(null);
  const [hidden, setHidden] = useState<Set<number>>(new Set());
  const [layout, setLayout] = useState<Layout | null>(null);
  const refs = useAsync(async () => {
    const [groups, metrics, business] = await Promise.all([api.allGroups(), api.allMetrics(), api.business(e.business_id)]);
    return { groups, metrics, business };
  }, [e.business_id]);

  const pickKey = pick.kind === "groups" || pick.kind === "metrics" ? `${pick.kind}:${pick.ids.join(",")}` : pick.kind;
  const rep = useAsync(async () => {
    if (pick.kind === "all") {
      const metrics = (await api.metrics({ kind: "business", id: e.business_id })).map((m) => m.id).join(",");
      return api.report(e.id, { from, to, metrics, dimension, alpha });
    }
    if (pick.kind === "metrics") return api.report(e.id, { from, to, metrics: pick.ids.join(",") || "0", dimension, alpha });
    return api.report(e.id, { from, to, groups: pick.kind === "groups" ? pick.ids.join(",") || "0" : undefined, dimension, alpha });
  }, [e.id, from, to, pickKey, dimension, alpha, e.updated_at]);

  const r = rep.data;
  useEffect(() => {
    if (r && !from) setFrom(r.from);
    if (r && !to) setTo(r.to);
  }, [r, from, to]);

  const control = r ? r.variants.find((v) => v.is_control) ?? r.variants[0] : undefined;
  const treatments = r && control ? r.variants.filter((v) => v.id !== control.id) : [];
  const shown = treatments.filter((v) => !hidden.has(v.id));
  const lay: Layout = layout ?? (treatments.length > 3 ? "matrix" : "detailed");
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
  const groupLabel =
    pick.kind === "all"
      ? `Every ${e.business_name} metric`
      : pick.kind === "groups"
      ? `Custom: ${plural(pick.ids.length, "group")}`
      : pick.kind === "metrics"
      ? `Custom: ${plural(pick.ids.length, "metric")}`
      : "As set up on the experiment";
  const query = q.trim().toLowerCase();
  const matches = (m: MetricResult) => !query || [m.name, m.key, m.formula].some((x) => x.toLowerCase().includes(query));
  const groupsOf = (seg: Report["segments"][number]) => {
    const byId = new Map(seg.metrics.map((m) => [m.metric_id, m]));
    return (r?.groups ?? []).map((g) => ({ g, key: `${g.id}-${g.name}`, ms: (g.metric_ids ?? []).map((id) => byId.get(id)).filter((m): m is MetricResult => !!m) }));
  };
  const totalMetrics = r ? new Set(r.segments[0]?.metrics.map((m) => m.metric_id)).size : 0;
  const shownMetrics = r ? new Set(r.segments[0]?.metrics.filter(matches).map((m) => m.metric_id)).size : 0;
  const exportRows = (): Cell[][] => {
    if (!r || !control) return [];
    const rows: Cell[][] = [["Segment", "Group", ...rowHeader]];
    for (const seg of r.segments)
      for (const { g, ms } of groupsOf(seg)) rows.push(...rowsFor(ms, control, treatments, [r.dimension ? `${r.dimension}=${seg.value}` : "all", g.name]));
    return rows;
  };
  const visibleRows = (): Cell[][] => {
    if (!r || !control) return [];
    const rows: Cell[][] = [["Group", ...rowHeader]];
    for (const { g, ms, key } of groupsOf(r.segments[0])) if (!collapsed.has(key)) rows.push(...rowsFor(ms.filter(matches), control, shown, [g.name]));
    return rows;
  };

  return (
    <div className="stack">
      <div className="card card-pad">
        <div className="row" style={{ alignItems: "flex-end", gap: 14 }}>
          <Field label="From">
            <input className="input" type="date" value={from} onChange={(x) => setFrom(x.target.value)} />
          </Field>
          <Field label="To">
            <input className="input" type="date" value={to} onChange={(x) => setTo(x.target.value)} />
          </Field>
          <Field label="Metrics">
            <button className="input" style={{ textAlign: "left", minWidth: 200, cursor: "pointer" }} onClick={(x) => setPicker(picker ? null : x.currentTarget)}>
              {groupLabel} ▾
            </button>
          </Field>
          <Field label="Break down by">
            <select className="input" value={dimension} onChange={(x) => setDimension(x.target.value)}>
              <option value="">Nothing (all units)</option>
              {r?.available_dimensions.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Significance">
            <Segmented
              options={[
                ["0.1", "90%"],
                ["0.05", "95%"],
                ["0.01", "99%"],
              ]}
              value={alpha}
              onChange={setAlpha}
            />
          </Field>
          <span className="spacer" />
          <Link className="btn btn-sm" to={`/businesses/${e.business_id}?tab=metrics`}>
            Edit metrics & formulas
          </Link>
        </div>
        {treatments.length > 1 && (
          <div className="row" style={{ marginTop: 12, gap: 6 }}>
            <span className="small faint">Compare with {control?.name || control?.key}:</span>
            {treatments.map((v) => {
              const i = r!.variants.findIndex((x) => x.id === v.id);
              const on = !hidden.has(v.id);
              return (
                <button
                  key={v.id}
                  className={`vchip ${on ? "on" : ""}`}
                  onClick={() => {
                    const next = new Set(hidden);
                    if (on) next.add(v.id);
                    else next.delete(v.id);
                    if (next.size < treatments.length) setHidden(next);
                  }}
                >
                  <span className="dot" style={{ background: seriesColor(i) }} />
                  {v.name || v.key}
                </button>
              );
            })}
            {hidden.size > 0 && (
              <button className="btn btn-ghost btn-sm" onClick={() => setHidden(new Set())}>
                Show all
              </button>
            )}
            <span className="spacer" />
            <Segmented<Layout>
              options={[
                ["matrix", "Compact"],
                ["detailed", "Detailed"],
              ]}
              value={lay}
              onChange={setLayout}
            />
          </div>
        )}
      </div>
      {picker && refs.data && (
        <Popover anchor={picker} onClose={() => setPicker(null)} width={760}>
          <MetricsChooser
            e={e}
            pick={pick}
            onPick={setPick}
            current={(r?.groups ?? []).map((g) => g.id).filter(Boolean)}
            groups={refs.data.groups}
            metrics={refs.data.metrics}
            configured={configuredLabel(e, refs.data.groups)}
          />
        </Popover>
      )}

      <ErrorBox error={rep.error} />
      {rep.loading && !r ? (
        <Loading label="Computing report…" />
      ) : !r ? null : r.variants.every((v) => v.units === 0) ? (
        <div className="card">
          <Empty title="No measured units in this window yet">
            <p>
              Units appear once the experiment is running, services call the resolve API, and the{" "}
              <Link to="/pipeline">pipeline</Link> has processed the exposures.
            </p>
          </Empty>
        </div>
      ) : (
        <>
          <Summary r={r} />
          <div className="card card-pad rep-toolbar">
            <input
              className="input"
              style={{ flex: 1, minWidth: 220, maxWidth: 420 }}
              placeholder="Find a metric by name, key or formula…"
              value={q}
              onChange={(x) => setQ(x.target.value)}
            />
            <span className="small faint">{query ? `${shownMetrics} of ${totalMetrics} metrics` : `${totalMetrics} metrics`}</span>
            <span className="spacer" />
            <button className="btn btn-sm btn-ghost" onClick={() => setCollapsed(new Set())}>
              Expand all
            </button>
            <button className="btn btn-sm btn-ghost" onClick={() => setCollapsed(new Set(r.segments.flatMap((sg) => groupsOf(sg).map((x) => x.key))))}>
              Collapse all
            </button>
            <CopyButton className="btn btn-sm" label="Copy table" title="Copy what's shown (open groups, matching metrics, visible variants) — pastes into a sheet" text={() => toTSV(visibleRows())} />
            <button className="btn btn-sm" onClick={() => downloadCSV(`libra-${e.id}-report-${r.from}-to-${r.to}.csv`, exportRows())} title="Every group, metric and variant (and segment) as CSV">
              Export CSV
            </button>
          </div>
          {r.segments.map((seg) => (
            <section key={seg.value} className="card">
              <div className="card-head">
                <div>
                  <h2>{r.dimension ? `${r.dimension} = ${seg.value}` : "Results"}</h2>
                  <p>
                    {fmtInt(seg.units)} units · {r.from} to {r.to} · {Math.round((1 - r.alpha) * 100)}% confidence intervals · click a metric for its trend
                  </p>
                  <p className="small faint">
                    <b>Significant positive</b> / <b>negative</b>: a real change that's good / bad for the metric (its direction decides — for latency, lower is
                    better); the arrow shows which way it moved. <b>Not significant</b>: no detectable difference yet.
                  </p>
                </div>
              </div>
              {groupsOf(seg).map(({ g, key, ms: all }) => {
                const ms = all.filter(matches);
                if (query && ms.length === 0) return null;
                const closed = collapsed.has(key);
                const toggle = () => {
                  const next = new Set(collapsed);
                  if (closed) next.delete(key);
                  else next.add(key);
                  setCollapsed(next);
                };
                return (
                  <div key={key} className="rep-group">
                    {(r.groups.length > 1 || g.id !== 0) && (
                      <div className="rep-group-head" onClick={toggle} title={closed ? "Show" : "Hide"}>
                        <span className="faint" style={{ width: 12 }}>
                          {closed ? "▸" : "▾"}
                        </span>
                        <b>{g.name}</b> <span className="faint small">{g.owner}</span>{" "}
                        {g.is_default && (
                          <span className="badge b-good" title="A default group: included in every experiment of its business or platform">
                            default
                          </span>
                        )}
                        <span className="faint small"> · {query ? `${ms.length} of ${all.length}` : ms.length} metrics</span>
                      </div>
                    )}
                    {closed ? null : lay === "matrix" && control ? (
                      <MetricMatrix metrics={ms} control={control} treatments={shown} all={r.variants} selected={r.dimension ? null : selected} onSelect={setSelected} />
                    ) : (
                      <MetricTable metrics={ms} variants={r.variants.filter((v) => !hidden.has(v.id))} selected={r.dimension ? null : selected} onSelect={setSelected} />
                    )}
                  </div>
                );
              })}
            </section>
          ))}
          {r.segments_not_shown ? <p className="faint small">{r.segments_not_shown} smaller segments not shown.</p> : null}
          {!r.dimension && selected !== null && (
            <Trend
              experimentId={e.id}
              metric={r.segments[0].metrics.find((m) => m.metric_id === selected)}
              variants={r.variants.filter((v) => !hidden.has(v.id))}
              from={r.from}
              to={r.to}
              alpha={alpha}
            />
          )}
        </>
      )}
    </div>
  );
}

// Summary shows the split. With a handful of variants it's tiles; beyond
// that a compact table that stays readable at any count.
function Summary({ r }: { r: Report }) {
  const total = r.variants.reduce((s, v) => s + v.units, 0);
  const wsum = r.variants.reduce((s, v) => s + v.weight, 0);
  const many = r.variants.length > 3;
  return (
    <>
      {r.srm.suspect ? (
        <div className="alert alert-bad">
          <b>Sample ratio mismatch.</b> The split between variants differs from the configured weights more than chance allows (p = {fmtP(r.srm.p_value)}).
          Results may be biased — check for targeting that depends on the variant, exposure logging that differs by variant, or bots.
        </div>
      ) : (
        <div className="alert alert-good small">
          Traffic split matches the configured weights (sample ratio check p = {fmtP(r.srm.p_value)}).
        </div>
      )}
      {many ? (
        <section className="card">
          <div className="card-head">
            <div>
              <h2>Units per variant</h2>
              <p>
                {fmtInt(total)} units in {r.variants.length} variants · data {r.data_through ? ago(r.data_through) : "never processed"}
                {r.excluded_multi_variant_units > 0 && ` · ${fmtInt(r.excluded_multi_variant_units)} units excluded (saw 2+ variants)`}
              </p>
            </div>
          </div>
          <div className="table-wrap">
            <table className="tbl tbl-compact">
              <thead>
                <tr>
                  <th>Variant</th>
                  <th className="num">Units</th>
                  <th className="num">Share</th>
                  <th className="num">Expected</th>
                  <th style={{ width: "40%" }}>Split</th>
                </tr>
              </thead>
              <tbody>
                {r.variants.map((v, i) => {
                  const share = total ? v.units / total : 0;
                  const exp = v.weight / wsum;
                  return (
                    <tr key={v.id}>
                      <td className="nowrap">
                        <span className="dot" style={{ background: seriesColor(i), marginRight: 8 }} />
                        {v.name || v.key} {v.is_control && <span className="badge">control</span>}
                      </td>
                      <td className="num">{fmtInt(v.units)}</td>
                      <td className="num">{(share * 100).toFixed(1)}%</td>
                      <td className="num faint">{(exp * 100).toFixed(1)}%</td>
                      <td>
                        <div className="split-bar" title={`${(share * 100).toFixed(2)}% of units, expected ${(exp * 100).toFixed(2)}%`}>
                          <div style={{ width: `${Math.min(100, (share / Math.max(exp, share, 1e-9)) * 100)}%`, background: seriesColor(i) }} />
                          <span className="mark" style={{ left: `${Math.min(100, (exp / Math.max(exp, share, 1e-9)) * 100)}%` }} />
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      ) : (
        <div className="grid-4">
          {r.variants.map((v, i) => (
            <Stat
              key={v.id}
              label={`${v.name || v.key}${v.is_control ? " · control" : ""}`}
              value={<span style={{ color: seriesColor(i) }}>{fmtInt(v.units)}</span>}
              sub={`${total ? ((v.units / total) * 100).toFixed(1) : "0"}% of units · expected ${((v.weight / wsum) * 100).toFixed(1)}%`}
            />
          ))}
          <Stat
            label="Data freshness"
            value={<span style={{ fontSize: 16 }}>{r.data_through ? ago(r.data_through) : "never"}</span>}
            sub={r.excluded_multi_variant_units > 0 ? `${fmtInt(r.excluded_multi_variant_units)} units excluded (saw 2+ variants)` : "last pipeline run"}
          />
        </div>
      )}
    </>
  );
}

// MetricMatrix: metrics down, variants across, each cell the lift vs
// control colored by verdict. Scales to many variants (scrolls sideways
// with the metric column pinned).
function MetricMatrix({
  metrics,
  control,
  treatments,
  all,
  selected,
  onSelect,
}: {
  metrics: MetricResult[];
  control: ReportVariant;
  treatments: ReportVariant[];
  all: ReportVariant[];
  selected: number | null;
  onSelect: (id: number) => void;
}) {
  if (metrics.length === 0) return <Empty title="No metrics in this group" />;
  return (
    <div className="table-wrap">
      <table className="tbl rep-matrix">
        <thead>
          <tr>
            <th className="sticky-col">Metric</th>
            <th className="num">
              <div className="th-copy">
                {control.name || control.key}
                <CopyButton
                  label="⧉"
                  title={`Copy the ${control.name || control.key} column`}
                  text={() => toTSV([["Metric", "Key", `${control.name || control.key} (control)`], ...metrics.map((m) => [m.name, m.key, numCell(m.values.find((v) => v.variant_id === control.id)?.value)])])}
                />
              </div>
            </th>
            {treatments.map((t) => (
              <th key={t.id} className="num" title={t.key}>
                <div className="th-copy">
                  <span>
                    <span className="dot" style={{ background: seriesColor(all.findIndex((x) => x.id === t.id)), marginRight: 6 }} />
                    {t.name || t.key}
                  </span>
                  <CopyButton label="⧉" title={`Copy the ${t.name || t.key} column: values, lift, interval, p-value and result`} text={() => toTSV([rowHeader, ...rowsFor(metrics, control, [t])])} />
                </div>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {metrics.map((m) => {
            const cv = m.values.find((v) => v.variant_id === control.id);
            return (
              <tr
                key={m.metric_id}
                className="clickable"
                style={selected === m.metric_id ? { background: "var(--accent-soft)" } : undefined}
                onClick={() => onSelect(m.metric_id)}
              >
                <td className="sticky-col">
                  <div className="metric-name">{m.name}</div>
                  <div className="metric-formula" title={m.expanded}>
                    {m.formula}
                  </div>
                </td>
                <td className="num">{m.error ? <span className="badge b-bad">error</span> : <Val m={m} v={cv} />}</td>
                {treatments.map((t) => {
                  const c = m.comparisons.find((x) => x.variant_id === t.id);
                  const tv = m.values.find((v) => v.variant_id === t.id);
                  if (m.error || !c) return <td key={t.id} className="num faint">—</td>;
                  return (
                    <td
                      key={t.id}
                      className={`num cell-${c.verdict}`}
                      title={`${t.name || t.key}: ${fmtValue(tv?.value ?? null, m.format, m.decimals)} · ${fmtPct(c.rel_ci_low, 1)} to ${fmtPct(
                        c.rel_ci_high,
                        1
                      )} · p ${fmtP(c.p_value)} · ${verdict(c, m).text}`}
                    >
                      <div className="lift">{fmtPct(c.rel_diff)}</div>
                      <div className="small faint nowrap">
                        {fmtPct(c.rel_ci_low, 1)} … {fmtPct(c.rel_ci_high, 1)}
                      </div>
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function MetricTable({
  metrics,
  variants,
  selected,
  onSelect,
}: {
  metrics: MetricResult[];
  variants: ReportVariant[];
  selected: number | null;
  onSelect: (id: number) => void;
}) {
  const control = variants.find((v) => v.is_control) ?? variants[0];
  const treatments = variants.filter((v) => v.id !== control.id);
  // One scale for every CI bar in the table, so widths compare.
  const scale = useMemo(() => {
    let m = 0.01;
    for (const met of metrics)
      for (const c of met.comparisons)
        for (const x of [c.rel_ci_low, c.rel_ci_high, c.rel_diff]) if (x !== null && isFinite(x)) m = Math.max(m, Math.abs(x));
    return Math.min(m * 1.1, 2);
  }, [metrics]);

  if (metrics.length === 0) return <Empty title="No metrics selected" />;
  return (
    <div className="table-wrap">
      <table className="tbl rep-metric">
        <thead>
          <tr>
            <th>Metric</th>
            <th className="num">{control.name || control.key}</th>
            <th>Variant</th>
            <th className="num">Value</th>
            <th className="num">Relative lift</th>
            <th style={{ minWidth: 180 }}>Interval</th>
            <th className="num">p-value</th>
            <th>Result</th>
          </tr>
        </thead>
        <tbody>
          {metrics.map((m) => {
            const cv = m.values.find((v) => v.variant_id === control.id);
            if (m.error)
              return (
                <tr key={m.metric_id}>
                  <td>
                    <div className="metric-name">{m.name}</div>
                    <div className="metric-formula">{m.formula}</div>
                  </td>
                  <td colSpan={7}>
                    <span className="badge b-bad">Formula error</span> <span className="small">{m.error}</span>
                  </td>
                </tr>
              );
            return (
              <Fragment key={m.metric_id}>
                {treatments.map((t, ti) => {
                  const tv = m.values.find((v) => v.variant_id === t.id);
                  const c = m.comparisons.find((x) => x.variant_id === t.id);
                  const cls = !c ? "lift-flat" : c.verdict === "better" ? "lift-good" : c.verdict === "worse" ? "lift-bad" : "lift-flat";
                  return (
                    <tr
                      key={t.id}
                      className="clickable"
                      style={selected === m.metric_id ? { background: "var(--accent-soft)" } : undefined}
                      onClick={() => onSelect(m.metric_id)}
                    >
                      {ti === 0 && (
                        <>
                          <td rowSpan={treatments.length}>
                            <div className="metric-name">{m.name}</div>
                            <div className="metric-formula" title={m.expanded}>
                              {m.formula}
                            </div>
                            {m.kind !== "ratio" && (
                              <div className="faint small" title="Variants differ in size, so totals are compared per exposed unit.">
                                {m.kind === "total" ? "total · compared per unit" : "compared per unit"}
                              </div>
                            )}
                          </td>
                          <td rowSpan={treatments.length} className="num">
                            <Val m={m} v={cv} />
                          </td>
                        </>
                      )}
                      <td className="nowrap">{t.name || t.key}</td>
                      <td className="num">
                        <Val m={m} v={tv} />
                      </td>
                      <td className={`num lift ${cls}`}>{c ? fmtPct(c.rel_diff) : "—"}</td>
                      <td>{c && <CIBar c={c} scale={scale} />}</td>
                      <td className="num faint">{c ? fmtP(c.p_value) : "—"}</td>
                      <td>
                        {c && (
                          <span className={`badge ${verdict(c, m).cls}`} title={verdict(c, m).title}>
                            {verdict(c, m).text}
                          </span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function Val({ m, v }: { m: MetricResult; v?: { value: number | null; total: number | null } }) {
  if (!v) return <>—</>;
  if (m.kind === "total")
    return (
      <>
        <div>{fmtValue(v.total, m.format, m.decimals)}</div>
        <div className="faint small">{fmtValue(v.value, m.format, 2)} / unit</div>
      </>
    );
  return <>{fmtValue(v.value, m.format, m.decimals)}</>;
}

function CIBar({ c, scale }: { c: Comparison; scale: number }) {
  if (c.rel_ci_low === null || c.rel_ci_high === null || c.rel_diff === null) return <span className="faint small">—</span>;
  const pos = (x: number) => `${50 + (Math.max(-scale, Math.min(scale, x)) / scale) * 50}%`;
  const color = c.verdict === "better" ? "var(--good)" : c.verdict === "worse" ? "var(--bad)" : c.verdict === "changed" ? "var(--accent)" : "var(--text-3)";
  const lo = Math.max(-scale, c.rel_ci_low);
  const hi = Math.min(scale, c.rel_ci_high);
  return (
    <div className="ci-bar" title={`${fmtPct(c.rel_ci_low)} to ${fmtPct(c.rel_ci_high)}`}>
      <div className="axis" />
      <div className="zero" style={{ left: "50%" }} />
      <div className="range" style={{ left: pos(lo), width: `calc(${pos(hi)} - ${pos(lo)})`, background: color, opacity: 0.35 }} />
      <div className="point" style={{ left: pos(c.rel_diff), background: color }} />
      <div className="faint" style={{ position: "absolute", top: 17, left: 0, right: 0, fontSize: 10.5, display: "flex", justifyContent: "space-between" }}>
        <span>{fmtPct(c.rel_ci_low, 1)}</span>
        <span>{fmtPct(c.rel_ci_high, 1)}</span>
      </div>
    </div>
  );
}

function Trend({
  experimentId,
  metric,
  variants,
  from,
  to,
  alpha,
}: {
  experimentId: number;
  metric?: MetricResult;
  variants: ReportVariant[];
  from: string;
  to: string;
  alpha: string;
}) {
  const [mode, setMode] = useState<"lift" | "value">("lift");
  const t = useAsync(async () => (metric ? api.trend(experimentId, metric.metric_id, { from, to, alpha }) : []), [experimentId, metric?.metric_id, from, to, alpha]);
  if (!metric) return null;
  const pts = t.data ?? [];
  const control = variants.find((v) => v.is_control) ?? variants[0];
  const treatments = variants.filter((v) => v.id !== control.id);
  return (
    <section className="card card-pad stack">
      <div className="row-between">
        <div>
          <h2>{metric.name} over time</h2>
          <p className="faint small">
            Cumulative from {from}: each day includes every unit exposed so far. Intervals narrow as data accumulates; wait for them to settle before
            deciding.
          </p>
        </div>
        <Segmented
          options={[
            ["lift", "Relative lift"],
            ["value", "Metric value"],
          ]}
          value={mode}
          onChange={setMode}
        />
      </div>
      {t.loading && !t.data ? (
        <Loading />
      ) : mode === "lift" ? (
        <LineChart
          labels={pts.map((p) => p.day)}
          zeroLine
          series={treatments.map((tr) => ({
            name: `${tr.name || tr.key} vs ${control.name || control.key}`,
            points: pts.map((p) => p.comparisons.find((c) => c.variant_id === tr.id)?.rel_diff ?? null),
            band: pts.map((p) => {
              const c = p.comparisons.find((x) => x.variant_id === tr.id);
              return [c?.rel_ci_low ?? null, c?.rel_ci_high ?? null];
            }),
          }))}
          format={(v) => fmtPct(v, 1)}
        />
      ) : (
        <LineChart
          labels={pts.map((p) => p.day)}
          series={variants.map((v) => ({
            name: v.name || v.key,
            points: pts.map((p) => p.values.find((x) => x.variant_id === v.id)?.value ?? null),
          }))}
          format={(v) => fmtValue(v, metric.format, metric.format === "percent" ? 1 : 2)}
        />
      )}
    </section>
  );
}

// configuredLabel spells out what "as set up" means for this experiment:
// the default groups of its business and platform plus the groups chosen
// on the experiment.
function configuredLabel(e: Experiment, groups: MetricGroup[]): string {
  const mine = groups.filter(
    (g) =>
      (g.metric_ids ?? []).length > 0 &&
      ((g.is_default && (g.business_id === e.business_id || (!!g.platform_id && g.platform_id === e.platform_id))) || e.metric_group_ids.includes(g.id))
  );
  const dup = (n: string) => mine.filter((g) => g.name === n).length > 1;
  return mine.length ? mine.map((g) => (dup(g.name) ? `${g.name} (${g.owner})` : g.name)).join(", ") : `no groups — shows every ${e.business_name} metric`;
}

// MetricsChooser: three plain choices — the experiment's setup, every
// metric of the business, or a custom pick of groups or single metrics.
function MetricsChooser({
  e,
  pick,
  onPick,
  current,
  groups,
  metrics,
  configured,
}: {
  e: Experiment;
  pick: Selection;
  onPick: (p: Selection) => void;
  current: number[];
  groups: MetricGroup[];
  metrics: MetricBrief[];
  configured: string;
}) {
  const custom = pick.kind === "groups" || pick.kind === "metrics";
  const [tab, setTab] = useState<"groups" | "metrics">(pick.kind === "metrics" ? "metrics" : "groups");
  const [others, setOthers] = useState(false);
  const [q, setQ] = useState("");
  const ownNames = [e.business_name, e.platform_name];
  const pickable = metrics.filter((m) => others || ownNames.includes(m.owner));
  const s = q.trim().toLowerCase();
  const shownMetrics = pickable.filter((m) => !s || [m.name, m.key, m.owner].some((x) => x.toLowerCase().includes(s)));
  const selectedMetrics = pick.kind === "metrics" ? pick.ids : [];
  const option = (kind: "experiment" | "all" | "custom", title: string, desc: string) => {
    const on = kind === "custom" ? custom : pick.kind === kind;
    return (
      <label className={`gpick-item ${on ? "on" : ""}`} style={{ alignItems: "flex-start" }}>
        <input
          type="radio"
          checked={on}
          onChange={() =>
            onPick(kind === "custom" ? (tab === "metrics" ? { kind: "metrics", ids: [] } : { kind: "groups", ids: current }) : { kind })
          }
        />
        <span>
          <span style={{ fontWeight: 600 }}>{title}</span>
          <span className="faint small" style={{ display: "block" }}>
            {desc}
          </span>
        </span>
      </label>
    );
  };
  return (
    <div className="stack-sm">
      <b>Which metrics should this report show?</b>
      <div className="choice-grid">
        {option("experiment", "As set up on the experiment", configured)}
        {option("all", `Every ${e.business_name} metric`, `All metrics defined for ${e.business_name} and shared by ${e.platform_name}`)}
        {option("custom", "Custom", "Pick metric groups, or single metrics")}
      </div>
      {custom && (
        <>
          <div className="row-between">
            <Segmented<"groups" | "metrics">
              options={[
                ["groups", "By group"],
                ["metrics", "Single metrics"],
              ]}
              value={tab}
              onChange={(t) => {
                setTab(t);
                onPick(t === "metrics" ? { kind: "metrics", ids: [] } : { kind: "groups", ids: current });
              }}
            />
            {tab === "metrics" && (
              <label className="check small">
                <input type="checkbox" checked={others} onChange={(x) => setOthers(x.target.checked)} /> Include other platforms
              </label>
            )}
          </div>
          {tab === "groups" ? (
            <GroupPicker
              groups={groups}
              platformName={e.platform_name}
              metrics={metrics}
              value={pick.kind === "groups" ? pick.ids : []}
              onChange={(ids) => onPick({ kind: "groups", ids })}
              businessId={e.business_id}
              platformId={e.platform_id}
              lockDefaults={false}
            />
          ) : (
            <div className="stack-sm">
              <div className="row" style={{ gap: 8 }}>
                <input className="input" style={{ flex: 1 }} placeholder="Search metrics…" value={q} onChange={(x) => setQ(x.target.value)} />
                <span className="small faint">{selectedMetrics.length} selected</span>
                <button className="btn btn-sm" onClick={() => onPick({ kind: "metrics", ids: Array.from(new Set([...selectedMetrics, ...shownMetrics.map((m) => m.id)])) })}>
                  Select all{s ? " shown" : ""}
                </button>
                <button className="btn btn-sm" onClick={() => onPick({ kind: "metrics", ids: [] })} disabled={selectedMetrics.length === 0}>
                  Clear
                </button>
              </div>
              <div className="gpick-items" style={{ maxHeight: 320, overflow: "auto" }}>
                {shownMetrics.map((m) => {
                  const on = selectedMetrics.includes(m.id);
                  return (
                    <label key={m.id} className={`gpick-item ${on ? "on" : ""}`}>
                      <input
                        type="checkbox"
                        checked={on}
                        onChange={() => onPick({ kind: "metrics", ids: on ? selectedMetrics.filter((x) => x !== m.id) : [...selectedMetrics, m.id] })}
                      />
                      <span style={{ minWidth: 0 }}>
                        <span style={{ fontWeight: 600 }}>{m.name}</span>
                        <span className="faint small" style={{ display: "block" }}>
                          <span className="mono">{m.key}</span> · {m.owner}
                        </span>
                      </span>
                    </label>
                  );
                })}
                {shownMetrics.length === 0 && <div className="small faint">No metrics match.</div>}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
