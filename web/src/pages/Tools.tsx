import { useState } from "react";
import { Link } from "react-router-dom";
import { ErrorBox, Field, Loading, StatusBadge, Tabs } from "../components/ui";
import { api } from "../lib/api";
import { useDiversions } from "../lib/diversions";
import { useAsync, useDebounced } from "../lib/hooks";
import type { Hit, Step } from "../lib/types";

const outcomeText: Record<string, [string, string]> = {
  assigned: ["In experiment", "b-good"],
  whitelisted: ["Whitelisted", "b-accent"],
  launched: ["Launched config", "b-accent"],
  not_running: ["Not running", ""],
  not_in_traffic: ["Not in traffic", ""],
  targeting_failed: ["Targeting excluded", "b-warn"],
  missing_id: ["Missing id", "b-warn"],
  other_business: ["Other business", ""],
};

export default function Tools() {
  const [tab, setTab] = useState<"diagnose" | "params">("diagnose");
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Debug tools</h1>
          <p>Answer “why did this user (not) get my experiment?” and “who else sets this parameter?”.</p>
        </div>
      </div>
      <Tabs
        tabs={[
          ["diagnose", "Hit diagnosis"],
          ["params", "Parameter search"],
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === "diagnose" ? <Diagnose /> : <Params />}
    </div>
  );
}

function Diagnose() {
  const diversions = useDiversions();
  const [ids, setIds] = useState<Record<string, string>>({});
  const [business, setBusiness] = useState("");
  const [attrs, setAttrs] = useState('{\n  "region": "ID",\n  "os": "android"\n}');
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<{ version: number; hits: Hit[]; params: Record<string, unknown>; trace: Step[] } | null>(null);
  const businesses = useAsync(() => api.businesses(), []);
  const run = async () => {
    setError("");
    let parsed: Record<string, unknown> = {};
    try {
      parsed = attrs.trim() ? JSON.parse(attrs) : {};
    } catch {
      setError("Attributes must be a JSON object.");
      return;
    }
    setBusy(true);
    try {
      const r = await api.diagnose(ids, business, parsed);
      setRes({ version: r.snapshot_version, ...r.result, trace: r.result.trace ?? [] });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="grid-2" style={{ gridTemplateColumns: "minmax(0, 360px) minmax(0, 1fr)", alignItems: "start" }}>
      <section className="card card-pad stack">
        {diversions.map((d) => (
          <Field key={d.key} label={d.name} hint={`Used by layers that split by ${d.key}.`}>
            <input
              className="input input-mono"
              value={ids[d.key] ?? ""}
              onChange={(e) => setIds({ ...ids, [d.key]: e.target.value })}
              placeholder={d.key === "user_id" ? "demo-search-000042" : d.key === "device_id" ? "dev-search-000042" : d.key}
            />
          </Field>
        ))}
        <Field label="Business">
          <select className="input" value={business} onChange={(e) => setBusiness(e.target.value)}>
            <option value="">All</option>
            {businesses.data?.map((b) => (
              <option key={b.id} value={b.key}>
                {b.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Request attributes (JSON)" hint="Targeting rules are checked against these.">
          <textarea className="input input-mono" rows={6} value={attrs} onChange={(e) => setAttrs(e.target.value)} />
        </Field>
        <ErrorBox error={error} />
        <div>
          <button className="btn btn-primary" disabled={!Object.values(ids).some((v) => v.trim()) || busy} onClick={run}>
            Diagnose
          </button>
        </div>
        <p className="faint small">Nothing is logged: this doesn't create exposures.</p>
      </section>
      <div className="stack">
        {busy && <Loading />}
        {res && (
          <>
            <section className="card">
              <div className="card-head">
                <h2>Decision per experiment</h2>
                <span className="faint small">config version {res.version}</span>
              </div>
              <table className="tbl">
                <tbody>
                  {res.trace.map((s) => {
                    const [label, cls] = outcomeText[s.outcome] ?? [s.outcome, ""];
                    return (
                      <tr key={s.experiment_id}>
                        <td>
                          <Link to={`/experiments/${s.experiment_id}`}>{s.experiment}</Link>
                          <div className="row small faint" style={{ gap: 6 }}>
                            <StatusBadge status={s.status} /> {s.business}
                          </div>
                        </td>
                        <td>
                          <span className={`badge ${cls}`}>{label}</span>
                          <div className="small muted" style={{ marginTop: 4 }}>
                            {s.detail}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </section>
            <section className="card card-pad stack-sm">
              <h2>Parameters served</h2>
              <pre className="code">{JSON.stringify(res.params, null, 2)}</pre>
            </section>
          </>
        )}
      </div>
    </div>
  );
}

function Params() {
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 250);
  const res = useAsync(() => api.paramSearch(dq), [dq]);
  return (
    <section className="card">
      <div className="card-head">
        <input className="input input-mono" style={{ maxWidth: 360 }} placeholder="search.ranking" value={q} onChange={(e) => setQ(e.target.value)} />
        <span className="faint small">Across experiments that can still serve (drafts with test users, running, launched).</span>
      </div>
      <table className="tbl tbl-compact">
        <thead>
          <tr>
            <th>Parameter</th>
            <th>Experiment</th>
            <th>Variant</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          {res.data?.map((h, i) => (
            <tr key={i}>
              <td className="mono">{h.path}</td>
              <td>
                <Link to={`/experiments/${h.experiment_id}`}>{h.experiment}</Link> <span className="faint small">{h.business}</span>
              </td>
              <td className="mono">{h.variant}</td>
              <td>
                <StatusBadge status={h.status as Step["status"]} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
