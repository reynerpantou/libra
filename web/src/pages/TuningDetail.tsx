import { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { Pager, usePaged } from "../components/Pager";
import { TargetingView } from "../components/Targeting";
import { CurveChart, ProgressChart, SpaceChart, shortPath } from "../components/TuningCharts";
import { ErrorBox, Icon, Loading, Modal, Tabs } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { copyText } from "../lib/exportTable";
import { ago, fmtDate, fmtDateTime, fmtInt, fmtP, fmtValue, trafficPct } from "../lib/format";
import { useAsync } from "../lib/hooks";
import { algorithmName, algorithms, sourceLabel } from "../lib/tuning";
import type { SurfacePoint, Tuning, TuningEstimate, TuningRound } from "../lib/types";
import { Header, History } from "./ExperimentDetail";
import { fmtWhen, relative } from "../components/Schedule";

type Tab = "results" | "rounds" | "setup" | "history";

const pct = (v: number, d = 1) => `${v > 0 ? "+" : v < 0 ? "−" : ""}${Math.abs(v * 100).toFixed(d)}%`;
const num = (v: number) => (Number.isInteger(v) ? String(v) : Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 1 ? v.toFixed(2) : v.toPrecision(3));

export default function TuningDetail() {
  const id = Number(useParams().id);
  const [params, setParams] = useSearchParams();
  const q = useAsync(() => api.tuning(id), [id]);
  const t = q.data;
  const tab = (params.get("tab") as Tab) || "results";
  const setTab = (x: Tab) => setParams({ tab: x }, { replace: true });
  if (q.loading && !t) return <div className="page"><Loading /></div>;
  if (!t) return <div className="page"><ErrorBox error={q.error || "Tuning study not found"} /></div>;
  const e = t.experiment;

  // Launch choices: the recommendation first, then every tested arm by lift.
  const tested: { id: number; label: string; lift: number }[] = [];
  const flip = t.config.objective_direction === "decrease" ? -1 : 1;
  for (const r of t.rounds) {
    for (const res of r.results?.arms ?? []) {
      const arm = r.arms.find((a) => a.variant_id === res.variant_id);
      if (!arm || !res.objective?.testable) continue;
      const vals = t.config.params.map((p, i) => `${shortPath(p.path)} ${num(arm.values[i])}`).join(", ");
      tested.push({ id: arm.variant_id, lift: flip * res.objective.rel_diff, label: `Round ${r.round} ${arm.name} · ${vals} · ${pct(flip * res.objective.rel_diff)}` });
    }
  }
  tested.sort((a, b) => b.lift - a.lift);
  const launchOptions = [
    ...(t.best ? [{ id: t.best.variant_id, label: `★ Recommended: round ${t.best.round} ${t.best.key.split("_").pop()} (${pct(t.best.predicted.mean)} expected)` }] : []),
    ...tested.filter((x) => x.id !== t.best?.variant_id).map(({ id, label }) => ({ id, label })),
  ];

  return (
    <div className="page">
      <div className="crumbs">
        <Link to="/tuning">AB Tuning</Link> / #{e.id}
      </div>
      <Header
        e={e}
        onChange={() => q.reload()}
        editTo={`/tuning/${e.id}/edit`}
        canClone={false}
        editable={["draft", "approved", "rejected"].includes(e.status)}
        launchOptions={launchOptions}
        defaultLaunch={t.best?.variant_id}
        extra={
          <>
            <AddRoundsButton t={t} onDone={q.reload} />
            <AdvanceButton t={t} onDone={q.reload} />
          </>
        }
      />
      <StatusStrip t={t} />
      <Tabs<Tab>
        tabs={[
          ["results", "Results"],
          ["rounds", `Rounds (${t.rounds.length})`],
          ["setup", "Setup"],
          ["history", "History"],
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === "results" && <Results t={t} />}
      {tab === "rounds" && <Rounds t={t} />}
      {tab === "setup" && <Setup t={t} />}
      {tab === "history" && <History id={e.id} version={e.updated_at} />}
    </div>
  );
}

function AddRoundsButton({ t, onDone, small }: { t: Tuning; onDone: () => void; small?: boolean }) {
  const canEdit = useCan("editor");
  const [open, setOpen] = useState(false);
  const [n, setN] = useState("2");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!canEdit || t.finished_at || ["stopped", "launched", "archived"].includes(t.experiment.status)) return null;
  return (
    <>
      <button className={`btn ${small ? "btn-sm" : ""}`} onClick={() => setOpen(true)}>
        <Icon name="plus" /> Extend
      </button>
      {open && (
        <Modal
          title="Extend the study"
          onClose={() => setOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setOpen(false)}>
                Cancel
              </button>
              <button
                className="btn btn-primary"
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  setError("");
                  try {
                    await api.extend(t.experiment.id, { rounds: Number(n) || 1 });
                    setOpen(false);
                    onDone();
                  } catch (err) {
                    setError(err instanceof Error ? err.message : String(err));
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                Add rounds
              </button>
            </>
          }
        >
          <p className="muted small">
            The study has {t.config.max_rounds} rounds of {t.config.round_days} day{t.config.round_days > 1 ? "s" : ""}. Adding rounds keeps the search going
            from where it is.
          </p>
          <div className="row">
            <input className="input" type="number" min={1} max={50} style={{ width: 100 }} value={n} onChange={(e) => setN(e.target.value)} />
            <span className="small muted">
              more round{n === "1" ? "" : "s"} → {t.config.max_rounds + (Number(n) || 0)} in total, about {(Number(n) || 0) * t.config.round_days} more day
              {(Number(n) || 0) * t.config.round_days === 1 ? "" : "s"}
            </span>
          </div>
          <ErrorBox error={error} />
        </Modal>
      )}
    </>
  );
}

function AdvanceButton({ t, onDone }: { t: Tuning; onDone: () => void }) {
  const canEdit = useCan("editor");
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const running = (t.experiment.status === "active" || t.experiment.status === "paused") && !t.finished_at;
  if (!canEdit || !running) return null;
  const last = t.round >= t.config.max_rounds;
  return (
    <>
      <button className="btn" onClick={() => setOpen(true)} title="Analyse the current round with the data so far and move on">
        <Icon name="refresh" /> {last ? "Finish now" : "End round now"}
      </button>
      {open && (
        <Modal
          title={last ? "Finish the study now?" : `End round ${t.round} now?`}
          onClose={() => setOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setOpen(false)}>
                Cancel
              </button>
              <button
                className="btn btn-primary"
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  setError("");
                  try {
                    await api.advanceTuning(t.experiment.id);
                    setOpen(false);
                    onDone();
                  } catch (err) {
                    setError(err instanceof Error ? err.message : String(err));
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                {busy ? "Analysing…" : last ? "Finish" : "End round"}
              </button>
            </>
          }
        >
          <p className="muted">
            Rounds end on their own at 00:00 UTC once the data is in. Ending early analyses round {t.round} with the data so far
            {last ? " and stops the study (the last round)." : `, then round ${t.round + 1}'s candidates start serving straight away.`} A short round
            means fewer units per arm, so noisier results.
          </p>
          <ErrorBox error={error} />
        </Modal>
      )}
    </>
  );
}

function StatusStrip({ t }: { t: Tuning }) {
  const cur = t.rounds.find((r) => r.round === t.round);
  const done = t.rounds.filter((r) => r.status === "analyzed").length;
  // The study ends when its last round does.
  const expectedEnd =
    !t.finished_at && cur?.status === "running" && cur.ends_at
      ? new Date(new Date(cur.ends_at).getTime() + (t.config.max_rounds - t.round) * t.config.round_days * 86400000).toISOString()
      : null;
  const endingSoon = expectedEnd && t.round >= t.config.max_rounds && new Date(expectedEnd).getTime() - Date.now() < 86400000;
  return (
    <>
    {endingSoon && (
      <div className="alert alert-warn small row-between" style={{ marginBottom: 12, gap: 10 }}>
        <span>The last round ends {relative(expectedEnd!)} — the study stops then unless you add rounds.</span>
        <AddRoundsButton t={t} onDone={() => window.location.reload()} small />
      </div>
    )}
    <section className="card card-pad row" style={{ gap: 24, flexWrap: "wrap", marginBottom: 12 }}>
      <div>
        <div className="small faint">Progress</div>
        <div className="round-pill">
          <span className="round-bar">
            <span style={{ width: `${(100 * done) / t.config.max_rounds}%` }} />
          </span>
          <b>
            {done} of {t.config.max_rounds} rounds
          </b>
        </div>
      </div>
      <div>
        <div className="small faint">Now</div>
        <b>
          {t.finished_at
            ? `Finished ${fmtDate(t.finished_at)}`
            : cur?.status === "running"
            ? `Round ${t.round} · ends ${cur.ends_at ? fmtDateTime(cur.ends_at) : "—"}`
            : cur?.status === "planned"
            ? `Round 1 is planned — starts with the study`
            : `Round ${t.round}`}
        </b>
      </div>
      <div>
        <div className="small faint">Algorithm</div>
        <b>{algorithmName(t.config.algorithm)}</b>
        {t.config.algorithm === "constrained" && t.state.trust ? <span className="faint small"> · trust region {t.state.trust.toFixed(2)}</span> : null}
      </div>
      <div>
        <div className="small faint">Arms per round</div>
        <b>v0 + {t.config.arms}</b> <span className="faint small">· {trafficPct(Math.floor(1000 / (t.config.arms + 1)))} each</span>
      </div>
      <div>
        <div className="small faint">Points measured</div>
        <b>{t.tested}</b>
      </div>
      <div>
        <div className="small faint">Data through</div>
        <b>{t.data_through ? ago(t.data_through) : "—"}</b>
      </div>
      {expectedEnd && (
        <div>
          <div className="small faint">Expected end</div>
          <b title={fmtWhen(expectedEnd)}>{relative(expectedEnd)}</b>
        </div>
      )}
      {!t.experiment.started_at && t.experiment.planned_start && (
        <div>
          <div className="small faint">Planned start</div>
          <b title={fmtWhen(t.experiment.planned_start)}>{relative(t.experiment.planned_start)}</b>
        </div>
      )}
      {t.note && <div className="small muted" style={{ flexBasis: "100%" }}>{t.note}</div>}
    </section>
    </>
  );
}

function Results({ t }: { t: Tuning }) {
  const n = t.config.params.length;
  const [xi, setXi] = useState(0);
  const [yi, setYi] = useState(n > 1 ? 1 : -1);
  const surf = useAsync<{ grid: SurfacePoint[] | null } | null>(async () => (t.tested >= 2 ? api.tuningSurface(t.experiment.id, xi, yi) : null), [t.experiment.id, xi, yi, t.tested]);
  useEffect(() => {
    if (yi === xi) setYi(xi === 0 ? 1 : 0);
  }, [xi, yi]);
  return (
    <div className="stack">
      <Recommendation t={t} />
      <section className="card">
        <div className="card-head">
          <div>
            <h2>Search progress</h2>
            <p>
              Every arm's {t.objective.name} lift versus v0 in its round ({t.config.objective_direction === "increase" ? "higher" : "lower"} is better;
              shown so up is better). Hover a dot for its values.
            </p>
          </div>
        </div>
        <div className="card-pad">
          <ProgressChart t={t} />
        </div>
      </section>
      <section className="card">
        <div className="card-head">
          <div>
            <h2>Search space</h2>
            <p>
              {n > 1
                ? "Where each round's points landed, over the model's map of expected lift. Other parameters are held at the recommendation."
                : "The model's expected lift across the range, and the points tested."}
            </p>
          </div>
          {n > 2 && (
            <div className="row">
              <select className="input" value={xi} onChange={(e) => setXi(Number(e.target.value))} aria-label="x axis">
                {t.config.params.map((p, i) => (
                  <option key={p.path} value={i}>
                    x: {p.path}
                  </option>
                ))}
              </select>
              <select className="input" value={yi} onChange={(e) => setYi(Number(e.target.value))} aria-label="y axis">
                {t.config.params.map((p, i) => (
                  <option key={p.path} value={i} disabled={i === xi}>
                    y: {p.path}
                  </option>
                ))}
              </select>
            </div>
          )}
        </div>
        <div className="card-pad">
          {n > 1 ? <SpaceChart t={t} xi={xi} yi={yi < 0 ? 1 : yi} surface={surf.data?.grid ?? null} /> : <CurveChart t={t} surface={surf.data?.grid ?? null} />}
        </div>
      </section>
    </div>
  );
}

function Recommendation({ t }: { t: Tuning }) {
  const [copied, setCopied] = useState(false);
  const b = t.best;
  if (!b)
    return (
      <section className="card card-pad">
        <h2>Recommendation</h2>
        <p className="muted">
          {t.experiment.status === "draft" || t.experiment.status === "in_review" || t.experiment.status === "approved"
            ? "Start the study: round 1 tries the planned points, and the recommendation appears after it's analysed."
            : `Round ${t.round} is collecting data. The first recommendation appears when a round has been analysed.`}
        </p>
      </section>
    );
  const json = JSON.stringify({ [t.experiment.platform_key]: b.params }, null, 2);
  const g = t.guardrails.length > 0;
  return (
    <section className="card">
      <div className="card-head">
        <div>
          <h2>Recommendation</h2>
          <p>
            The tested point the model rates best{g ? " among those likely to keep every guardrail" : ""}, from round {b.round} (variant{" "}
            <span className="mono">{b.variant_id}</span>). {t.finished_at ? "Launch it to serve these values to everyone." : "It's re-tested each round while the search goes on."}
          </p>
        </div>
        <button
          className="btn btn-sm"
          onClick={async () => {
            await copyText(json);
            setCopied(true);
            setTimeout(() => setCopied(false), 1200);
          }}
        >
          {copied ? "Copied ✓" : "Copy params JSON"}
        </button>
      </div>
      <div className="card-pad grid-2" style={{ alignItems: "start" }}>
        <table className="tbl tbl-compact">
          <thead>
            <tr>
              <th>Parameter</th>
              <th className="num">v0 (today)</th>
              <th className="num">Recommended</th>
              <th className="num">Change</th>
            </tr>
          </thead>
          <tbody>
            {t.config.params.map((p, i) => (
              <tr key={p.path}>
                <td className="mono small">{p.path}</td>
                <td className="num">{num(p.control)}</td>
                <td className="num">
                  <b>{num(b.values[i])}</b>
                </td>
                <td className="num faint">{p.control ? pct((b.values[i] - p.control) / Math.abs(p.control), 0) : "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <div className="row" style={{ gap: 24, alignItems: "flex-start" }}>
          <div>
            <div className="small faint">Expected {t.objective.name} lift</div>
            <div style={{ fontSize: 26, fontWeight: 700 }} className={b.predicted.mean > 0 ? "tone-good" : b.predicted.mean < 0 ? "tone-bad" : ""}>
              {pct(b.predicted.mean)}
            </div>
            <div className="small faint">± {(b.predicted.sd * 100).toFixed(1)}% (model, all rounds)</div>
          </div>
          <div>
            <div className="small faint">Measured in round {b.round}</div>
            <div style={{ fontSize: 26, fontWeight: 700 }}>{pct(b.observed)}</div>
            <div className="small faint">one round's estimate</div>
          </div>
          {g && (
            <div>
              <div className="small faint">Guardrails hold</div>
              <div style={{ fontSize: 26, fontWeight: 700 }}>{Math.round(b.predicted.feasible * 100)}%</div>
              <div className="small faint">probability</div>
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

function Lift({ est, flip }: { est?: TuningEstimate; flip: number }) {
  if (!est || !est.testable) return <span className="faint">—</span>;
  const v = flip * est.rel_diff;
  const cls = v > 0 ? "tone-good" : v < 0 ? "tone-bad" : "tone-none";
  return (
    <span className={`${cls} ${est.significant ? "sig" : ""}`} style={{ padding: "1px 6px", borderRadius: 5 }} title={`${pct(est.rel_ci_low)} to ${pct(est.rel_ci_high)} · p ${fmtP(est.p_value)}`}>
      {pct(est.rel_diff)}
    </span>
  );
}

function Rounds({ t }: { t: Tuning }) {
  const ordered = [...t.rounds].reverse();
  const paged = usePaged(ordered, 5, "");
  return (
    <div className="stack">
      {paged.slice.map((r) => (
        <RoundCard key={r.round} t={t} r={r} />
      ))}
      <section className="card">
        <Pager page={paged.page} pages={paged.pages} total={paged.total} size={paged.size} onPage={paged.setPage} onSize={paged.setSize} noun="rounds" />
      </section>
    </div>
  );
}

function RoundCard({ t, r }: { t: Tuning; r: TuningRound }) {
  const flip = t.config.objective_direction === "decrease" ? -1 : 1;
  const [copied, setCopied] = useState<number | null>(null);
  const res = r.results;
  return (
    <section className="card">
      <div className="card-head">
        <div>
          <h2>
            Round {r.round}{" "}
            <span className={`badge ${r.status === "running" ? "b-good" : r.status === "planned" ? "" : "b-neutral"}`}>
              {r.status === "analyzed" ? "analysed" : r.status}
            </span>
          </h2>
          <p>
            {r.started_at ? fmtDateTime(r.started_at) : "not started"} → {r.ended_at ? fmtDateTime(r.ended_at) : r.ends_at ? `due ${fmtDateTime(r.ends_at)}` : "—"}
            {res && ` · v0: ${fmtInt(res.control_units)} units, ${t.objective.name} ${fmtValue(res.control_value, t.objective.format as never, t.objective.decimals)}`}
          </p>
          {r.note && <p className="small faint">{r.note}</p>}
        </div>
      </div>
      <div className="table-wrap">
        <table className="tbl tbl-compact">
          <thead>
            <tr>
              <th>Arm</th>
              {t.config.params.map((p) => (
                <th key={p.path} className="num mono" title={p.path}>
                  {shortPath(p.path)}
                </th>
              ))}
              <th className="num">Units</th>
              <th className="num">{t.objective.name}</th>
              <th className="num">vs v0</th>
              <th className="num">p</th>
              {t.guardrails.map((g) => (
                <th key={g.id} className="num" title={`Guardrail: at most ${(100 * (g.max_drop ?? 0)).toFixed(1)}% worse`}>
                  {g.name}
                </th>
              ))}
              <th>Variant id</th>
            </tr>
          </thead>
          <tbody>
            {r.arms.map((a) => {
              const ar = res?.arms.find((x) => x.variant_id === a.variant_id);
              return (
                <tr key={a.variant_id} style={a.is_control ? { background: "var(--surface-2)" } : undefined}>
                  <td className="nowrap">
                    <b>{a.is_control ? "v0" : a.name}</b> <span className="chip">{sourceLabel[a.source] ?? a.source}</span>
                  </td>
                  {t.config.params.map((p, i) => (
                    <td key={p.path} className="num">
                      {num(a.values[i])}
                    </td>
                  ))}
                  <td className="num">{a.is_control ? fmtInt(res?.control_units) : ar ? fmtInt(ar.units) : "—"}</td>
                  <td className="num">
                    {a.is_control
                      ? res
                        ? fmtValue(res.control_value, t.objective.format as never, t.objective.decimals)
                        : "—"
                      : ar?.objective
                      ? fmtValue(ar.objective.value, t.objective.format as never, t.objective.decimals)
                      : "—"}
                  </td>
                  <td className="num">
                    {a.is_control ? (
                      <span className="faint">baseline</span>
                    ) : ar ? (
                      <Lift est={ar.objective} flip={flip} />
                    ) : a.predicted ? (
                      <span className="faint" title="The model's expectation before measuring">
                        exp. {pct(a.predicted.mean)}
                      </span>
                    ) : (
                      <span className="faint">—</span>
                    )}
                  </td>
                  <td className="num faint">{ar?.objective?.testable ? fmtP(ar.objective.p_value) : ""}</td>
                  {t.guardrails.map((g, gi) => {
                    const ge = ar?.guardrails[gi];
                    return (
                      <td key={g.id} className="num nowrap">
                        {ge && ge.testable ? (
                          <span className={ge.holds ? "" : "tone-bad"} title={`${pct(ge.rel_ci_low)} to ${pct(ge.rel_ci_high)}`}>
                            {ge.holds ? "✓" : "✗"} {pct(ge.rel_diff)}
                          </span>
                        ) : (
                          <span className="faint">—</span>
                        )}
                      </td>
                    );
                  })}
                  <td className="mono small nowrap">
                    {a.variant_id}{" "}
                    <button
                      className="copy-btn"
                      onClick={async () => {
                        await copyText(String(a.variant_id));
                        setCopied(a.variant_id);
                        setTimeout(() => setCopied(null), 1000);
                      }}
                    >
                      {copied === a.variant_id ? "✓" : "⧉"}
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Setup({ t }: { t: Tuning }) {
  const e = t.experiment;
  const algo = algorithms.find((a) => a.key === t.config.algorithm);
  return (
    <div className="grid-2" style={{ alignItems: "start" }}>
      <section className="card card-pad stack">
        <h2>Search</h2>
        <div>
          <b>{algo?.name}</b>
          <p className="small muted">{algo?.long}</p>
        </div>
        <table className="tbl tbl-compact">
          <thead>
            <tr>
              <th>Parameter</th>
              <th>Type</th>
              <th className="num">Range</th>
              <th className="num">v0</th>
            </tr>
          </thead>
          <tbody>
            {t.config.params.map((p) => (
              <tr key={p.path}>
                <td className="mono small">{p.path}</td>
                <td className="small">
                  {p.type}
                  {p.scale === "log" ? " · log" : ""}
                  {p.step ? ` · step ${p.step}` : ""}
                </td>
                <td className="num">
                  {num(p.min)} – {num(p.max)}
                </td>
                <td className="num">{num(p.control)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <dl className="kv">
          <dt>Rounds</dt>
          <dd>
            up to {t.config.max_rounds} × {t.config.round_days} day{t.config.round_days > 1 ? "s" : ""}
            {t.config.min_units > 0 && `, at least ${fmtInt(t.config.min_units)} units per arm`}
          </dd>
          <dt>Arms</dt>
          <dd>
            v0 + {t.config.arms} candidates{t.config.keep_best ? " (one re-tests the best so far)" : ""}
          </dd>
          <dt>Objective</dt>
          <dd>
            {t.objective.name} — {t.config.objective_direction === "increase" ? "maximise" : "minimise"}
          </dd>
          <dt>Guardrails</dt>
          <dd>
            {t.guardrails.length === 0
              ? "none"
              : t.guardrails.map((g) => `${g.name}: at most ${(100 * (g.max_drop ?? 0)).toFixed(1)}% worse`).join(" · ")}
          </dd>
        </dl>
        {Object.keys(t.config.base_params ?? {}).length > 0 && (
          <div>
            <b className="small">Served by every arm as well</b>
            <pre className="code">{JSON.stringify({ [e.platform_key]: t.config.base_params }, null, 2)}</pre>
          </div>
        )}
      </section>
      <section className="card card-pad stack">
        <h2>Traffic</h2>
        <dl className="kv">
          <dt>Platform</dt>
          <dd>{e.platform_name}</dd>
          <dt>Businesses</dt>
          <dd>{(e.business_names ?? [e.business_name]).join(", ")}</dd>
          <dt>Layer</dt>
          <dd>{e.layer_auto ? `Dedicated, split by ${e.layer_diversion}` : `${e.layer_name} (by ${e.layer_diversion})`}</dd>
          <dt>Traffic</dt>
          <dd>{e.status === "active" || e.status === "paused" ? trafficPct(e.traffic_held) : `target ${trafficPct(e.traffic_target)}`}</dd>
          <dt>Re-randomised</dt>
          <dd>every round — units are split across the arms afresh, so last round's arm doesn't carry over</dd>
        </dl>
        <div>
          <b className="small">Targeting</b>
          <TargetingView value={e.targeting} />
        </div>
        <p className="small muted">
          Traffic ramps, test users and the audit trail work as for any experiment:{" "}
          <Link to={`/experiments/${e.id}?tab=overview`}>open the experiment view</Link>.
        </p>
      </section>
    </div>
  );
}
