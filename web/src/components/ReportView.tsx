import { Fragment, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import { ago, fmtInt, fmtP, fmtPct, fmtValue } from "../lib/format";
import { useAsync } from "../lib/hooks";
import type { Comparison, Experiment, MetricResult, Report, ReportVariant } from "../lib/types";
import { LineChart } from "./LineChart";
import { GroupPicker } from "./GroupPicker";
import { Empty, ErrorBox, Field, Loading, Popover, Segmented, Stat, seriesColor } from "./ui";

const verdictText: Record<Comparison["verdict"], string> = {
  better: "Better",
  worse: "Worse",
  changed: "Changed",
  flat: "Not significant",
  untestable: "Too little data",
};
const verdictClass: Record<Comparison["verdict"], string> = {
  better: "b-good",
  worse: "b-bad",
  changed: "b-accent",
  flat: "",
  untestable: "",
};

type Layout = "detailed" | "matrix";

export default function ReportView({ experiment: e }: { experiment: Experiment }) {
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  // Metric selection: the experiment's own groups (with defaults), a custom
  // set of groups, or every metric the business can see.
  const [pick, setPick] = useState<{ kind: "experiment" } | { kind: "groups"; ids: number[] } | { kind: "all" }>({ kind: "experiment" });
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

  const pickKey = pick.kind === "groups" ? pick.ids.join(",") : pick.kind;
  const rep = useAsync(async () => {
    if (pick.kind === "all") {
      const metrics = (await api.metrics({ kind: "business", id: e.business_id })).map((m) => m.id).join(",");
      return api.report(e.id, { from, to, metrics, dimension, alpha });
    }
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
  const groupLabel =
    pick.kind === "all" ? "All business metrics" : pick.kind === "groups" ? `${pick.ids.length} group${pick.ids.length === 1 ? "" : "s"} (custom)` : "Experiment's groups";

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
        <Popover anchor={picker} onClose={() => setPicker(null)} width={720}>
          <div className="stack-sm">
            <div className="row-between">
              <b>Metrics in this report</b>
              <div className="row" style={{ gap: 6 }}>
                <button className={`btn btn-sm ${pick.kind === "experiment" ? "btn-primary" : ""}`} onClick={() => setPick({ kind: "experiment" })}>
                  Experiment's groups
                </button>
                <button className={`btn btn-sm ${pick.kind === "all" ? "btn-primary" : ""}`} onClick={() => setPick({ kind: "all" })}>
                  All business metrics
                </button>
              </div>
            </div>
            <GroupPicker
              groups={refs.data.groups}
              metrics={refs.data.metrics}
              value={pick.kind === "groups" ? pick.ids : pick.kind === "experiment" ? (r?.groups ?? []).map((g) => g.id).filter(Boolean) : []}
              onChange={(ids) => setPick({ kind: "groups", ids })}
              businessId={-1}
              platformId={-1}
            />
          </div>
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
          {r.segments.map((seg) => (
            <section key={seg.value} className="card">
              <div className="card-head">
                <div>
                  <h2>{r.dimension ? `${r.dimension} = ${seg.value}` : "Results"}</h2>
                  <p>
                    {fmtInt(seg.units)} units · {r.from} to {r.to} · {Math.round((1 - r.alpha) * 100)}% confidence intervals · click a metric for its trend
                  </p>
                </div>
              </div>
              {r.groups.map((g) => {
                const byId = new Map(seg.metrics.map((m) => [m.metric_id, m]));
                const ms = (g.metric_ids ?? []).map((id) => byId.get(id)).filter((m): m is MetricResult => !!m);
                return (
                  <div key={`${g.id}-${g.name}`} className="rep-group">
                    {(r.groups.length > 1 || g.id !== 0) && (
                      <div className="rep-group-head">
                        <b>{g.name}</b> <span className="faint small">{g.owner}</span>{" "}
                        {g.is_default && (
                          <span className="badge b-good" title="A default group: included in every experiment of its business or platform">
                            default
                          </span>
                        )}
                        <span className="faint small"> · {ms.length} metrics</span>
                      </div>
                    )}
                    {lay === "matrix" && control ? (
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
            <th className="num">{control.name || control.key}</th>
            {treatments.map((t) => (
              <th key={t.id} className="num" title={t.key}>
                <span className="dot" style={{ background: seriesColor(all.findIndex((x) => x.id === t.id)), marginRight: 6 }} />
                {t.name || t.key}
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
                      )} · p ${fmtP(c.p_value)} · ${verdictText[c.verdict]}`}
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
                      <td>{c && <span className={`badge ${verdictClass[c.verdict]}`}>{verdictText[c.verdict]}</span>}</td>
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
