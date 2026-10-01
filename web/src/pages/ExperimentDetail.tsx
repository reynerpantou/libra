import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { LineChart } from "../components/LineChart";
import { ParamTree, UsagePanel } from "../components/ParamTree";
import { Pager, usePaged } from "../components/Pager";
import { ReviewerPicker } from "../components/ReviewerPicker";
import { RolloutChoice, RolloutStatus, schedule } from "../components/Rollout";
import ReportView from "../components/ReportView";
import { TargetingView } from "../components/Targeting";
import { Empty, ErrorBox, Field, Icon, Loading, Modal, Popover, StatusBadge, Tabs, TrafficBar } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { actionLabel, fmtDate, fmtDateTime, fmtInt, statusLabel, trafficPct } from "../lib/format";
import { diversionName, useDiversions } from "../lib/diversions";
import { useAsync } from "../lib/hooks";
import type { Experiment, Gradual, Layer, Status } from "../lib/types";

type Tab = "report" | "overview" | "whitelist" | "history";

export default function ExperimentDetail() {
  const id = Number(useParams().id);
  const [params, setParams] = useSearchParams();
  const exp = useAsync(() => api.experiment(id), [id]);
  const diversions = useDiversions();
  const e = exp.data;
  const defaultTab: Tab = e && (e.units > 0 || ["active", "paused", "stopped", "launched"].includes(e.status)) ? "report" : "overview";
  const tab = (params.get("tab") as Tab) || defaultTab;
  const setTab = (t: Tab) => setParams({ tab: t }, { replace: true });

  if (exp.loading && !e) return <div className="page"><Loading /></div>;
  if (!e) return <div className="page"><ErrorBox error={exp.error || "Experiment not found"} /></div>;

  return (
    <div className="page">
      <div className="crumbs">
        <Link to="/experiments">Experiments</Link> / #{e.id}
      </div>
      <Header e={e} onChange={(x) => exp.setData(x)} />
      <Tabs<Tab>
        tabs={[
          ["report", "Report"],
          ["overview", "Overview & traffic"],
          ["whitelist", `Test ${unitNoun(e.layer_diversion, diversionName(diversions, e.layer_diversion))} (${e.whitelist?.length ?? 0})`],
          ["history", "History"],
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === "report" && <ReportView experiment={e} />}
      {tab === "overview" && <Overview e={e} onChange={(x) => exp.setData(x)} />}
      {tab === "whitelist" && <Whitelist e={e} reload={exp.reload} />}
      {tab === "history" && <History id={e.id} version={e.updated_at} />}
    </div>
  );
}

function Header({ e, onChange }: { e: Experiment; onChange: (e: Experiment) => void }) {
  const canEdit = useCan("editor");
  const nav = useNavigate();
  const [pending, setPending] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [variantId, setVariantId] = useState<number>(0);
  const [gradual, setGradual] = useState<Gradual | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const actions = e.actions ?? [];
  const primary = ["start", "submit", "approve", "resume"];
  const needsDialog = ["reject", "approve", "launch", "stop", "archive", "pause", "start", "submit"];
  const [reviewerIds, setReviewerIds] = useState<number[]>([]);
  const [inviting, setInviting] = useState(false);
  const biz = useAsync(async () => (pending === "submit" ? api.business(e.business_id) : null), [pending, e.business_id]);
  const reviewSkipped = pending === "submit" && biz.data && !biz.data.require_review;

  const run = async (action: string) => {
    setBusy(true);
    setError("");
    try {
      const g = (action === "start" || action === "launch") && gradual ? gradual : undefined;
      onChange(
        await api.action(e.id, action, { note, variant_id: variantId || undefined, gradual: g, reviewer_ids: action === "submit" ? reviewerIds : undefined })
      );
      setReviewerIds([]);
      setPending(null);
      setNote("");
      setGradual(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const confirmText: Record<string, string> = {
    stop: "Stopping releases the traffic. Units go back to default behaviour, and the report freezes at today. This can't be undone.",
    archive: "Archived experiments are hidden from the default list. This can't be undone.",
    pause: "Pausing stops serving the experiment, but it keeps its traffic so resuming brings the same units back.",
    approve: "Approve this experiment so its owner can start it.",
  };

  return (
    <div className="page-head">
      <div style={{ minWidth: 0 }}>
        <div className="row">
          <h1>{e.name}</h1>
          <StatusBadge status={e.status} />
        </div>
        <p>{e.hypothesis || <span className="faint">No hypothesis written.</span>}</p>
        <div className="row small faint" style={{ marginTop: 6 }}>
          <span>{e.business_name}</span>·<span>{e.layer_auto ? `dedicated layer (by ${e.layer_diversion})` : `layer ${e.layer_name}`}</span>·<span>owner {e.owner_name || "—"}</span>·
          <span>
            {e.status === "active" || e.status === "paused"
              ? `${trafficPct(e.traffic_held)} traffic`
              : e.status === "launched"
              ? `launched to ${trafficPct(e.launch_rollout)} of units`
              : `target ${trafficPct(e.traffic_target)}`}
          </span>
          {e.started_at && <>·<span>started {fmtDate(e.started_at)}</span></>}
        </div>
        {e.status === "rejected" && e.review_note && (
          <div className="alert alert-bad small" style={{ marginTop: 10 }}>
            Rejected by {e.reviewer_name}: {e.review_note}
          </div>
        )}
        {e.status === "in_review" && (
          <div className="alert alert-warn small row-between" style={{ marginTop: 10, gap: 10 }}>
            <span>
              {(e.reviewers ?? []).length === 0 ? (
                <>No reviewers invited yet — invite at least one (you can invite yourself).</>
              ) : (
                <>
                  Waiting for review by{" "}
                  {(e.reviewers ?? []).map((r, i) => (
                    <span key={r.user_id}>
                      {i > 0 && ", "}
                      <b>{r.name}</b>
                      {r.decision && ` (${r.decision})`}
                    </span>
                  ))}
                  . Any one of them can approve or reject.
                </>
              )}
            </span>
            {canEdit && (
              <button className="btn btn-sm" onClick={() => setInviting(true)}>
                <Icon name="plus" /> Invite reviewer
              </button>
            )}
          </div>
        )}
      </div>
      {canEdit && (
        <div className="row">
          {actions.map((a) => (
            <button
              key={a}
              className={`btn ${primary.includes(a) ? "btn-primary" : ""} ${a === "reject" || a === "stop" ? "btn-danger" : ""}`}
              disabled={busy}
              onClick={() => {
                setError("");
                if (needsDialog.includes(a)) {
                  setPending(a);
                  setVariantId(e.variants?.find((v) => !v.is_control)?.id ?? 0);
                } else run(a);
              }}
            >
              {actionLabel[a]}
            </button>
          ))}
          {!["stopped", "launched", "archived", "in_review"].includes(e.status) && (
            <Link className="btn" to={`/experiments/${e.id}/edit`}>
              <Icon name="edit" /> Edit
            </Link>
          )}
          <button
            className="btn"
            title="Copy into a new draft"
            onClick={async () => {
              const r = await api.clone(e.id);
              nav(`/experiments/${r.id}`);
            }}
          >
            <Icon name="copy" /> Clone
          </button>
        </div>
      )}
      {error && !pending && <div style={{ width: "100%" }}><ErrorBox error={error} /></div>}
      {inviting && (
        <Modal
          title="Invite reviewers"
          onClose={() => setInviting(false)}
          footer={
            <>
              <button className="btn" onClick={() => setInviting(false)}>
                Cancel
              </button>
              <button
                className="btn btn-primary"
                disabled={busy || reviewerIds.length === 0}
                onClick={async () => {
                  setBusy(true);
                  setError("");
                  try {
                    onChange(await api.inviteReviewers(e.id, reviewerIds));
                    setReviewerIds([]);
                    setInviting(false);
                  } catch (err) {
                    setError(err instanceof Error ? err.message : String(err));
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                Invite
              </button>
            </>
          }
        >
          <ReviewerPicker value={reviewerIds} onChange={setReviewerIds} already={(e.reviewers ?? []).map((r) => ({ user_id: r.user_id, name: r.name }))} />
          <ErrorBox error={error} />
        </Modal>
      )}
      {pending && (
        <Modal
          title={actionLabel[pending]}
          onClose={() => setPending(null)}
          footer={
            <>
              <button className="btn" onClick={() => setPending(null)}>
                Cancel
              </button>
              <button className={`btn ${pending === "stop" || pending === "reject" ? "btn-danger" : "btn-primary"}`} disabled={busy || (pending === "submit" && !reviewSkipped && reviewerIds.length === 0)} onClick={() => run(pending)}>
                {actionLabel[pending]}
              </button>
            </>
          }
        >
          {confirmText[pending] && <p className="muted">{confirmText[pending]}</p>}
          {pending === "submit" &&
            (reviewSkipped ? (
              <p className="muted">{e.business_name} doesn't require review: submitting approves the experiment right away.</p>
            ) : (
              // Not a <Field>: that's a <label>, and a click anywhere in a
              // label also clicks its first button (a chip's ×).
              <div className="stack-sm">
                <b className="small">Reviewers</b>
                <ReviewerPicker value={reviewerIds} onChange={setReviewerIds} />
                <div className="small faint">Invite at least one editor or admin — you can invite yourself. Any one of them can approve or reject.</div>
              </div>
            ))}
          {pending === "start" && (
            <>
              <p className="muted">
                Start serving the experiment to <b>{trafficPct(e.traffic_target)}</b> of {e.layer_auto ? "its units" : `layer ${e.layer_name}`}. Go
                there at once, or ramp up gradually and watch the metrics as it grows.
              </p>
              <RolloutChoice from={0} target={e.traffic_target} value={gradual} onChange={setGradual} withStart noun="Traffic" />
            </>
          )}
          {pending === "launch" && (
            <>
              <p className="muted">
                Launching serves the chosen variant's parameters to everyone who matches the targeting, releases the experiment's traffic, and stops
                measurement. Running experiments can still override launched parameters.
              </p>
              <Field label="Variant to launch">
                <select className="input" value={variantId} onChange={(x) => setVariantId(Number(x.target.value))}>
                  {e.variants?.map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.name || v.key} {v.is_control ? "(control)" : ""}
                    </option>
                  ))}
                </select>
              </Field>
              <Field group label="Release" hint="A gradual release serves the variant to a growing share of units; the rest keep the current defaults.">
                <RolloutChoice from={0} target={1000} value={gradual} onChange={setGradual} withStart noun="The launch" />
              </Field>
            </>
          )}
          {(pending === "reject" || pending === "approve") && (
            <Field label={pending === "reject" ? "Why? (required)" : "Note (optional)"}>
              <textarea className="input" rows={3} value={note} onChange={(x) => setNote(x.target.value)} />
            </Field>
          )}
          <ErrorBox error={error} />
        </Modal>
      )}
    </div>
  );
}

function Overview({ e, onChange }: { e: Experiment; onChange: (e: Experiment) => void }) {
  const layers = useAsync(() => api.layers(), [e.updated_at]);
  const exposures = useAsync(() => api.exposures(e.id), [e.id]);
  const usage = useAsync(() => api.paramUsage(e.id), [e.id, e.updated_at]);
  const diversions = useDiversions();
  const [sel, setSel] = useState<{ path: string; anchor: HTMLElement } | null>(null);
  const layer = layers.data?.find((l) => l.id === e.layer_id);
  const running = e.status === "active" || e.status === "paused";
  const free = layer ? 1000 - layer.used_buckets + (running ? e.traffic_held : 0) : 1000;

  const days = exposures.data ?? [];
  return (
    <div className="stack">
      <div className="grid-2">
        <section className="card card-pad stack">
          <h2>Setup</h2>
          <dl className="kv">
            <dt>Business</dt>
            <dd>
              <Link to={`/businesses/${e.business_id}`}>{e.business_name}</Link>
            </dd>
            <dt>Layer</dt>
            <dd>{e.layer_auto ? <span title="A layer of its own: no other experiment shares its traffic">Dedicated (auto)</span> : e.layer_name}</dd>
            <dt>Status</dt>
            <dd>{statusLabel[e.status as Status]}</dd>
            <dt>Created</dt>
            <dd>{fmtDateTime(e.created_at)}</dd>
            <dt>Started</dt>
            <dd>{fmtDateTime(e.started_at)}</dd>
            <dt>Ended</dt>
            <dd>{fmtDateTime(e.ended_at)}</dd>
            {e.reviewer_name && (
              <>
                <dt>Reviewed by</dt>
                <dd>
                  {e.reviewer_name}
                  {e.review_note && <span className="faint"> — {e.review_note}</span>}
                </dd>
              </>
            )}
            <dt>Targeting</dt>
            <dd>
              <TargetingView value={e.targeting} />
            </dd>
            <dt>Split by</dt>
            <dd>
              {diversionName(diversions, e.layer_diversion)} <span className="mono faint">{e.layer_diversion}</span>
            </dd>
            <dt>Units measured</dt>
            <dd>{fmtInt(e.units)}</dd>
          </dl>
          {e.description && <p className="muted">{e.description}</p>}
        </section>

        <TrafficPanel e={e} onChange={onChange} layer={layer} free={free} />
      </div>

      <section className="card">
        <div className="card-head">
          <div>
            <h2>Variants</h2>
            <p>
              Click any field to see which other experiments use it and who wins a conflict.{" "}
              <span className="ptree" style={{ padding: "0 4px", display: "inline" }}>
                <span className="pkey used">underlined</span>
              </span>{" "}
              = also used elsewhere,{" "}
              <span className="ptree" style={{ padding: "0 4px", display: "inline" }}>
                <span className="pkey conflict">highlighted</span>
              </span>{" "}
              = can conflict.
            </p>
          </div>
        </div>
        <div className="table-wrap">
          {/* Fixed layout: the variant column stays narrow and the JSON gets
              the rest, scrolling inside its own box when long or wide. */}
          <table className="tbl variants-tbl">
            <colgroup>
              <col style={{ width: 240 }} />
              <col />
            </colgroup>
            <thead>
              <tr>
                <th>Variant</th>
                <th>Parameters served</th>
              </tr>
            </thead>
            <tbody>
              {e.variants?.map((v) => {
                const wl = (e.whitelist ?? []).filter((w) => w.variant_id === v.id);
                const noun = unitNoun(e.layer_diversion, diversionName(diversions, e.layer_diversion));
                return (
                  <tr key={v.id}>
                    <td style={{ verticalAlign: "top" }}>
                      <div style={{ fontWeight: 600, wordBreak: "break-word" }}>
                        {v.name || v.key}{" "}
                        {v.is_control && <span className="badge">control</span>}{" "}
                        {e.launched_variant_id === v.id && <span className="badge b-accent">launched</span>}
                      </div>
                      <div className="mono faint small" style={{ wordBreak: "break-all" }}>
                        {v.key} · id {v.id}
                      </div>
                      <div className="small" style={{ marginTop: 4 }}>
                        {trafficPct(v.weight)} <span className="faint">of the experiment's units</span>
                      </div>
                      <div className="small" style={{ marginTop: 6 }}>
                        {wl.length === 0 ? (
                          <span className="faint">No test {noun}</span>
                        ) : (
                          <>
                            <span className="faint">
                              {wl.length} test {noun}:
                            </span>
                            <div className="chip-row" style={{ marginTop: 3 }}>
                              {wl.slice(0, 5).map((w) => (
                                <span key={w.unit_id} className="chip" title={w.note || undefined} style={{ maxWidth: 200, overflow: "hidden", textOverflow: "ellipsis" }}>
                                  {w.unit_id}
                                </span>
                              ))}
                              {wl.length > 5 && <Link to="?tab=whitelist">+{wl.length - 5} more</Link>}
                            </div>
                          </>
                        )}
                      </div>
                    </td>
                    <td style={{ minWidth: 0 }}>
                      <ParamBox>
                        <ParamTree
                          value={v.params}
                          usage={usage.data?.paths ?? null}
                          selected={sel?.anchor.isConnected ? sel.path : ""}
                          onSelect={(p, anchor) => setSel(sel?.anchor === anchor ? null : { path: p, anchor })}
                        />
                      </ParamBox>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {sel && usage.data && (
          <Popover anchor={sel.anchor} onClose={() => setSel(null)}>
            <UsagePanel path={sel.path} uses={usage.data.paths[sel.path] ?? []} rules={usage.data.priority_rules} onClose={() => setSel(null)} />
          </Popover>
        )}
      </section>

      <section className="card card-pad stack">
        <div>
          <h2>New units per day</h2>
          <p className="faint small">First exposures by variant, after the pipeline processes them. A flat line while running means exposures aren't arriving.</p>
        </div>
        {days.length === 0 ? (
          <Empty title="No exposures yet" />
        ) : (
          <LineChart
            labels={days.map((d) => d.day)}
            series={(e.variants ?? []).map((v) => ({ name: v.name || v.key, points: days.map((d) => d.variants[String(v.id)] ?? 0) }))}
            format={(v) => Math.round(v).toLocaleString()}
            height={180}
          />
        )}
      </section>
    </div>
  );
}

// TrafficPanel changes how much traffic the experiment gets (or, once
// launched, how many units the launched variant reaches): immediately, or
// gradually on a schedule.
function TrafficPanel({ e, onChange, layer, free }: { e: Experiment; onChange: (e: Experiment) => void; layer?: Layer; free: number }) {
  const canEdit = useCan("editor");
  const launched = e.status === "launched";
  const running = e.status === "active" || e.status === "paused";
  const finished = ["stopped", "archived"].includes(e.status);
  const current = launched ? e.launch_rollout : e.traffic_target;
  const max = launched || e.layer_auto ? 1000 : free;
  const [value, setValue] = useState(current);
  const [gradual, setGradual] = useState<Gradual | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const plans = useAsync(() => api.rollouts(e.id), [e.id, e.updated_at]);
  useEffect(() => setValue(current), [current]);
  const canGradual = running || launched;
  const g = canGradual && value > current ? gradual : null;
  const apply = async () => {
    setBusy(true);
    setError("");
    try {
      onChange(await api.setTraffic(e.id, value, g ?? undefined));
      setGradual(null);
      plans.reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  const steps = g ? schedule(current, value, g) : [];
  return (
    <section className="card card-pad stack">
      <h2>{launched ? "Launch rollout" : "Traffic"}</h2>
      <div className="row-between">
        <div>
          <div style={{ fontSize: 26, fontWeight: 650 }}>{launched ? trafficPct(e.launch_rollout) : running ? trafficPct(e.traffic_held) : trafficPct(0)}</div>
          <div className="faint small">
            {launched
              ? "of targeted units get the launched variant; the rest keep the previous defaults"
              : running
              ? "of the layer's units are in this experiment"
              : finished
              ? "traffic released"
              : `will take ${trafficPct(e.traffic_target)} when started`}
          </div>
        </div>
        {!launched &&
          (e.layer_auto ? (
            <div className="right small faint">
              Dedicated layer
              <br />
              no other experiments
            </div>
          ) : (
            layer && (
              <div className="right small faint">
                Layer {layer.name}
                <br />
                {trafficPct(1000 - layer.used_buckets)} free
              </div>
            )
          ))}
      </div>
      {!launched && layer && !e.layer_auto && (
        <div className="bucketbar" title="Layer occupancy">
          {layer.holders.map((h, i) => (
            <div
              key={h.experiment_id}
              title={`#${h.experiment_id} ${h.name}: ${trafficPct(h.buckets)}`}
              style={{ width: `${h.buckets / 10}%`, background: h.experiment_id === e.id ? "var(--accent)" : `var(--series-${(i % 4) + 2})`, opacity: h.experiment_id === e.id ? 1 : 0.45 }}
            />
          ))}
        </div>
      )}
      {launched && (
        <div className="rollout-bar">
          <div style={{ width: `${e.launch_rollout / 10}%` }} />
        </div>
      )}
      <RolloutStatus plans={plans.data ?? []} current={current} kind={launched ? "launch" : "traffic"} canEdit={canEdit} onCancelled={plans.reload} />
      {canEdit && !finished && e.status !== "in_review" && (
        <>
          <Field
            label={`${launched ? "Roll out to" : "Target"}: ${trafficPct(value)}`}
            hint={
              launched
                ? "Raising it keeps everyone who already has the launched variant."
                : `Up to ${trafficPct(max)} is available. Ramping up keeps everyone already in; ramping down removes the most recent units first.`
            }
          >
            <input type="range" min={launched ? 5 : 0} max={1000} step={5} value={value} onChange={(x) => setValue(Math.max(launched ? 1 : 0, Math.min(Number(x.target.value), max)))} />
            {!launched && <TrafficBar mine={value} free={max} />}
          </Field>
          <div className="row">
            {[10, 50, 100, 200, 500, 1000].map((v) => (
              <button key={v} className="btn btn-sm" disabled={v > max} onClick={() => setValue(v)}>
                {trafficPct(v)}
              </button>
            ))}
          </div>
          {canGradual ? (
            <RolloutChoice from={current} target={value} value={gradual} onChange={setGradual} noun={launched ? "The launch" : "Traffic"} />
          ) : (
            <p className="small faint">Gradual ramps are available once the experiment runs — or start it gradually.</p>
          )}
          <div className="row">
            <span className="spacer" />
            <button className="btn btn-primary btn-sm" disabled={busy || value === current} onClick={apply}>
              {g ? `Ramp to ${trafficPct(value)} in ${steps.length} steps` : `Apply ${trafficPct(value)} now`}
            </button>
          </div>
          {e.status === "approved" && <p className="small faint">Changing traffic sends an approved experiment back to draft.</p>}
          <ErrorBox error={error} />
        </>
      )}
    </section>
  );
}

// ParamBox caps a variant's JSON at a readable height; long or wide
// configs scroll inside it, and Expand shows the whole thing.
function ParamBox({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const [tall, setTall] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (el) setTall(el.scrollHeight > el.clientHeight + 4);
  });
  return (
    <div>
      <div ref={ref} className={`param-box ${open ? "open" : ""}`}>
        {children}
      </div>
      {(tall || open) && (
        <button className="btn btn-ghost btn-sm" onClick={() => setOpen(!open)} style={{ marginTop: 4 }}>
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}

// unitNoun names the units an experiment splits by: users, devices, or
// e.g. "shop ids" for other diversions.
export function unitNoun(key: string, name: string): string {
  if (key === "user_id") return "users";
  if (key === "device_id") return "devices";
  return `${name.toLowerCase()}s`;
}

type WLSort = "newest" | "oldest" | "id" | "variant";

function Whitelist({ e, reload }: { e: Experiment; reload: () => void }) {
  const canEdit = useCan("editor");
  const diversions = useDiversions();
  const [units, setUnits] = useState("");
  const [variant, setVariant] = useState<number>(e.variants?.find((v) => !v.is_control)?.id ?? 0);
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [q, setQ] = useState("");
  const [only, setOnly] = useState(0);
  const [sort, setSort] = useState<WLSort>("newest");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const name = (vid: number) => {
    const v = e.variants?.find((x) => x.id === vid);
    return v ? v.name || v.key : `#${vid}`;
  };
  const add = async () => {
    setError("");
    try {
      await api.addWhitelist(e.id, units.split(/[\s,]+/).filter(Boolean), variant, note);
      setUnits("");
      setNote("");
      reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };
  const remove = async (ids: string[]) => {
    if (ids.length > 1 && !confirm(`Remove ${ids.length} test ${noun}?`)) return;
    setError("");
    try {
      await api.removeWhitelistMany(e.id, ids);
      setPicked(new Set());
      reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };
  const dname = diversionName(diversions, e.layer_diversion);
  const noun = unitNoun(e.layer_diversion, dname);
  const all = e.whitelist ?? [];
  const query = q.trim().toLowerCase();
  const found = all
    .filter((w) => (!only || w.variant_id === only) && (!query || w.unit_id.toLowerCase().includes(query) || w.note.toLowerCase().includes(query)))
    .sort((a, b) =>
      sort === "id"
        ? a.unit_id.localeCompare(b.unit_id, undefined, { numeric: true })
        : sort === "variant"
        ? name(a.variant_id).localeCompare(name(b.variant_id)) || a.unit_id.localeCompare(b.unit_id, undefined, { numeric: true })
        : sort === "oldest"
        ? a.created_at.localeCompare(b.created_at)
        : b.created_at.localeCompare(a.created_at)
    );
  const paged = usePaged(found, 25, `${query}|${only}|${sort}`);
  const pageIds = paged.slice.map((w) => w.unit_id);
  const allOnPage = pageIds.length > 0 && pageIds.every((id) => picked.has(id));
  const counts = new Map<number, number>();
  for (const w of all) counts.set(w.variant_id, (counts.get(w.variant_id) ?? 0) + 1);
  return (
    <div className="grid-2" style={{ alignItems: "start", gridTemplateColumns: "minmax(0, 3fr) minmax(0, 2fr)" }}>
      <section className="card">
        <div className="card-head">
          <div>
            <h2>
              Test {noun} <span className="faint">({all.length})</span>
            </h2>
            <p>
              Whitelisted {noun} (by <span className="mono">{e.layer_diversion}</span>) always get their variant — even before the experiment starts — and
              are left out of reports.
            </p>
          </div>
        </div>
        {all.length === 0 ? (
          <Empty title={`No test ${noun}`} />
        ) : (
          <>
            <div className="row" style={{ gap: 8, padding: "10px 16px" }}>
              <input className="input" style={{ flex: 1, minWidth: 160 }} placeholder={`Search ${dname} or note…`} value={q} onChange={(x) => setQ(x.target.value)} />
              <select className="input" style={{ width: 170 }} value={only} onChange={(x) => setOnly(Number(x.target.value))} aria-label="Variant">
                <option value={0}>All variants</option>
                {e.variants?.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.name || v.key} ({counts.get(v.id!) ?? 0})
                  </option>
                ))}
              </select>
              <select className="input" style={{ width: 150 }} value={sort} onChange={(x) => setSort(x.target.value as WLSort)} aria-label="Sort">
                <option value="newest">Newest first</option>
                <option value="oldest">Oldest first</option>
                <option value="id">By {dname}</option>
                <option value="variant">By variant</option>
              </select>
            </div>
            {canEdit && picked.size > 0 && (
              <div className="row" style={{ gap: 8, padding: "0 16px 10px" }}>
                <span className="small">{picked.size} selected</span>
                <button className="btn btn-sm btn-danger" onClick={() => remove(Array.from(picked))}>
                  <Icon name="trash" size={14} /> Remove selected
                </button>
                <button className="btn btn-sm btn-ghost" onClick={() => setPicked(new Set())}>
                  Clear selection
                </button>
              </div>
            )}
            <table className="tbl tbl-compact">
              <thead>
                <tr>
                  {canEdit && (
                    <th style={{ width: 32 }}>
                      <input
                        type="checkbox"
                        aria-label="Select this page"
                        checked={allOnPage}
                        onChange={() => {
                          const next = new Set(picked);
                          pageIds.forEach((id) => (allOnPage ? next.delete(id) : next.add(id)));
                          setPicked(next);
                        }}
                      />
                    </th>
                  )}
                  <th>{dname}</th>
                  <th>Variant</th>
                  <th>Note</th>
                  <th>Added</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {paged.slice.map((w) => (
                  <tr key={w.unit_id}>
                    {canEdit && (
                      <td>
                        <input
                          type="checkbox"
                          aria-label={`Select ${w.unit_id}`}
                          checked={picked.has(w.unit_id)}
                          onChange={() => {
                            const next = new Set(picked);
                            if (next.has(w.unit_id)) next.delete(w.unit_id);
                            else next.add(w.unit_id);
                            setPicked(next);
                          }}
                        />
                      </td>
                    )}
                    <td className="mono">{w.unit_id}</td>
                    <td>{name(w.variant_id)}</td>
                    <td className="faint">{w.note}</td>
                    <td className="faint small nowrap">{fmtDate(w.created_at)}</td>
                    <td className="right">
                      {canEdit && (
                        <button className="icon-btn" aria-label="Remove" onClick={() => remove([w.unit_id])}>
                          <Icon name="trash" size={15} />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {found.length === 0 && <div className="small faint" style={{ padding: 16 }}>Nothing matches.</div>}
            <Pager page={paged.page} pages={paged.pages} total={paged.total} size={paged.size} onPage={paged.setPage} onSize={paged.setSize} noun={noun} />
          </>
        )}
        <ErrorBox error={error} />
      </section>
      {canEdit && !["stopped", "launched", "archived"].includes(e.status) && (
        <section className="card card-pad stack">
          <h2>Add test {noun}</h2>
          <Field
            label={`${dname}s`}
            hint={`This experiment splits by ${dname.toLowerCase()} (${e.layer_diversion}), so enter those ids — one per line, or separated by commas or spaces.`}
          >
            <textarea className="input input-mono" rows={4} value={units} onChange={(x) => setUnits(x.target.value)} />
          </Field>
          <Field label="Variant">
            <select className="input" value={variant} onChange={(x) => setVariant(Number(x.target.value))}>
              {e.variants?.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name || v.key}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Note">
            <input className="input" value={note} onChange={(x) => setNote(x.target.value)} placeholder="e.g. QA phone" />
          </Field>
          <div>
            <button className="btn btn-primary" disabled={!units.trim()} onClick={add}>
              Add
            </button>
          </div>
        </section>
      )}
    </div>
  );
}

function History({ id, version }: { id: number; version: string }) {
  const h = useAsync(() => api.history(id), [id, version]);
  if (h.loading && !h.data) return <Loading />;
  return (
    <section className="card card-pad">
      <ErrorBox error={h.error} />
      <div className="timeline">
        {h.data?.map((a) => (
          <div key={a.id} className="timeline-item">
            <div className="faint small">{fmtDateTime(a.created_at)}</div>
            <div>
              <div>
                <b>{a.actor}</b> {describe(a.action)}
                {a.to_status && a.from_status !== a.to_status && (
                  <span className="faint">
                    {" "}
                    ({a.from_status ? `${statusLabel[a.from_status as Status] ?? a.from_status} → ` : ""}
                    {statusLabel[a.to_status as Status] ?? a.to_status})
                  </span>
                )}
              </div>
              {detailText(a.detail) && <div className="small faint">{detailText(a.detail)}</div>}
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

function describe(action: string): string {
  const m: Record<string, string> = {
    create: "created the experiment",
    edit: "edited the setup",
    submit: "submitted it for review",
    withdraw: "withdrew the review",
    approve: "approved it",
    reject: "rejected it",
    start: "started it",
    pause: "paused it",
    resume: "resumed it",
    stop: "stopped it",
    launch: "launched a variant",
    archive: "archived it",
    traffic: "changed traffic",
    launch_rollout: "changed the launch rollout",
    rollout_cancel: "stopped a gradual rollout",
    invite_reviewers: "invited reviewers",
    whitelist_add: "added test users",
    whitelist_remove: "removed a test user",
    clone: "created it as a clone",
    seed: "created it (demo data)",
  };
  return m[action] ?? action;
}

function detailText(d: Record<string, unknown>): string {
  if (!d) return "";
  const parts: string[] = [];
  if (typeof d.note === "string" && d.note) parts.push(`“${d.note}”`);
  if (d.from && d.to && typeof d.from === "string") parts.push(`${d.from} → ${d.to}`);
  if (typeof d.from === "number") parts.push(`from experiment #${d.from}`);
  if (typeof d.variant === "string") parts.push(`variant ${d.variant}`);
  if (typeof d.review === "string") parts.push(d.review);
  if (typeof d.buckets === "number") parts.push(`${trafficPct(d.buckets)} traffic allocated`);
  if (Array.isArray(d.units)) parts.push((d.units as string[]).join(", "));
  if (typeof d.unit === "string") parts.push(d.unit);
  return parts.join(" · ");
}
