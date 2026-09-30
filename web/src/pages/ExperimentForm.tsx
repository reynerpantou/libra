import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { GroupPicker } from "../components/GroupPicker";
import { JsonEditor } from "../components/JsonEditor";
import { ErrorBox, Field, Icon, Loading, Segmented, TrafficBar } from "../components/ui";
import { diversionName, useDiversions } from "../lib/diversions";
import { api, type ExperimentInput } from "../lib/api";
import { useAuth } from "../lib/auth";
import { trafficPct } from "../lib/format";
import { useAsync } from "../lib/hooks";
import type { Targeting, Variant } from "../lib/types";
import { TargetingEditor } from "../components/Targeting";


interface VariantDraft extends Omit<Variant, "params"> {
  paramsText: string;
}

const blankVariants = (): VariantDraft[] => [
  { key: "control", name: "Control", is_control: true, weight: 500, paramsText: "{\n  \n}" },
  { key: "treatment", name: "Treatment", is_control: false, weight: 500, paramsText: "{\n  \n}" },
];

export default function ExperimentForm() {
  const { id } = useParams();
  const editing = id !== undefined;
  const nav = useNavigate();
  const { user } = useAuth();
  const refs = useAsync(async () => {
    const [businesses, layers, users, attributes, groups, metrics] = await Promise.all([
      api.businesses(),
      api.layers(),
      api.users(),
      api.attributes(),
      api.allGroups(),
      api.allMetrics(),
    ]);
    return { businesses, layers, users, attributes, groups, metrics };
  }, []);
  const existing = useAsync(async () => (editing ? api.experiment(Number(id)) : null), [id]);

  const [businessId, setBusinessId] = useState(0);
  const [layerId, setLayerId] = useState(0);
  const [name, setName] = useState("");
  const [hypothesis, setHypothesis] = useState("");
  const [description, setDescription] = useState("");
  const [ownerId, setOwnerId] = useState<number | null>(null);
  const [traffic, setTraffic] = useState(100);
  const [targeting, setTargeting] = useState<Targeting>({ groups: [] });
  const [groupIds, setGroupIds] = useState<number[]>([]);
  const [mode, setMode] = useState<"layer" | "auto">("layer");
  const [autoDiversion, setAutoDiversion] = useState("user_id");
  const diversions = useDiversions();
  const [variants, setVariants] = useState<VariantDraft[]>(blankVariants());
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    const e = existing.data;
    if (!e) return;
    setBusinessId(e.business_id);
    setMode(e.layer_auto ? "auto" : "layer");
    if (e.layer_auto) setAutoDiversion(e.layer_diversion);
    else setLayerId(e.layer_id);
    setName(e.name);
    setHypothesis(e.hypothesis);
    setDescription(e.description);
    setOwnerId(e.owner_id);
    setTraffic(e.traffic_target);
    setTargeting(e.targeting);
    setGroupIds(e.metric_group_ids ?? []);
    setVariants(
      (e.variants ?? []).map((v) => ({ ...v, paramsText: JSON.stringify(v.params ?? {}, null, 2) }))
    );
  }, [existing.data]);

  useEffect(() => {
    if (editing || !refs.data) return;
    if (!businessId && refs.data.businesses[0]) setBusinessId(refs.data.businesses[0].id);
    if (!layerId && refs.data.layers[0]) setLayerId(refs.data.layers[0].id);
    if (ownerId === null && user) setOwnerId(user.id);
  }, [refs.data, editing, businessId, layerId, ownerId, user]);

  const locked = existing.data?.status === "active" || existing.data?.status === "paused";
  const total = variants.reduce((s, v) => s + (Number(v.weight) || 0), 0);
  const layer = refs.data?.layers.find((l) => l.id === layerId);
  const layerFree = mode === "auto" ? 1000 : layer ? 1000 - layer.used_buckets + (existing.data?.layer_id === layerId ? existing.data.traffic_held : 0) : 1000;
  const business = refs.data?.businesses.find((b) => b.id === businessId);
  // Don't let a draft plan more traffic than its layer has free.
  useEffect(() => {
    if (!locked && traffic > layerFree) setTraffic(layerFree);
  }, [layerFree, locked, traffic]);

  const paramErrors = useMemo(
    () =>
      variants.map((v) => {
        try {
          const p = JSON.parse(v.paramsText || "{}");
          return p && typeof p === "object" && !Array.isArray(p) ? "" : "must be a JSON object";
        } catch (e) {
          return "invalid JSON";
        }
      }),
    [variants]
  );

  const splitEvenly = () => {
    const n = variants.length;
    const base = Math.floor(1000 / n);
    setVariants(variants.map((v, i) => ({ ...v, weight: i === 0 ? 1000 - base * (n - 1) : base })));
  };

  const save = async () => {
    setError("");
    if (paramErrors.some(Boolean)) {
      setError("Fix the variant parameters (they must be JSON objects).");
      return;
    }
    const input: ExperimentInput = {
      business_id: businessId,
      layer_id: mode === "auto" ? (existing.data?.layer_auto ? existing.data.layer_id : 0) : layerId,
      auto_diversion: mode === "auto" ? autoDiversion : undefined,
      name,
      hypothesis,
      description,
      owner_id: ownerId,
      traffic_target: traffic,
      targeting,
      metric_group_ids: groupIds,
      variants: variants.map(({ paramsText, ...v }) => ({ ...v, weight: Number(v.weight), params: JSON.parse(paramsText || "{}") })),
    };
    setSaving(true);
    try {
      const e = editing ? await api.updateExperiment(Number(id), input) : await api.createExperiment(input);
      nav(`/experiments/${e.id}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  if (refs.loading || existing.loading) return <div className="page"><Loading /></div>;
  if (refs.data && refs.data.businesses.length === 0) {
    return (
      <div className="page">
        <h1>New experiment</h1>
        <div className="alert alert-warn" style={{ marginTop: 16 }}>
          An experiment needs a <Link to="/businesses">business</Link> (whose metrics it's measured by). Create one first.
        </div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="crumbs">
        <Link to="/experiments">Experiments</Link> {editing && <>/ <Link to={`/experiments/${id}`}>#{id}</Link></>}
      </div>
      <div className="page-head">
        <div>
          <h1>{editing ? "Edit experiment" : "New experiment"}</h1>
          <p>
            {locked
              ? "This experiment is running: its business, layer, traffic and variants are locked so assignments stay stable. You can still change the description, targeting and metric group."
              : existing.data?.status === "approved"
              ? "Saving changes sends the experiment back to draft for another review."
              : "Experiments start as drafts. Submit for review when ready, then start."}
          </p>
        </div>
      </div>

      <div className="stack">
        <section className="card card-pad stack">
          <h2>Basics</h2>
          <Field label="Name">
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Ranking formula v2" />
          </Field>
          <Field label="Hypothesis" hint="What you expect to change, and why. Reviewers read this first.">
            <textarea className="input" rows={2} value={hypothesis} onChange={(e) => setHypothesis(e.target.value)} />
          </Field>
          <Field label="Notes">
            <textarea className="input" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <div className="grid-2">
            <Field label="Business" hint="Where the experiment lives; its and its platform's default metric groups are always in the report.">
              <select className="input" disabled={locked} value={businessId} onChange={(e) => setBusinessId(Number(e.target.value))}>
                {refs.data?.businesses.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.platform_name} › {b.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Owner">
              <select className="input" value={ownerId ?? ""} onChange={(e) => setOwnerId(Number(e.target.value))}>
                {refs.data?.users.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.display_name}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        </section>

        <section className="card card-pad stack">
          <div>
            <h2>Metric groups</h2>
            <p className="faint small">
              Pick any number of groups — from this business or any other (e.g. Search › Back end with Recommendation › Product). The report shows one
              section per group. With none picked, the report shows all of the business's metrics plus the defaults.
            </p>
          </div>
          <GroupPicker
            groups={refs.data?.groups ?? []}
            metrics={refs.data?.metrics ?? []}
            value={groupIds}
            onChange={setGroupIds}
            businessId={businessId}
            platformId={business?.platform_id ?? 0}
          />
        </section>

        <section className="card card-pad stack">
          <div className="row-between">
            <h2>Traffic</h2>
            <Segmented<"layer" | "auto">
              options={[
                ["layer", "Shared layer"],
                ["auto", "Auto (dedicated)"],
              ]}
              value={mode}
              onChange={(m) => {
                if (locked) return;
                setMode(m);
                if (m === "auto" && mode !== "auto") setTraffic(1000);
              }}
            />
          </div>
          {mode === "auto" ? (
            <div className="grid-2">
              <Field
                label="Split traffic by"
                hint="The experiment gets a layer of its own — no other experiment shares its units, so up to 100% of traffic is available. Manage diversions on the Traffic layers page."
              >
                <select className="input" disabled={locked} value={autoDiversion} onChange={(e) => setAutoDiversion(e.target.value)}>
                  {diversions.map((d) => (
                    <option key={d.key} value={d.key}>
                      {d.name} ({d.key})
                    </option>
                  ))}
                </select>
              </Field>
              <Field
                label={`Traffic: ${trafficPct(traffic)}`}
                hint={locked ? "Change traffic from the experiment page (ramping keeps current units in)." : "Start small and ramp up from the experiment page, or go straight to 100%."}
              >
                <input type="range" min={0} max={1000} step={5} disabled={locked} value={traffic} onChange={(e) => setTraffic(Number(e.target.value))} />
                <TrafficBar mine={traffic} free={1000} />
                <div className="row" style={{ marginTop: 6 }}>
                  {[100, 500, 1000].map((v) => (
                    <button key={v} type="button" className="btn btn-sm" disabled={locked} onClick={() => setTraffic(v)}>
                      {trafficPct(v)}
                    </button>
                  ))}
                </div>
              </Field>
            </div>
          ) : (refs.data?.layers.length ?? 0) === 0 ? (
            <div className="alert alert-warn small">
              No shared layers yet. Use <b>Auto (dedicated)</b>, or create a <Link to="/layers">traffic layer</Link>.
            </div>
          ) : (
          <div className="grid-2">
            <Field
              label="Layer"
              hint={
                layer
                  ? `Splits traffic by ${diversionName(diversions, layer.diversion).toLowerCase()}. Experiments in the same layer never share a unit.`
                  : "Experiments in the same layer never share units."
              }
            >
              <select className="input" disabled={locked} value={layerId} onChange={(e) => setLayerId(Number(e.target.value))}>
                {refs.data?.layers.map((l) => (
                  <option key={l.id} value={l.id}>
                    {l.name} (by {diversionName(diversions, l.diversion).toLowerCase()}) — {trafficPct(1000 - l.used_buckets)} free
                  </option>
                ))}
              </select>
            </Field>
            <Field
              label={`Traffic: ${trafficPct(traffic)} of the layer`}
              hint={
                locked
                  ? "Change traffic from the experiment page (ramping keeps current units in)."
                  : layerFree === 0
                  ? "This layer is full. Pick another layer, or free traffic by stopping an experiment in it."
                  : `Up to ${trafficPct(layerFree)} — what this layer has free right now.`
              }
            >
              <input
                type="range"
                min={0}
                max={1000}
                step={5}
                disabled={locked || layerFree === 0}
                value={traffic}
                onChange={(e) => setTraffic(Math.min(Number(e.target.value), layerFree))}
              />
              <TrafficBar mine={traffic} free={layerFree} />
            </Field>
          </div>
          )}
        </section>

        <section className="card card-pad stack">
          <div>
            <h2>Targeting</h2>
            <p className="faint small">
              A unit is eligible when all AND conditions in at least one OR group match its request attributes. Attributes come from{" "}
              <Link to="/attributes">Targeting attributes</Link>.
            </p>
          </div>
          <TargetingEditor value={targeting} onChange={setTargeting} attributes={refs.data?.attributes ?? []} />
        </section>

        <section className="card card-pad stack">
          <div className="row-between">
            <div>
              <h2>Variants</h2>
              <p className="faint small">
                Parameters are the JSON config each variant serves; your service reads them from the resolve API. Weights must add up to 100%.
              </p>
            </div>
            {!locked && (
              <div className="row">
                <button className="btn btn-sm" onClick={splitEvenly}>
                  Split evenly
                </button>
                <button
                  className="btn btn-sm"
                  disabled={variants.length >= 20}
                  onClick={() => setVariants([...variants, { key: `variant_${variants.length}`, name: "", is_control: false, weight: 0, paramsText: "{\n  \n}" }])}
                >
                  <Icon name="plus" /> Add variant
                </button>
              </div>
            )}
          </div>
          <div className={total === 1000 ? "faint small" : "alert alert-warn small"}>Weights total {trafficPct(total)}</div>
          {variants.map((v, i) => (
            <div key={i} className="card" style={{ padding: 14, boxShadow: "none", background: "var(--surface-2)" }}>
              <div className="row" style={{ alignItems: "flex-end" }}>
                <Field label="Key">
                  <input className="input input-mono" style={{ width: 150 }} disabled={locked} value={v.key} onChange={(e) => setVariants(variants.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)))} />
                </Field>
                <div style={{ flex: 1, minWidth: 160 }}>
                  <Field label="Name">
                    <input className="input" disabled={locked} value={v.name} onChange={(e) => setVariants(variants.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} />
                  </Field>
                </div>
                <Field label="Weight %">
                  <input
                    className="input"
                    type="number"
                    style={{ width: 90 }}
                    min={0}
                    max={100}
                    step={0.1}
                    disabled={locked}
                    value={v.weight / 10}
                    onChange={(e) => setVariants(variants.map((x, j) => (j === i ? { ...x, weight: Math.round(Number(e.target.value) * 10) } : x)))}
                  />
                </Field>
                <label className="check" style={{ height: 34 }}>
                  <input type="radio" name="control" disabled={locked} checked={v.is_control} onChange={() => setVariants(variants.map((x, j) => ({ ...x, is_control: j === i })))} />
                  Control
                </label>
                {!locked && variants.length > 2 && (
                  <button className="icon-btn" aria-label="Remove variant" onClick={() => setVariants(variants.filter((_, j) => j !== i))}>
                    <Icon name="trash" size={16} />
                  </button>
                )}
              </div>
              <div style={{ marginTop: 10 }}>
                <Field
                  label="Parameters (JSON)"
                  hint={paramErrors[i] ? <span style={{ color: "var(--bad)" }}>{paramErrors[i]}</span> : "Tab / Shift+Tab indent. Esc, then Tab, moves to the next field."}
                >
                  <JsonEditor
                    disabled={locked}
                    invalid={!!paramErrors[i]}
                    value={v.paramsText}
                    onChange={(t) => setVariants(variants.map((x, j) => (j === i ? { ...x, paramsText: t } : x)))}
                  />
                </Field>
              </div>
            </div>
          ))}
        </section>

        <ErrorBox error={error} />
        <div className="row">
          <button className="btn btn-primary" disabled={saving} onClick={save}>
            {saving ? "Saving…" : editing ? "Save changes" : "Create draft"}
          </button>
          <Link className="btn btn-ghost" to={editing ? `/experiments/${id}` : "/experiments"}>
            Cancel
          </Link>
        </div>
      </div>
    </div>
  );
}
