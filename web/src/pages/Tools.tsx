import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Combobox } from "../components/Combobox";
import { ErrorBox, Field, Icon, Loading, StatusBadge, Tabs } from "../components/ui";
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
  other_platform: ["Other platform", ""],
  not_in_rollout: ["Not in launch rollout yet", ""],
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
  // Only the ids you add: pick a diversion, type its value.
  const [rows, setRows] = useState<{ key: string; value: string }[]>([{ key: "user_id", value: "" }]);
  const ids = Object.fromEntries(rows.filter((r) => r.key && r.value.trim()).map((r) => [r.key, r.value.trim()]));
  const [platform, setPlatform] = useState("");
  const [business, setBusiness] = useState("");
  const [attrs, setAttrs] = useState('{\n  "region": "ID",\n  "os": "android"\n}');
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<{ version: number; hits: Hit[]; params: Record<string, unknown>; trace: Step[] } | null>(null);
  const platforms = useAsync(() => api.platforms(), []);
  const plat = platforms.data?.find((p) => p.key === platform);
  // With one platform there's nothing to choose.
  useEffect(() => {
    if (!platform && platforms.data?.length === 1) setPlatform(platforms.data[0].key);
  }, [platforms.data, platform]);
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
      const r = await api.diagnose(ids, platform, business, parsed);
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
        <div className="stack-sm">
          <b className="small">Unit ids</b>
          {rows.map((r, i) => {
            const taken = new Set(rows.filter((_, j) => j !== i).map((x) => x.key));
            const d = diversions.find((x) => x.key === r.key);
            return (
              <div key={i} className="row" style={{ gap: 6, alignItems: "flex-start" }}>
                <div style={{ width: 150 }}>
                  <Combobox
                    options={diversions.filter((x) => !taken.has(x.key)).map((x) => ({ value: x.key, label: x.name, hint: x.key }))}
                    value={r.key}
                    onChange={(v) => setRows(rows.map((x, j) => (j === i ? { ...x, key: v } : x)))}
                    placeholder="Diversion"
                  />
                </div>
                <input
                  className="input input-mono"
                  style={{ flex: 1, minWidth: 0 }}
                  value={r.value}
                  onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                  placeholder={r.key === "user_id" ? "demo-search-000042" : r.key === "device_id" ? "dev-search-000042" : d ? `${d.name} value` : "value"}
                  aria-label={`${d?.name ?? "id"} value`}
                />
                <button className="icon-btn" aria-label="Remove" onClick={() => setRows(rows.filter((_, j) => j !== i))} disabled={rows.length === 1}>
                  <Icon name="x" size={14} />
                </button>
              </div>
            );
          })}
          <div>
            <button
              className="btn btn-sm"
              disabled={rows.length >= diversions.length}
              onClick={() => {
                const used = new Set(rows.map((r) => r.key));
                setRows([...rows, { key: diversions.find((d) => !used.has(d.key))?.key ?? "", value: "" }]);
              }}
            >
              <Icon name="plus" /> Add id
            </button>
          </div>
          <div className="small faint">Each layer splits by one diversion; experiments whose id you don't send are skipped (the trace says so).</div>
        </div>
        <div className="grid-2">
          <Field label="Platform" hint="What the service sends as platform.">
            <select
              className="input"
              value={platform}
              onChange={(e) => {
                setPlatform(e.target.value);
                setBusiness("");
              }}
            >
              <option value="">All (debug only)</option>
              {platforms.data?.map((p) => (
                <option key={p.id} value={p.key}>
                  {p.name} ({p.key})
                </option>
              ))}
            </select>
          </Field>
          <Field label="Business" hint="Optional: only its experiments.">
            <select className="input" value={business} onChange={(e) => setBusiness(e.target.value)} disabled={!plat}>
              <option value="">All of the platform</option>
              {plat?.businesses.map((b) => (
                <option key={b.id} value={b.key}>
                  {b.name} ({b.key})
                </option>
              ))}
            </select>
          </Field>
        </div>
        {!platform && (platforms.data?.length ?? 0) > 1 && (
          <div className="small faint">The runtime API needs a platform when there are several; here "All" shows every platform's experiments for debugging.</div>
        )}
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
                            <StatusBadge status={s.status} /> {s.platform} › {s.business}
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
                <Link to={`/experiments/${h.experiment_id}`}>{h.experiment}</Link> <span className="faint small">
                  {h.platform} › {h.business}
                </span>
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
