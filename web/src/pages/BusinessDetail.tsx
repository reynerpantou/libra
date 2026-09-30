import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { LineChart } from "../components/LineChart";
import { Empty, ErrorBox, Field, Icon, Loading, Modal, Tabs } from "../components/ui";
import { api, BASE, type MeasureInput, type MetricInput } from "../lib/api";
import { useCan } from "../lib/auth";
import { fmtDateTime, fmtInt, fmtValue } from "../lib/format";
import { useAsync, useDebounced } from "../lib/hooks";
import type { Business, EventSummary, Filter, Measure, Metric, MetricGroup, Scope } from "../lib/types";
import { slug } from "./Businesses";

type Tab = "metrics" | "measures" | "groups" | "data" | "settings";

export default function BusinessDetail() {
  const id = Number(useParams().id);
  const [params, setParams] = useSearchParams();
  const tab = (params.get("tab") as Tab) || "metrics";
  const scope: Scope = useMemo(() => ({ kind: "business", id }), [id]);
  const biz = useAsync(() => api.business(id), [id]);
  const measures = useAsync(() => api.measures(scope), [scope]);
  const metrics = useAsync(() => api.metrics(scope), [scope]);
  const groups = useAsync(() => api.groups(scope), [scope]);
  const events = useAsync(() => api.eventSummary(id), [id]);
  const isAdmin = useCan("admin");
  const b = biz.data;
  if (biz.loading && !b) return <div className="page"><Loading /></div>;
  if (!b) return <div className="page"><ErrorBox error={biz.error || "Not found"} /></div>;
  const reloadDefs = () => {
    measures.reload();
    metrics.reload();
    groups.reload();
  };
  return (
    <div className="page">
      <div className="crumbs">
        <Link to="/businesses">Businesses</Link> / <Link to={`/platforms/${b.platform_id}`}>{b.platform_name}</Link> / {b.key}
      </div>
      <div className="page-head">
        <div>
          <div className="row">
            <h1>{b.name}</h1>
            <span className="chip">{b.key}</span>
            <span className="chip" title="Business id">id {b.id}</span>
            <Link className="chip" to={`/platforms/${b.platform_id}`} title="Platform: its measures, metrics and default groups apply here too">
              {b.platform_name}
            </Link>
          </div>
          <p>{b.description}</p>
        </div>
        <Link className="btn" to={`/experiments?business=${b.key}`}>
          View experiments
        </Link>
      </div>
      <Tabs<Tab>
        tabs={[
          ["metrics", `Metrics (${metrics.data?.length ?? 0})`],
          ["measures", `Measures (${measures.data?.length ?? 0})`],
          ["groups", `Metric groups (${groups.data?.length ?? 0})`],
          ["data", "Incoming data"],
          ...(isAdmin ? ([["settings", "Settings"]] as [Tab, string][]) : []),
        ]}
        value={tab}
        onChange={(t) => setParams({ tab: t }, { replace: true })}
      />
      {tab === "metrics" && <Metrics scope={scope} metrics={metrics.data ?? []} measures={measures.data ?? []} reload={reloadDefs} />}
      {tab === "measures" && <Measures scope={scope} measures={measures.data ?? []} events={events.data} reload={reloadDefs} />}
      {tab === "groups" && <Groups scope={scope} groups={groups.data ?? []} metrics={metrics.data ?? []} reload={groups.reload} />}
      {tab === "data" && <Data business={b} events={events.data} />}
      {tab === "settings" && <Settings business={b} onSaved={(x) => biz.setData(x)} />}
    </div>
  );
}

// ---------- metrics ----------

// Inherited: defined on the platform, shown read-only in a business.
function PlatformBadge({ on }: { on?: boolean }) {
  return on ? (
    <span className="badge b-accent" title="Defined on the platform; shared by every business under it. Edit it on the platform page.">
      platform
    </span>
  ) : null;
}

const kindHelp: Record<string, string> = {
  ratio: "Ratio metric — compared directly between variants (e.g. CTR, GMV per user).",
  total: "Total metric — variants differ in size, so the report shows totals and compares them per exposed unit.",
  mixed: "Mixed scale — compared per exposed unit. Consider rewriting as a ratio or a total.",
};

export function Metrics({ scope, metrics, measures, reload }: { scope: Scope; metrics: Metric[]; measures: Measure[]; reload: () => void }) {
  const canEdit = useCan("editor");
  const [editing, setEditing] = useState<Metric | "new" | null>(null);
  const [error, setError] = useState("");
  return (
    <div className="stack">
      <div className="alert alert-info small">
        A metric is a <b>formula</b> over measures. Use <code>+ − × ÷</code>, numbers, parentheses, any measure key, other metric keys, and{" "}
        <code>users</code> (units exposed to the experiment). Example: <code>search_gmv / users</code>, <code>search_clicks / search_impressions</code>.
        Changes apply to every report immediately — no reprocessing needed.
      </div>
      <section className="card">
        <div className="card-head">
          <h2>Metrics</h2>
          {canEdit && (
            <button className="btn btn-primary btn-sm" onClick={() => setEditing("new")} disabled={measures.length === 0}>
              <Icon name="plus" /> New metric
            </button>
          )}
        </div>
        <ErrorBox error={error} />
        {metrics.length === 0 ? (
          <Empty title="No metrics yet">{measures.length === 0 ? <p>Define measures first — metrics are formulas over them.</p> : null}</Empty>
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Metric</th>
                  <th>Formula</th>
                  <th>Type</th>
                  <th>Good when</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {metrics.map((m) => (
                  <tr key={m.id}>
                    <td>
                      <div style={{ fontWeight: 600 }}>
                        {m.name} <PlatformBadge on={m.inherited} />
                      </div>
                      <div className="mono faint">{m.key}</div>
                    </td>
                    <td>
                      <code>{m.formula}</code>
                      {m.expanded && m.expanded !== m.formula && <div className="metric-formula">= {m.expanded}</div>}
                      {m.error && <div className="small" style={{ color: "var(--bad)" }}>{m.error}</div>}
                    </td>
                    <td>
                      <span className="badge" title={kindHelp[m.kind]}>
                        {m.kind}
                      </span>{" "}
                      <span className="faint small">{m.format}</span>
                    </td>
                    <td className="small">{m.direction === "increase" ? "↑ higher" : m.direction === "decrease" ? "↓ lower" : "neutral"}</td>
                    <td className="right nowrap">
                      {canEdit && !m.inherited && (
                        <>
                          <button className="icon-btn" aria-label="Edit" onClick={() => setEditing(m)}>
                            <Icon name="edit" size={16} />
                          </button>
                          <button
                            className="icon-btn"
                            aria-label="Delete"
                            onClick={async () => {
                              if (!confirm(`Delete metric ${m.name}?`)) return;
                              setError("");
                              try {
                                await api.deleteMetric(m.id);
                                reload();
                              } catch (e) {
                                setError(e instanceof Error ? e.message : String(e));
                              }
                            }}
                          >
                            <Icon name="trash" size={16} />
                          </button>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {editing && (
        <MetricModal
          scope={scope}
          metric={editing === "new" ? null : editing}
          measures={measures}
          metrics={metrics}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

function MetricModal({
  scope,
  metric,
  measures,
  metrics,
  onClose,
  onSaved,
}: {
  scope: Scope;
  metric: Metric | null;
  measures: Measure[];
  metrics: Metric[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [f, setF] = useState<MetricInput>(
    metric
      ? { key: metric.key, name: metric.name, description: metric.description, formula: metric.formula, format: metric.format, decimals: metric.decimals, direction: metric.direction }
      : { key: "", name: "", description: "", formula: "", format: "number", decimals: 2, direction: "increase" }
  );
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<Awaited<ReturnType<typeof api.previewFormula>> | null>(null);
  const formula = useDebounced(f.formula, 300);
  const [check, setCheck] = useState<Awaited<ReturnType<typeof api.validateFormula>> | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (!formula.trim()) {
      setCheck(null);
      return;
    }
    api.validateFormula(scope, formula, f.key).then(setCheck).catch(() => setCheck(null));
    setPreview(null);
  }, [formula, scope, f.key]);

  const insert = (token: string) => {
    const el = ref.current;
    const s = f.formula;
    const at = el ? el.selectionStart : s.length;
    const end = el ? el.selectionEnd : s.length;
    const before = s.slice(0, at);
    const pad = before && !/[\s(]$/.test(before) ? " " : "";
    const next = `${before}${pad}${token}${s.slice(end)}`;
    setF({ ...f, formula: next });
    requestAnimationFrame(() => {
      el?.focus();
      const p = before.length + pad.length + token.length;
      el?.setSelectionRange(p, p);
    });
  };

  const save = async () => {
    setError("");
    try {
      if (metric) await api.updateMetric(metric.id, f);
      else await api.createMetric(scope, f);
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <Modal
      wide
      title={metric ? `Edit ${metric.name}` : "New metric"}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={save} disabled={!!check && !check.ok}>
            Save metric
          </button>
        </>
      }
    >
      <div className="grid-2">
        <Field label="Name">
          <input
            className="input"
            value={f.name}
            placeholder="Search GMV / User"
            onChange={(e) => setF({ ...f, name: e.target.value, key: !metric && (!f.key || f.key === slug(f.name)) ? slug(e.target.value) : f.key })}
          />
        </Field>
        <Field label="Key" hint="Used to reference this metric from other formulas.">
          <input className="input input-mono" value={f.key} onChange={(e) => setF({ ...f, key: e.target.value })} />
        </Field>
      </div>
      <Field label="Formula">
        <textarea
          ref={ref}
          className="input formula-box"
          rows={3}
          value={f.formula}
          placeholder="search_gmv / users"
          onChange={(e) => setF({ ...f, formula: e.target.value })}
          spellCheck={false}
        />
      </Field>
      <div className="stack-sm">
        <div className="small faint">Click to insert:</div>
        <div className="pill-row">
          <button className="chip" onClick={() => insert("users")} title="Units exposed to the experiment">
            users
          </button>
          {measures.map((m) => (
            <button key={m.id} className="chip" onClick={() => insert(m.key)} title={`${m.name}: ${m.aggregation} of ${m.event_name}`}>
              {m.key}
            </button>
          ))}
        </div>
        {metrics.filter((m) => m.key !== metric?.key).length > 0 && (
          <div className="pill-row">
            {metrics
              .filter((m) => m.key !== metric?.key)
              .map((m) => (
                <button key={m.id} className="chip" style={{ borderStyle: "dashed" }} onClick={() => insert(m.key)} title={`Metric: ${m.formula}`}>
                  {m.key}
                </button>
              ))}
          </div>
        )}
        <div className="pill-row">
          {["+", "-", "*", "/", "(", ")"].map((op) => (
            <button key={op} className="chip" onClick={() => insert(op)}>
              {op}
            </button>
          ))}
        </div>
      </div>
      {check &&
        (check.ok ? (
          <div className="alert alert-good small">
            <div>
              Valid · <b>{check.kind}</b> — {kindHelp[check.kind ?? "ratio"]}
            </div>
            {check.expanded !== f.formula.trim() && (
              <div className="mono" style={{ marginTop: 4 }}>
                = {check.expanded}
              </div>
            )}
          </div>
        ) : (
          <div className="alert alert-bad small">{check.error}</div>
        ))}
      <div className="row">
        <button
          className="btn btn-sm"
          disabled={!check?.ok}
          onClick={async () => setPreview(await api.previewFormula(scope, f.formula, 7))}
        >
          Preview on last 7 days
        </button>
        {preview &&
          (preview.ok ? (
            <span className="small">
              <b>{fmtValue(preview.value ?? null, f.format, f.decimals)}</b>{" "}
              <span className="faint">
                over {fmtInt(preview.users)} active units ({preview.from} – {preview.to})
                {preview.measures &&
                  " · " +
                    Object.entries(preview.measures)
                      .map(([k, v]) => `${k} = ${fmtValue(v, "number", 2)}`)
                      .join(", ")}
              </span>
            </span>
          ) : (
            <span className="small" style={{ color: "var(--bad)" }}>
              {preview.error}
            </span>
          ))}
      </div>
      <div className="grid-3">
        <Field label="Display as">
          <select className="input" value={f.format} onChange={(e) => setF({ ...f, format: e.target.value as MetricInput["format"] })}>
            <option value="number">Number</option>
            <option value="percent">Percent</option>
            <option value="currency">Currency</option>
          </select>
        </Field>
        <Field label="Decimals">
          <input className="input" type="number" min={0} max={6} value={f.decimals} onChange={(e) => setF({ ...f, decimals: Number(e.target.value) })} />
        </Field>
        <Field label="Good when it goes">
          <select className="input" value={f.direction} onChange={(e) => setF({ ...f, direction: e.target.value as MetricInput["direction"] })}>
            <option value="increase">Up</option>
            <option value="decrease">Down</option>
            <option value="neutral">Either way (just flag changes)</option>
          </select>
        </Field>
      </div>
      <Field label="Description">
        <input className="input" value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
      </Field>
      <ErrorBox error={error} />
    </Modal>
  );
}

// ---------- measures ----------

const AGG_HELP: Record<string, string> = {
  count: "number of matching events",
  sum: "sum of the value",
  max: "largest value",
  any: "1 if the unit had any matching event",
};

export function Measures({ scope, measures, events, reload }: { scope: Scope; measures: Measure[]; events: EventSummary | null; reload: () => void }) {
  const canEdit = useCan("editor");
  const [editing, setEditing] = useState<Measure | "new" | null>(null);
  const [error, setError] = useState("");
  return (
    <div className="stack">
      <div className="alert alert-info small">
        A <b>measure</b> turns raw events into one number per unit per day — e.g. <i>search GMV = sum of value of <code>order</code> events where{" "}
        <code>source = search</code></i>. The pipeline computes measures incrementally; editing one recomputes its history in the background.
        {scope.kind === "platform" && <> A platform measure counts the events of <b>every business</b> under the platform.</>}
      </div>
      <section className="card">
        <div className="card-head">
          <h2>Measures</h2>
          {canEdit && (
            <button className="btn btn-primary btn-sm" onClick={() => setEditing("new")}>
              <Icon name="plus" /> New measure
            </button>
          )}
        </div>
        <ErrorBox error={error} />
        {measures.length === 0 ? (
          <Empty title="No measures yet">
            <p>Send events to the ingestion API, then define measures over them.</p>
          </Empty>
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Measure</th>
                  <th>Definition</th>
                  <th>Used by</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {measures.map((m) => (
                  <tr key={m.id}>
                    <td>
                      <div style={{ fontWeight: 600 }}>
                        {m.name} <PlatformBadge on={m.inherited} /> {m.pending_backfill && <span className="badge b-warn">recomputing</span>}
                      </div>
                      <div className="mono faint">{m.key}</div>
                    </td>
                    <td className="small">
                      <b>{m.aggregation}</b>
                      {m.aggregation === "sum" || m.aggregation === "max" ? ` of ${m.value_field || "value"}` : ""} over <code>{m.event_name}</code>
                      {(m.filters_text ?? []).length > 0 && <span className="faint"> where {m.filters_text!.join(" and ")}</span>}
                    </td>
                    <td>
                      <div className="pill-row">
                        {(m.used_by ?? []).map((k) => (
                          <span key={k} className="chip">
                            {k}
                          </span>
                        ))}
                      </div>
                    </td>
                    <td className="right nowrap">
                      {canEdit && !m.inherited && (
                        <>
                          <button className="icon-btn" aria-label="Edit" onClick={() => setEditing(m)}>
                            <Icon name="edit" size={16} />
                          </button>
                          <button
                            className="icon-btn"
                            aria-label="Delete"
                            onClick={async () => {
                              if (!confirm(`Delete measure ${m.name}?`)) return;
                              setError("");
                              try {
                                await api.deleteMeasure(m.id);
                                reload();
                              } catch (e) {
                                setError(e instanceof Error ? e.message : String(e));
                              }
                            }}
                          >
                            <Icon name="trash" size={16} />
                          </button>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {editing && (
        <MeasureModal
          scope={scope}
          measure={editing === "new" ? null : editing}
          events={events}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

const FILTER_OPS: [string, string][] = [
  ["eq", "="],
  ["neq", "≠"],
  ["in", "in"],
  ["not_in", "not in"],
  ["gt", ">"],
  ["gte", "≥"],
  ["lt", "<"],
  ["lte", "≤"],
  ["exists", "is set"],
  ["not_exists", "is not set"],
];

function MeasureModal({
  scope,
  measure,
  events,
  onClose,
  onSaved,
}: {
  scope: Scope;
  measure: Measure | null;
  events: EventSummary | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [f, setF] = useState<MeasureInput>(
    measure
      ? { key: measure.key, name: measure.name, description: measure.description, event_name: measure.event_name, aggregation: measure.aggregation, value_field: measure.value_field, filters: measure.filters }
      : { key: "", name: "", description: "", event_name: events?.events[0]?.name ?? "", aggregation: "count", value_field: "", filters: [] }
  );
  const [error, setError] = useState("");
  const props = useMemo(() => events?.events.find((e) => e.name === f.event_name)?.props ?? [], [events, f.event_name]);
  const setFilter = (i: number, patch: Partial<Filter>) => setF({ ...f, filters: f.filters.map((x, j) => (j === i ? { ...x, ...patch } : x)) });
  const save = async () => {
    setError("");
    try {
      if (measure) await api.updateMeasure(measure.id, f);
      else await api.createMeasure(scope, f);
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Modal
      wide
      title={measure ? `Edit ${measure.name}` : "New measure"}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={save}>
            Save measure
          </button>
        </>
      }
    >
      <datalist id="event-names">{events?.events.map((e) => <option key={e.name} value={e.name} />)}</datalist>
      <datalist id="event-props">
        {props.map((p) => (
          <option key={p} value={p} />
        ))}
      </datalist>
      <div className="grid-2">
        <Field label="Name">
          <input
            className="input"
            value={f.name}
            placeholder="Search GMV"
            onChange={(e) => setF({ ...f, name: e.target.value, key: !measure && (!f.key || f.key === slug(f.name)) ? slug(e.target.value) : f.key })}
          />
        </Field>
        <Field label="Key" hint="Formulas refer to the measure by this key.">
          <input className="input input-mono" value={f.key} onChange={(e) => setF({ ...f, key: e.target.value })} />
        </Field>
      </div>
      <div className="grid-3">
        <Field label="Event" hint={events?.events.length ? "Pick one seen recently, or type a name." : undefined}>
          <input className="input input-mono" list="event-names" value={f.event_name} onChange={(e) => setF({ ...f, event_name: e.target.value })} />
        </Field>
        <Field label="Aggregation" hint={AGG_HELP[f.aggregation]}>
          <select className="input" value={f.aggregation} onChange={(e) => setF({ ...f, aggregation: e.target.value as MeasureInput["aggregation"] })}>
            <option value="count">count</option>
            <option value="sum">sum</option>
            <option value="max">max</option>
            <option value="any">any (0/1)</option>
          </select>
        </Field>
        {(f.aggregation === "sum" || f.aggregation === "max") && (
          <Field label="Of" hint="The event's value, or a numeric property.">
            <input className="input input-mono" list="event-props" placeholder="value" value={f.value_field} onChange={(e) => setF({ ...f, value_field: e.target.value })} />
          </Field>
        )}
      </div>
      <div className="stack-sm">
        <div className="row-between">
          <b className="small">Only events where</b>
          <button className="btn btn-sm" onClick={() => setF({ ...f, filters: [...f.filters, { field: props[0] ?? "", op: "eq", values: [] }] })}>
            <Icon name="plus" /> Add filter
          </button>
        </div>
        {f.filters.length === 0 && <div className="faint small">All {f.event_name || "matching"} events count.</div>}
        {f.filters.map((flt, i) => (
          <div key={i} className="row">
            <input className="input input-mono" style={{ width: 170 }} list="event-props" placeholder="property" value={flt.field} onChange={(e) => setFilter(i, { field: e.target.value })} />
            <select className="input" style={{ width: 110 }} value={flt.op} onChange={(e) => setFilter(i, { op: e.target.value })}>
              {FILTER_OPS.map(([k, l]) => (
                <option key={k} value={k}>
                  {l}
                </option>
              ))}
            </select>
            {flt.op !== "exists" && flt.op !== "not_exists" && (
              <input
                className="input"
                style={{ flex: 1, minWidth: 140 }}
                placeholder={flt.op === "in" || flt.op === "not_in" ? "a, b, c" : "value"}
                value={flt.values.join(", ")}
                onChange={(e) => setFilter(i, { values: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })}
              />
            )}
            <button className="icon-btn" aria-label="Remove filter" onClick={() => setF({ ...f, filters: f.filters.filter((_, j) => j !== i) })}>
              <Icon name="trash" size={16} />
            </button>
          </div>
        ))}
        {f.filters.length > 0 && <div className="faint small">Booleans compare as text: use <code>true</code> / <code>false</code>.</div>}
      </div>
      <Field label="Description">
        <input className="input" value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
      </Field>
      {measure && <div className="alert alert-warn small">Saving recomputes this measure over all past events (the report updates after the next pipeline run).</div>}
      <ErrorBox error={error} />
    </Modal>
  );
}

// ---------- metric groups ----------

export function Groups({ scope, groups, metrics, reload }: { scope: Scope; groups: MetricGroup[]; metrics: Metric[]; reload: () => void }) {
  const canEdit = useCan("editor");
  const [editing, setEditing] = useState<MetricGroup | "new" | null>(null);
  const byId = new Map(metrics.map((m) => [m.id, m]));
  return (
    <div className="stack">
      <div className="row-between">
        <p className="muted">
          Metric groups are reusable report templates (e.g. Back end, Front end, Product). An experiment picks any number of groups, from any business.
          A <b>default</b> group is added to every experiment of this {scope.kind === "platform" ? "platform's businesses" : "business"} automatically.
        </p>
        {canEdit && (
          <button className="btn btn-primary btn-sm" onClick={() => setEditing("new")} disabled={metrics.length === 0}>
            <Icon name="plus" /> New group
          </button>
        )}
      </div>
      {groups.length === 0 ? (
        <div className="card">
          <Empty title="No metric groups" />
        </div>
      ) : (
        <div className="grid-3">
          {[...groups]
            .sort((a, b) => Number(!!b.builtin) - Number(!!a.builtin))
            .map((g) => (
            <div key={g.id} className={`card card-pad stack-sm ${g.builtin ? "group-builtin" : ""}`}>
              <div className="row-between">
                <h3>
                  {g.name} {g.is_default && <span className="badge b-good" title="Included in every experiment automatically">default</span>}{" "}
                  {g.builtin && (
                    <span className="badge" title="Every platform has this group. Add metrics to report them for every experiment of the platform.">
                      built in
                    </span>
                  )}{" "}
                  <PlatformBadge on={g.inherited} />
                </h3>
                {canEdit && !g.inherited && (
                  <div className="row" style={{ gap: 2 }}>
                    <button className="icon-btn" aria-label="Edit" onClick={() => setEditing(g)}>
                      <Icon name="edit" size={15} />
                    </button>
                    {!g.builtin && <button
                      className="icon-btn"
                      aria-label="Delete"
                      onClick={async () => {
                        if (confirm(`Delete group ${g.name}?`)) {
                          await api.deleteGroup(g.id);
                          reload();
                        }
                      }}
                    >
                      <Icon name="trash" size={15} />
                    </button>}
                  </div>
                )}
              </div>
              {g.description && <p className="faint small">{g.description}</p>}
              {g.metric_ids.length === 0 ? (
                <div className="small faint">
                  No metrics yet.{" "}
                  {canEdit && !g.inherited &&
                    (metrics.length === 0 ? (
                      <Link to="?tab=metrics">Create a metric first</Link>
                    ) : (
                      <button className="btn btn-sm" onClick={() => setEditing(g)}>
                        <Icon name="plus" /> Add metrics
                      </button>
                    ))}
                </div>
              ) : (
                <ol style={{ margin: 0, paddingLeft: 18 }} className="small">
                  {g.metric_ids.map((id) => (
                    <li key={id}>{byId.get(id)?.name ?? `#${id}`}</li>
                  ))}
                </ol>
              )}
            </div>
          ))}
        </div>
      )}
      {editing && (
        <GroupModal
          scope={scope}
          group={editing === "new" ? null : editing}
          metrics={metrics}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
          }}
        />
      )}
    </div>
  );
}

function GroupModal({
  scope,
  group,
  metrics,
  onClose,
  onSaved,
}: {
  scope: Scope;
  group: MetricGroup | null;
  metrics: Metric[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  const [ids, setIds] = useState<number[]>(group?.metric_ids ?? []);
  const [isDefault, setDefault] = useState(group?.is_default ?? false);
  const [error, setError] = useState("");
  const byId = new Map(metrics.map((m) => [m.id, m]));
  const move = (i: number, d: number) => {
    const next = [...ids];
    const j = i + d;
    if (j < 0 || j >= next.length) return;
    [next[i], next[j]] = [next[j], next[i]];
    setIds(next);
  };
  const save = async () => {
    try {
      const body = { name, description, is_default: isDefault, metric_ids: ids };
      if (group) await api.updateGroup(group.id, body);
      else await api.createGroup(scope, body);
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Modal
      title={group ? "Edit metric group" : "New metric group"}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={save}>
            Save
          </button>
        </>
      }
    >
      <Field label="Name">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Description">
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <label className="check">
        <input type="checkbox" checked={isDefault || !!group?.builtin} disabled={!!group?.builtin} onChange={(e) => setDefault(e.target.checked)} /> Default — include in
        every experiment of this{" "}
        {scope.kind === "platform" ? "platform (all its businesses)" : "business"}
      </label>
      <div className="grid-2">
        <div className="stack-sm">
          <b className="small">Available</b>
          {metrics
            .filter((m) => !ids.includes(m.id))
            .map((m) => (
              <button key={m.id} className="btn btn-sm" style={{ justifyContent: "flex-start" }} onClick={() => setIds([...ids, m.id])}>
                <Icon name="plus" /> {m.name} {m.inherited && <span className="faint small">· platform</span>}
              </button>
            ))}
        </div>
        <div className="stack-sm">
          <b className="small">In this group (report order)</b>
          {ids.map((id, i) => (
            <div key={id} className="row" style={{ gap: 4 }}>
              <span style={{ flex: 1 }} className="small">
                {i + 1}. {byId.get(id)?.name}
              </span>
              <button className="icon-btn" onClick={() => move(i, -1)} aria-label="Up">
                ↑
              </button>
              <button className="icon-btn" onClick={() => move(i, 1)} aria-label="Down">
                ↓
              </button>
              <button className="icon-btn" onClick={() => setIds(ids.filter((x) => x !== id))} aria-label="Remove">
                <Icon name="x" size={14} />
              </button>
            </div>
          ))}
        </div>
      </div>
      <ErrorBox error={error} />
    </Modal>
  );
}

// ---------- incoming data ----------

function Data({ business, events }: { business: Business; events: EventSummary | null }) {
  const recent = useAsync(() => api.recentEvents(business.id), [business.id]);
  const [selected, setSelected] = useState<string>("");
  const ev = events?.events ?? [];
  const cur = ev.find((e) => e.name === selected) ?? ev[0];
  return (
    <div className="stack">
      <section className="card">
        <div className="card-head">
          <div>
            <h2>Events received</h2>
            <p>Last 14 days (since {events?.since}). Send more with the ingestion API — see the <Link to="/integrate">integration guide</Link>.</p>
          </div>
        </div>
        {ev.length === 0 ? (
          <Empty title="No events yet" />
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Event</th>
                  <th className="num">Events</th>
                  <th className="num">Units</th>
                  <th>Properties seen</th>
                </tr>
              </thead>
              <tbody>
                {ev.map((e) => (
                  <tr key={e.name} className="clickable" onClick={() => setSelected(e.name)} style={cur?.name === e.name ? { background: "var(--accent-soft)" } : undefined}>
                    <td className="mono">{e.name}</td>
                    <td className="num">{fmtInt(e.total)}</td>
                    <td className="num">{fmtInt(e.units)}</td>
                    <td>
                      <div className="pill-row">
                        {e.props.map((p) => (
                          <span key={p} className="chip">
                            {p}
                          </span>
                        ))}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {cur && (
        <section className="card card-pad stack">
          <h2>
            <span className="mono">{cur.name}</span> per day
          </h2>
          <LineChart labels={cur.days.map((d) => d.day)} series={[{ name: "events", points: cur.days.map((d) => d.count) }]} format={(v) => Math.round(v).toLocaleString()} height={170} />
        </section>
      )}
      <section className="card">
        <div className="card-head">
          <h2>Latest events</h2>
          <button className="btn btn-sm" onClick={recent.reload}>
            <Icon name="refresh" /> Refresh
          </button>
        </div>
        <div className="table-wrap">
          <table className="tbl tbl-compact">
            <thead>
              <tr>
                <th>Time</th>
                <th>Event</th>
                <th>Unit</th>
                <th className="num">Value</th>
                <th>Props</th>
              </tr>
            </thead>
            <tbody>
              {recent.data?.map((e) => (
                <tr key={e.id}>
                  <td className="nowrap faint">{fmtDateTime(e.ts)}</td>
                  <td className="mono">{e.event}</td>
                  <td className="mono">{e.unit_id}</td>
                  <td className="num">{e.value}</td>
                  <td className="mono small">{JSON.stringify(e.props)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

// ---------- settings ----------

function Settings({ business, onSaved }: { business: Business; onSaved: (b: Business) => void }) {
  const [name, setName] = useState(business.name);
  const [description, setDescription] = useState(business.description);
  const [review, setReview] = useState(business.require_review);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  return (
    <section className="card card-pad stack" style={{ maxWidth: 640 }}>
      <Field label="Name">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="Description">
        <textarea className="input" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <Field label="Key" hint="Permanent — services send events with it.">
        <input className="input input-mono" value={business.key} disabled />
      </Field>
      <label className="check">
        <input type="checkbox" checked={review} onChange={(e) => setReview(e.target.checked)} /> Experiments need review by another editor before they start
      </label>
      <ErrorBox error={error} />
      {msg && <div className="alert alert-good small">{msg}</div>}
      <div>
        <button
          className="btn btn-primary"
          onClick={async () => {
            setError("");
            setMsg("");
            try {
              onSaved(await api.updateBusiness(business.id, { platform_id: business.platform_id, key: business.key, name, description, require_review: review }));
              setMsg("Saved.");
            } catch (e) {
              setError(e instanceof Error ? e.message : String(e));
            }
          }}
        >
          Save
        </button>
      </div>
      <hr style={{ border: 0, borderTop: "1px solid var(--border)", width: "100%" }} />
      <div className="row-between">
        <div className="small muted">
          {business.experiments > 0
            ? `Has ${business.experiments} experiment(s), so it can't be deleted. Move it to another platform from the platform page.`
            : "Delete this business with its measures, metrics, groups and events."}
        </div>
        <button
          className="btn btn-danger btn-sm"
          disabled={business.experiments > 0}
          onClick={async () => {
            if (!confirm(`Delete ${business.name}?`)) return;
            try {
              await api.deleteBusiness(business.id);
              window.location.assign(`${BASE}/platforms/${business.platform_id}`);
            } catch (e) {
              setError(e instanceof Error ? e.message : String(e));
            }
          }}
        >
          <Icon name="trash" /> Delete business
        </button>
      </div>
    </section>
  );
}
