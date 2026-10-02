import { useState } from "react";
import { Link } from "react-router-dom";
import { Combobox } from "../components/Combobox";
import { Pager, usePaged } from "../components/Pager";
import { copyText } from "../lib/exportTable";
import { ErrorBox, Field, Icon, Loading, StatusBadge, Tabs } from "../components/ui";
import { api, BASE, type ResolveBody } from "../lib/api";
import { JsonView } from "../components/JsonView";
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
  // Empty means "all", like the runtime API.
  // The scope: one row per platform, with all its businesses or a few.
  // No rows = "all" (every platform).
  const [scope, setScope] = useState<{ platform: string; businesses: string[] }[]>([]);
  const [attrs, setAttrs] = useState('{\n  "region": "ID",\n  "os": "android"\n}');
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<{ version: number; hits: Hit[]; params: Record<string, unknown>; trace: Step[] } | null>(null);
  const [sent, setSent] = useState<{ body: ResolveBody; response: Record<string, unknown> } | null>(null);
  const platforms = useAsync(() => api.platforms(), []);
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
      const body = resolveBody(ids, scope, parsed);
      const r = await api.diagnose(body);
      setRes({ version: r.snapshot_version, ...r.result, trace: r.result.trace ?? [] });
      setSent({ body, response: r.response });
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
        <Field
          group
          label="Scope"
          hint="Which platforms and businesses the request asks for — each business is chosen under its own platform, so equal keys never mix. No rows = every platform."
        >
          <div className="stack-sm">
            {scope.map((row, i) => {
              const plat = platforms.data?.find((p) => p.key === row.platform);
              const set = (patch: Partial<(typeof scope)[number]>) => setScope(scope.map((r, j) => (j === i ? { ...r, ...patch } : r)));
              return (
                <div key={row.platform} className="scope-row">
                  <div className="row-between">
                    <b className="small">
                      {plat?.name ?? row.platform} <span className="mono faint">{row.platform}</span>
                    </b>
                    <button className="icon-btn" aria-label={`Remove ${row.platform}`} onClick={() => setScope(scope.filter((_, j) => j !== i))}>
                      <Icon name="x" size={14} />
                    </button>
                  </div>
                  <MultiPick
                    options={(plat?.businesses ?? []).map((b) => ({ value: b.key, label: b.name, hint: b.key }))}
                    value={row.businesses}
                    onChange={(v) => set({ businesses: v })}
                    placeholder="All businesses"
                  />
                </div>
              );
            })}
            <Combobox
              options={(platforms.data ?? []).filter((p) => !scope.some((r) => r.platform === p.key)).map((p) => ({ value: p.key, label: p.name, hint: p.key }))}
              value=""
              onChange={(v) => v && setScope([...scope, { platform: v, businesses: [] }])}
              placeholder={scope.length ? "Add a platform" : "Every platform — or add one"}
            />
          </div>
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
        {res && <DiagnoseResult res={res} />}
        {sent && (
          <>
            <JsonView
              title={`Request to Libra · POST ${BASE}/api/v1/abtest/experiments`}
              value={sent.body}
              extra={{
                label: "curl",
                text: `curl -X POST ${window.location.origin}${BASE}/api/v1/abtest/experiments \\\n  -H "Authorization: Bearer $LIBRA_KEY" -H "Content-Type: application/json" \\\n  -d '${JSON.stringify(sent.body)}'`,
              }}
            />
            <JsonView title="Response from Libra" value={sent.response} />
          </>
        )}
      </div>
    </div>
  );
}

const APPLIED = new Set(["assigned", "whitelisted", "launched"]);

// DiagnoseResult: the merged parameters first (what the unit actually
// gets), then every experiment's decision — searchable, filterable by
// outcome and paged, so it stays usable with thousands of experiments.
function DiagnoseResult({ res }: { res: { version: number; hits: Hit[]; params: Record<string, unknown>; trace: Step[] } }) {
  const [q, setQ] = useState("");
  const [outcome, setOutcome] = useState<string>("applied");
  const [tall, setTall] = useState(false);
  const [copied, setCopied] = useState(false);
  const counts = new Map<string, number>();
  for (const s of res.trace) counts.set(s.outcome, (counts.get(s.outcome) ?? 0) + 1);
  const applied = res.trace.filter((s) => APPLIED.has(s.outcome)).length;
  const query = q.trim().toLowerCase();
  const found = res.trace
    .filter((s) => (outcome === "all" ? true : outcome === "applied" ? APPLIED.has(s.outcome) : outcome === "not_applied" ? !APPLIED.has(s.outcome) : s.outcome === outcome))
    .filter((s) => !query || [s.experiment, String(s.experiment_id), s.platform, s.business, s.detail].some((x) => x.toLowerCase().includes(query)))
    .sort((a, b) => Number(APPLIED.has(b.outcome)) - Number(APPLIED.has(a.outcome)));
  const paged = usePaged(found, 20, `${query}|${outcome}`);
  const json = JSON.stringify(res.params, null, 2);
  return (
    <>
      <section className="card">
        <div className="card-head">
          <div>
            <h2>Parameters served</h2>
            <p>
              Merged from {applied} experiment{applied === 1 ? "" : "s"}: {counts.get("assigned") ?? 0} in traffic, {counts.get("whitelisted") ?? 0} whitelisted,{" "}
              {counts.get("launched") ?? 0} launched · config version {res.version}
            </p>
          </div>
          <div className="row" style={{ gap: 6 }}>
            <button
              className="btn btn-sm"
              onClick={async () => {
                await copyText(json);
                setCopied(true);
                setTimeout(() => setCopied(false), 1400);
              }}
            >
              {copied ? "Copied ✓" : "Copy JSON"}
            </button>
            <button className="btn btn-sm" onClick={() => setTall(!tall)}>
              {tall ? "Shrink" : "Expand"}
            </button>
          </div>
        </div>
        <div className="card-pad">
          <pre className="code" style={{ maxHeight: tall ? "none" : 320, overflow: "auto", margin: 0 }}>
            {json}
          </pre>
        </div>
      </section>
      <section className="card">
        <div className="card-head">
          <h2>Decision per experiment</h2>
          <span className="faint small">{res.trace.length} checked</span>
        </div>
        <div className="row" style={{ gap: 8, padding: "10px 16px" }}>
          <input className="input" style={{ flex: 1, minWidth: 180 }} placeholder="Search experiment, id, business or reason…" value={q} onChange={(e) => setQ(e.target.value)} />
          <select className="input" style={{ width: 230 }} value={outcome} onChange={(e) => setOutcome(e.target.value)} aria-label="Outcome">
            <option value="applied">Applied to this unit ({applied})</option>
            <option value="not_applied">Not applied ({res.trace.length - applied})</option>
            <option value="all">All ({res.trace.length})</option>
            {Array.from(counts.entries()).map(([k, n]) => (
              <option key={k} value={k}>
                {(outcomeText[k] ?? [k])[0]} ({n})
              </option>
            ))}
          </select>
        </div>
        <table className="tbl">
          <tbody>
            {paged.slice.map((s) => {
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
        {found.length === 0 && <div className="small faint" style={{ padding: 16 }}>No experiments match.</div>}
        <Pager page={paged.page} pages={paged.pages} total={paged.total} size={paged.size} onPage={paged.setPage} onSize={paged.setSize} noun="experiments" />
      </section>
    </>
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

// resolveBody builds what a service sends to /api/v1/abtest/experiments.
function resolveBody(ids: Record<string, string>, scope: { platform: string; businesses: string[] }[], attrs: Record<string, unknown>): ResolveBody {
  const other = Object.fromEntries(Object.entries(ids).filter(([k, v]) => k !== "user_id" && k !== "device_id" && v));
  return {
    ...(ids.user_id ? { user_id: ids.user_id } : {}),
    ...(ids.device_id ? { device_id: ids.device_id } : {}),
    ...(Object.keys(other).length ? { ids: other } : {}),
    scope: scope.length ? Object.fromEntries(scope.map((r) => [r.platform, r.businesses.length ? r.businesses : "all"])) : "all",
    ...(Object.keys(attrs).length ? { attrs } : {}),
  };
}

// MultiPick: chips for what's picked plus a searchable dropdown to add more.
function MultiPick({
  options,
  value,
  onChange,
  placeholder,
}: {
  options: { value: string; label: string; hint?: string }[];
  value: string[];
  onChange: (v: string[]) => void;
  placeholder: string;
}) {
  const label = (v: string) => options.find((o) => o.value === v)?.label ?? v;
  return (
    <div className="stack" style={{ gap: 6 }}>
      {value.length > 0 && (
        <div className="chip-row">
          {value.map((v) => (
            <span key={v} className="chip" title={v}>
              {label(v)}
              <button type="button" className="chip-x" aria-label={`Remove ${label(v)}`} onClick={() => onChange(value.filter((x) => x !== v))}>
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      <Combobox
        options={options.filter((o) => !value.includes(o.value))}
        value=""
        onChange={(v) => v && onChange([...value, v])}
        placeholder={value.length ? "Add another…" : placeholder}
      />
    </div>
  );
}
