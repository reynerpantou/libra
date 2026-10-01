import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Combobox } from "../components/Combobox";
import { JsonEditor, NamespacedJson } from "../components/JsonEditor";
import { TargetingEditor } from "../components/Targeting";
import { ErrorBox, Field, Icon, Loading, Modal, Segmented, TrafficBar } from "../components/ui";
import { api, type TuningInput } from "../lib/api";
import { useAuth } from "../lib/auth";
import { diversionName, useDiversions } from "../lib/diversions";
import { trafficPct } from "../lib/format";
import { useAsync, useDebounced } from "../lib/hooks";
import { algorithms } from "../lib/tuning";
import type { Metric, Targeting, TuningAlgorithm, TuningParam } from "../lib/types";

interface ParamDraft {
  path: string;
  type: "float" | "int";
  min: string;
  max: string;
  control: string;
  scale: "linear" | "log";
  step: string;
}

const blankParam = (): ParamDraft => ({ path: "", type: "float", min: "0", max: "1", control: "0.5", scale: "linear", step: "" });

export default function TuningForm() {
  const { id } = useParams();
  const editing = !!id;
  const nav = useNavigate();
  const { user } = useAuth();
  const diversions = useDiversions();
  const refs = useAsync(async () => {
    const [businesses, layers, attributes] = await Promise.all([api.businesses(), api.layers(), api.attributes()]);
    return { businesses, layers, attributes };
  }, []);
  const existing = useAsync(async () => (editing ? api.tuning(Number(id)) : null), [id]);

  const [name, setName] = useState("");
  const [hypothesis, setHypothesis] = useState("");
  const [description, setDescription] = useState("");
  const [platformPick, setPlatformPick] = useState("");
  const [businessIds, setBusinessIds] = useState<number[]>([]);
  const [mode, setMode] = useState<"auto" | "layer">("auto");
  const [autoDiversion, setAutoDiversion] = useState("");
  const [layerId, setLayerId] = useState(0);
  const [traffic, setTraffic] = useState(1000);
  const [targeting, setTargeting] = useState<Targeting>({ groups: [] });
  const [params, setParams] = useState<ParamDraft[]>([blankParam()]);
  const [baseText, setBaseText] = useState("{\n  \n}");
  const [algorithm, setAlgorithm] = useState<TuningAlgorithm>("bayesian");
  const [objectiveId, setObjectiveId] = useState(0);
  const [direction, setDirection] = useState<"" | "increase" | "decrease">("");
  const [guards, setGuards] = useState<{ metric_id: number; drop: string }[]>([]);
  const [arms, setArms] = useState("10");
  const [roundDays, setRoundDays] = useState("1");
  const [maxRounds, setMaxRounds] = useState("10");
  const [minUnits, setMinUnits] = useState("0");
  const [keepBest, setKeepBest] = useState(true);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [confirming, setConfirming] = useState(false);

  useEffect(() => {
    const t = existing.data;
    if (!t) return;
    const e = t.experiment;
    setName(e.name);
    setHypothesis(e.hypothesis);
    setDescription(e.description);
    setPlatformPick(e.platform_key);
    setBusinessIds(e.business_ids?.length ? e.business_ids : [e.business_id]);
    setMode(e.layer_auto ? "auto" : "layer");
    if (e.layer_auto) setAutoDiversion(e.layer_diversion);
    else setLayerId(e.layer_id);
    setTraffic(e.traffic_target);
    setTargeting(e.targeting);
    setParams(
      t.config.params.map((p) => ({ path: p.path, type: p.type, min: String(p.min), max: String(p.max), control: String(p.control), scale: p.scale, step: p.step ? String(p.step) : "" }))
    );
    setBaseText(JSON.stringify(t.config.base_params ?? {}, null, 2));
    setAlgorithm(t.config.algorithm);
    setObjectiveId(t.config.objective_metric_id);
    setDirection(t.config.objective_direction);
    setGuards(t.config.guardrails.map((g) => ({ metric_id: g.metric_id, drop: String(+(g.max_drop * 100).toFixed(3)) })));
    setArms(String(t.config.arms));
    setRoundDays(String(t.config.round_days));
    setMaxRounds(String(t.config.max_rounds));
    setMinUnits(String(t.config.min_units));
    setKeepBest(t.config.keep_best);
  }, [existing.data]);

  const businesses = refs.data?.businesses ?? [];
  const platformOptions = Array.from(new Map(businesses.map((b) => [b.platform_key, b.platform_name])).entries()).map(([k, n]) => ({ value: k, label: n, hint: k }));
  const chosen = businessIds.map((b) => businesses.find((x) => x.id === b)).filter((b): b is NonNullable<typeof b> => !!b);
  const platformKey = chosen[0]?.platform_key ?? platformPick;
  const platformName = platformOptions.find((o) => o.value === platformKey)?.label ?? "";
  const businessOptions = businesses.filter((b) => b.platform_key === platformKey && !businessIds.includes(b.id)).map((b) => ({ value: String(b.id), label: b.name, hint: b.key }));
  const locked = editing && !["draft", "approved", "rejected"].includes(existing.data?.experiment.status ?? "draft");

  // Metrics of the chosen businesses (each includes its platform's).
  const metrics = useAsync(async () => {
    const lists = await Promise.all(businessIds.map((b) => api.metrics({ kind: "business", id: b })));
    const seen = new Map<number, Metric>();
    for (const l of lists) for (const m of l) if (!seen.has(m.id)) seen.set(m.id, m);
    return Array.from(seen.values()).sort((a, b) => a.name.localeCompare(b.name));
  }, [businessIds.join(",")]);
  const metricOptions = (metrics.data ?? []).map((m) => ({ value: String(m.id), label: m.name, hint: `${m.key} · ${m.direction === "increase" ? "↑ better" : m.direction === "decrease" ? "↓ better" : "no direction"}` }));
  const objective = metrics.data?.find((m) => m.id === objectiveId);
  const effDirection = direction || (objective?.direction === "neutral" ? "" : objective?.direction ?? "");

  const space: TuningParam[] | null = useMemo(() => {
    const out: TuningParam[] = [];
    for (const p of params) {
      const min = Number(p.min);
      const max = Number(p.max);
      const control = Number(p.control);
      if (!p.path.trim() || !isFinite(min) || !isFinite(max) || !(min < max) || !isFinite(control)) return null;
      out.push({ path: p.path.trim(), type: p.type, min, max, control, scale: p.scale, step: p.step ? Number(p.step) : 0 });
    }
    return out;
  }, [params]);
  const previewKey = useDebounced(JSON.stringify([space, algorithm, arms]), 400);
  const preview = useAsync(async () => (space ? api.previewTuning({ params: space, algorithm, arms: Number(arms) || 10 }).catch(() => null) : null), [previewKey]);

  const parseBase = (): Record<string, unknown> | null => {
    try {
      const v = JSON.parse(baseText || "{}");
      return v && typeof v === "object" && !Array.isArray(v) ? v : null;
    } catch {
      return null;
    }
  };

  const check = () => {
    setError("");
    if (!name.trim()) return setError("Give the study a name.");
    if (!businessIds.length) return setError("Choose the platform and at least one business.");
    if (!locked && mode === "auto" && !autoDiversion) return setError("Choose what to split traffic by.");
    if (!locked && mode === "layer" && !layerId) return setError("Choose a traffic layer.");
    if (!space) return setError("Every parameter needs a path, and min < max with a numeric v0 value.");
    if (!parseBase()) return setError("The shared parameters must be a JSON object.");
    if (!objectiveId) return setError("Choose the objective metric.");
    if (!effDirection) return setError(`${objective?.name ?? "The objective"} has no preferred direction: choose maximise or minimise.`);
    if (algorithm === "constrained" && guards.length === 0) return setError("Constrained search needs at least one guardrail.");
    if (guards.some((g) => !g.metric_id)) return setError("Choose a metric for every guardrail (or remove it).");
    setConfirming(true);
  };

  const input = (): TuningInput => ({
    business_ids: businessIds,
    layer_id: mode === "layer" ? layerId : 0,
    auto_diversion: mode === "auto" && !editing ? autoDiversion : undefined,
    name,
    hypothesis,
    description,
    traffic_target: traffic,
    targeting,
    metric_group_ids: [],
    tuning: {
      algorithm,
      params: space ?? [],
      base_params: parseBase() ?? {},
      objective_metric_id: objectiveId,
      objective_direction: effDirection as "increase" | "decrease",
      guardrails: guards.map((g) => ({ metric_id: g.metric_id, max_drop: (Number(g.drop) || 0) / 100 })),
      arms: Number(arms) || 10,
      round_days: Number(roundDays) || 1,
      max_rounds: Number(maxRounds) || 10,
      min_units: Number(minUnits) || 0,
      keep_best: keepBest,
    },
  });

  const save = async () => {
    setConfirming(false);
    setSaving(true);
    try {
      const t = editing ? await api.updateTuning(Number(id), input()) : await api.createTuning(input());
      nav(`/tuning/${t.experiment.id}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  if (refs.loading || (editing && existing.loading)) return <div className="page"><Loading /></div>;
  const layer = refs.data?.layers.find((l) => l.id === layerId);
  const layerFree = mode === "auto" ? 1000 : layer ? 1000 - layer.used_buckets : 1000;

  return (
    <div className="page" style={{ maxWidth: 1080 }}>
      <div className="crumbs">
        <Link to="/tuning">AB Tuning</Link> / {editing ? "Edit" : "New study"}
      </div>
      <div className="page-head">
        <div>
          <h1>{editing ? "Edit tuning study" : "New tuning study"}</h1>
          <p>
            Tune numeric parameters inside bounds. v0 keeps today's values; each round, every treatment arm gets a candidate point, and the algorithm
            uses all earlier rounds to choose the next ones.
          </p>
        </div>
      </div>
      {locked && <div className="alert alert-warn small">This study has started: it can't be edited. Stop it and create a new one to search again.</div>}
      <div className="stack">
        <section className="card card-pad stack">
          <h2>Basics</h2>
          <Field label="Name">
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Ranking weights auto-tune" />
          </Field>
          <Field label="Hypothesis">
            <textarea className="input" rows={2} value={hypothesis} onChange={(e) => setHypothesis(e.target.value)} placeholder="Some mix of relevance and freshness lifts CTR" />
          </Field>
          <Field label="Notes">
            <textarea className="input" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <div className="grid-3">
            <Field label="Platform">
              {editing ? (
                <input className="input" disabled value={platformName} />
              ) : (
                <Combobox
                  options={platformOptions}
                  value={platformKey}
                  onChange={(v) => {
                    setPlatformPick(v);
                    if (chosen[0]?.platform_key !== v) setBusinessIds([]);
                  }}
                  placeholder="Choose a platform"
                />
              )}
            </Field>
            <Field group label="Businesses" hint="Their metrics are available as objective and guardrails.">
              {editing ? (
                <input className="input" disabled value={chosen.map((b) => b.name).join(", ")} />
              ) : (
                <div className="stack" style={{ gap: 6 }}>
                  {chosen.length > 0 && (
                    <div className="chip-row">
                      {chosen.map((b) => (
                        <span key={b.id} className="chip">
                          {b.name}
                          <button type="button" className="chip-x" aria-label={`Remove ${b.name}`} onClick={() => setBusinessIds(businessIds.filter((x) => x !== b.id))}>
                            ×
                          </button>
                        </span>
                      ))}
                    </div>
                  )}
                  <Combobox options={businessOptions} value="" onChange={(v) => v && setBusinessIds([...businessIds, Number(v)])} placeholder={platformKey ? "Add a business" : "Choose a platform first"} />
                </div>
              )}
            </Field>
            <Field label="Owner">
              <input className="input" disabled value={editing ? existing.data?.experiment.owner_name ?? "" : `${user?.display_name ?? ""} (you)`} />
            </Field>
          </div>
        </section>

        <section className="card card-pad stack">
          <div className="row-between">
            <div>
              <h2>Search space</h2>
              <p className="small muted">
                Paths are inside the platform's params ({platformKey ? <code>{`{"${platformKey}": {…}}`}</code> : "choose a platform"}). v0 serves the v0 value;
                arms serve values between min and max.
              </p>
            </div>
            <button className="btn btn-sm" disabled={params.length >= 10} onClick={() => setParams([...params, blankParam()])}>
              <Icon name="plus" /> Parameter
            </button>
          </div>
          <div className="param-grid">
            <span className="hdr">Path</span>
            <span className="hdr">Type</span>
            <span className="hdr">Min</span>
            <span className="hdr">Max</span>
            <span className="hdr">v0 value</span>
            <span className="hdr">Scale</span>
            <span className="hdr">Step</span>
            <span />
            {params.map((p, i) => {
              const set = (patch: Partial<ParamDraft>) => setParams(params.map((x, j) => (j === i ? { ...x, ...patch } : x)));
              return (
                <ParamRow key={i} p={p} set={set} onRemove={params.length > 1 ? () => setParams(params.filter((_, j) => j !== i)) : undefined} />
              );
            })}
          </div>
          <Field label="Shared parameters (optional)" hint="Served unchanged by v0 and every arm, next to the tuned values.">
            <NamespacedJson platformKey={platformKey} platformName={platformName}>
              <JsonEditor value={baseText} onChange={setBaseText} rows={4} invalid={!parseBase()} />
            </NamespacedJson>
          </Field>
        </section>

        <section className="card card-pad stack">
          <h2>Objective and guardrails</h2>
          <div className="grid-2">
            <Field label="Objective metric" hint="Each arm's lift on it versus v0 drives the search.">
              <Combobox options={metricOptions} value={objectiveId ? String(objectiveId) : ""} onChange={(v) => setObjectiveId(Number(v))} placeholder={businessIds.length ? "Choose a metric" : "Choose a business first"} />
            </Field>
            <Field label="Goal">
              <select className="input" value={direction} onChange={(e) => setDirection(e.target.value as "" | "increase" | "decrease")}>
                <option value="">{objective && objective.direction !== "neutral" ? `As the metric says (${objective.direction === "increase" ? "maximise" : "minimise"})` : "Choose…"}</option>
                <option value="increase">Maximise</option>
                <option value="decrease">Minimise</option>
              </select>
            </Field>
          </div>
          <div className="stack-sm">
            <div className="row-between">
              <b className="small">Guardrails</b>
              <button className="btn btn-sm" disabled={guards.length >= 10} onClick={() => setGuards([...guards, { metric_id: 0, drop: "2" }])}>
                <Icon name="plus" /> Guardrail
              </button>
            </div>
            {guards.length === 0 && <div className="small faint">None. Guardrails are metrics that must not get worse than an allowed drop — the recommendation always respects them; constrained search also avoids breaking them while searching.</div>}
            {guards.map((g, i) => (
              <div key={i} className="row" style={{ gap: 8 }}>
                <div style={{ flex: 1 }}>
                  <Combobox options={metricOptions.filter((o) => Number(o.value) !== objectiveId)} value={g.metric_id ? String(g.metric_id) : ""} onChange={(v) => setGuards(guards.map((x, j) => (j === i ? { ...x, metric_id: Number(v) } : x)))} placeholder="Metric" />
                </div>
                <span className="small faint">at most</span>
                <input className="input" style={{ width: 80 }} type="number" min={0} max={100} step={0.5} value={g.drop} onChange={(e) => setGuards(guards.map((x, j) => (j === i ? { ...x, drop: e.target.value } : x)))} />
                <span className="small faint">% worse than v0</span>
                <button className="icon-btn" aria-label="Remove guardrail" onClick={() => setGuards(guards.filter((_, j) => j !== i))}>
                  <Icon name="trash" size={15} />
                </button>
              </div>
            ))}
          </div>
        </section>

        <section className="card card-pad stack">
          <h2>Algorithm and rounds</h2>
          <div className="algo-cards">
            {algorithms.map((a) => (
              <button key={a.key} type="button" className={`algo-card ${algorithm === a.key ? "on" : ""}`} onClick={() => setAlgorithm(a.key)}>
                <b>{a.name}</b>
                <span className="small muted">{a.short}</span>
              </button>
            ))}
          </div>
          <p className="small muted" style={{ margin: 0 }}>
            {algorithms.find((a) => a.key === algorithm)?.long}
          </p>
          <div className="grid-3">
            <Field label="Treatment arms per round" hint={`Plus v0. Each arm gets ${trafficPct(Math.floor(1000 / ((Number(arms) || 10) + 1)))} of the study's traffic.`}>
              <input className="input" type="number" min={1} max={20} value={arms} onChange={(e) => setArms(e.target.value)} />
            </Field>
            <Field label="Round length (days)" hint="Rounds end at 00:00 UTC.">
              <input className="input" type="number" min={1} max={30} value={roundDays} onChange={(e) => setRoundDays(e.target.value)} />
            </Field>
            <Field label="Rounds" hint="The study stops after the last; launch the recommendation then.">
              <input className="input" type="number" min={1} max={100} value={maxRounds} onChange={(e) => setMaxRounds(e.target.value)} />
            </Field>
            <Field label="Minimum units per arm" hint="A round runs another day until every arm has this many (0 = no minimum).">
              <input className="input" type="number" min={0} value={minUnits} onChange={(e) => setMinUnits(e.target.value)} />
            </Field>
            <Field group label="Re-test the best">
              <label className="check" style={{ height: 34 }}>
                <input type="checkbox" checked={keepBest} onChange={(e) => setKeepBest(e.target.checked)} /> One arm per round re-tests the best point so far
              </label>
            </Field>
          </div>
          <PreviewPlot params={space} candidates={preview.data?.candidates ?? []} note={preview.data?.note} />
        </section>

        <section className="card card-pad stack">
          <h2>Traffic</h2>
          {!editing && (
            <Segmented<"auto" | "layer">
              options={[
                ["auto", "Dedicated (auto)"],
                ["layer", "Shared layer"],
              ]}
              value={mode}
              onChange={setMode}
            />
          )}
          <div className="grid-2">
            {mode === "auto" ? (
              <Field label="Split traffic by" hint="The study gets a layer of its own.">
                <select className="input" disabled={editing} value={autoDiversion} onChange={(e) => setAutoDiversion(e.target.value)}>
                  <option value="" disabled>
                    Choose a diversion…
                  </option>
                  {diversions.map((d) => (
                    <option key={d.key} value={d.key}>
                      {d.name} ({d.key})
                    </option>
                  ))}
                </select>
              </Field>
            ) : (
              <Field label="Layer" hint={layer ? `Splits by ${diversionName(diversions, layer.diversion).toLowerCase()}.` : undefined}>
                <select className="input" disabled={editing} value={layerId} onChange={(e) => setLayerId(Number(e.target.value))}>
                  <option value={0} disabled>
                    Choose a layer…
                  </option>
                  {refs.data?.layers.map((l) => (
                    <option key={l.id} value={l.id}>
                      {l.name} — {trafficPct(1000 - l.used_buckets)} free
                    </option>
                  ))}
                </select>
              </Field>
            )}
            <Field group label={`Traffic: ${trafficPct(traffic)}`}>
              <input type="range" min={0} max={layerFree} step={5} value={Math.min(traffic, layerFree)} onChange={(e) => setTraffic(Number(e.target.value))} />
              <TrafficBar mine={Math.min(traffic, layerFree)} free={layerFree} />
            </Field>
          </div>
          <TargetingEditor value={targeting} onChange={setTargeting} attributes={refs.data?.attributes ?? []} />
        </section>

        <ErrorBox error={error} />
        <div className="row">
          <button className="btn btn-primary" disabled={saving || locked} onClick={check}>
            {saving ? "Saving…" : editing ? "Save changes" : "Create draft"}
          </button>
          <Link className="btn btn-ghost" to={editing ? `/tuning/${id}` : "/tuning"}>
            Cancel
          </Link>
        </div>
      </div>
      {confirming && (
        <Modal
          wide
          title={editing ? "Save this study?" : "Create this tuning study?"}
          onClose={() => setConfirming(false)}
          footer={
            <>
              <button className="btn btn-ghost" onClick={() => setConfirming(false)}>
                Back to editing
              </button>
              <button className="btn btn-primary" onClick={save} autoFocus>
                {editing ? "Save changes" : "Create draft"}
              </button>
            </>
          }
        >
          <dl className="confirm-list">
            <dt>Name</dt>
            <dd>{name}</dd>
            <dt>Businesses</dt>
            <dd>
              {platformName} · {chosen.map((b) => b.name).join(", ")}
            </dd>
            <dt>Parameters</dt>
            <dd>
              {(space ?? []).map((p) => (
                <div key={p.path}>
                  <span className="mono">{p.path}</span> in [{p.min}, {p.max}] · v0 {p.control}
                </div>
              ))}
            </dd>
            <dt>Objective</dt>
            <dd>
              {effDirection === "increase" ? "Maximise" : "Minimise"} {objective?.name}
            </dd>
            <dt>Guardrails</dt>
            <dd>{guards.length ? guards.map((g) => `${metrics.data?.find((m) => m.id === g.metric_id)?.name} ≤ ${g.drop}% worse`).join(" · ") : "none"}</dd>
            <dt>Search</dt>
            <dd>
              {algorithms.find((a) => a.key === algorithm)?.name} · v0 + {arms} arms · {maxRounds} rounds × {roundDays} day(s)
            </dd>
            <dt>Traffic</dt>
            <dd>
              {trafficPct(traffic)} · {mode === "auto" ? `dedicated, split by ${diversionName(diversions, autoDiversion).toLowerCase()}` : `layer ${layer?.name ?? ""}`}
            </dd>
          </dl>
          <p className="small faint">A draft serves nothing. Submit it for review, then start it; round 1 starts with it.</p>
        </Modal>
      )}
    </div>
  );
}

function ParamRow({ p, set, onRemove }: { p: ParamDraft; set: (x: Partial<ParamDraft>) => void; onRemove?: () => void }) {
  return (
    <>
      <input className="input input-mono" value={p.path} onChange={(e) => set({ path: e.target.value })} placeholder="search.ranking.relevance_weight" aria-label="Path" />
      <select className="input" value={p.type} onChange={(e) => set({ type: e.target.value as "float" | "int" })} aria-label="Type">
        <option value="float">float</option>
        <option value="int">int</option>
      </select>
      <input className="input" type="number" value={p.min} onChange={(e) => set({ min: e.target.value })} aria-label="Min" />
      <input className="input" type="number" value={p.max} onChange={(e) => set({ max: e.target.value })} aria-label="Max" />
      <input className="input" type="number" value={p.control} onChange={(e) => set({ control: e.target.value })} aria-label="v0 value" />
      <select className="input" value={p.scale} onChange={(e) => set({ scale: e.target.value as "linear" | "log" })} aria-label="Scale">
        <option value="linear">linear</option>
        <option value="log">log</option>
      </select>
      <input className="input" type="number" min={0} value={p.step} onChange={(e) => set({ step: e.target.value })} placeholder={p.type === "int" ? "1" : "any"} aria-label="Step" />
      {onRemove ? (
        <button className="icon-btn" aria-label="Remove parameter" onClick={onRemove}>
          <Icon name="trash" size={15} />
        </button>
      ) : (
        <span />
      )}
    </>
  );
}

// PreviewPlot shows where round 1's arms will land.
function PreviewPlot({ params, candidates, note }: { params: TuningParam[] | null; candidates: { values: number[] }[]; note?: string }) {
  if (!params || candidates.length === 0) return null;
  const W = 320;
  const H = params.length > 1 ? 240 : 70;
  const pad = 28;
  const [a, b] = params;
  const ux = (v: number, p: TuningParam) => (p.scale === "log" ? Math.log(v / p.min) / Math.log(p.max / p.min) : (v - p.min) / (p.max - p.min));
  const X = (v: number) => pad + ux(v, a) * (W - 2 * pad);
  const Y = (v: number) => (b ? H - pad - ux(v, b) * (H - 2 * pad) : H / 2);
  return (
    <div className="row" style={{ gap: 20, alignItems: "flex-start", flexWrap: "wrap" }}>
      <svg width={W} height={H} role="img" aria-label="Round 1 candidates">
        <rect x={pad} y={b ? pad : H / 2 - 12} width={W - 2 * pad} height={b ? H - 2 * pad : 24} className="chart-plot" />
        {candidates.map((c, i) => (
          <circle key={i} cx={X(c.values[0])} cy={Y(c.values[1])} r={5} fill="var(--accent)" stroke="var(--surface)" strokeWidth={1.5} />
        ))}
        <g stroke="var(--text)" strokeWidth={2.5}>
          <line x1={X(a.control) - 5} x2={X(a.control) + 5} y1={Y(b?.control ?? 0) - 5} y2={Y(b?.control ?? 0) + 5} />
          <line x1={X(a.control) - 5} x2={X(a.control) + 5} y1={Y(b?.control ?? 0) + 5} y2={Y(b?.control ?? 0) - 5} />
        </g>
        <text x={W / 2} y={H - 6} textAnchor="middle" className="chart-axis chart-axis-title">
          {a.path}
        </text>
        {b && (
          <text x={10} y={H / 2} textAnchor="middle" className="chart-axis chart-axis-title" transform={`rotate(-90 10 ${H / 2})`}>
            {b.path}
          </text>
        )}
      </svg>
      <div className="small muted" style={{ maxWidth: 420 }}>
        <b>Round 1 preview.</b> {note} The dots are where the {candidates.length} arms start (✕ is v0)
        {params.length > 2 ? `; showing the first two of ${params.length} parameters` : ""}. Later rounds follow the results.
      </div>
    </div>
  );
}
