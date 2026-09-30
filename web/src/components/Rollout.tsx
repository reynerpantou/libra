import { useState } from "react";
import { api } from "../lib/api";
import { fmtDateTime, trafficPct } from "../lib/format";
import type { Gradual, Rollout } from "../lib/types";
import { Segmented } from "./ui";

const STEPS = [10, 50, 100, 200, 250, 500];
const INTERVALS: [number, string][] = [
  [15, "15 minutes"],
  [30, "30 minutes"],
  [60, "1 hour"],
  [120, "2 hours"],
  [360, "6 hours"],
  [720, "12 hours"],
  [1440, "1 day"],
];

export const defaultGradual = (): Gradual => ({ step: 100, interval_minutes: 60 });

export function duration(minutes: number): string {
  if (minutes < 60) return `${minutes} min`;
  if (minutes < 60 * 48) return `${+(minutes / 60).toFixed(1)} h`;
  return `${+(minutes / 1440).toFixed(1)} days`;
}

// The value a gradual change applies right away (mirrors the server).
export function firstValue(from: number, target: number, g: Gradual): number {
  return Math.min(g.start && g.start > from ? g.start : from + g.step, target);
}

// The steps a gradual change takes from `from` to `target`.
export function schedule(from: number, target: number, g: Gradual): number[] {
  const out: number[] = [];
  let v = firstValue(from, target, g);
  out.push(v);
  while (v < target && out.length < 1000) {
    v = Math.min(v + g.step, target);
    out.push(v);
  }
  return out;
}

// RolloutChoice picks an immediate change or a gradual one: start (optional),
// step and interval, with the resulting schedule spelled out.
export function RolloutChoice({
  from,
  target,
  value,
  onChange,
  withStart,
  noun = "traffic",
}: {
  from: number;
  target: number;
  value: Gradual | null;
  onChange: (g: Gradual | null) => void;
  withStart?: boolean;
  noun?: string;
}) {
  const up = target > from;
  const steps = value ? schedule(from, target, value) : [];
  return (
    <div className="stack-sm">
      <Segmented<"now" | "gradual">
        options={[
          ["now", "Immediately"],
          ["gradual", "Gradually"],
        ]}
        value={value && up ? "gradual" : "now"}
        onChange={(m) => onChange(m === "gradual" ? value ?? defaultGradual() : null)}
      />
      {value && !up && <div className="small faint">Only increases can be gradual; this change applies immediately.</div>}
      {value && up && (
        <>
          <div className="row" style={{ gap: 8 }}>
            {withStart && (
              <label className="small">
                Start at{" "}
                <select className="input" style={{ width: 118, height: 30 }} value={value.start ?? 0} onChange={(e) => onChange({ ...value, start: Number(e.target.value) || undefined })}>
                  <option value={0}>one step</option>
                  {[10, 50, 100, 250, 500].filter((v) => v < target).map((v) => (
                    <option key={v} value={v}>
                      {trafficPct(v)}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <label className="small">
              then add{" "}
              <select className="input" style={{ width: 80, height: 30 }} value={value.step} onChange={(e) => onChange({ ...value, step: Number(e.target.value) })}>
                {STEPS.map((v) => (
                  <option key={v} value={v}>
                    {trafficPct(v)}
                  </option>
                ))}
              </select>
            </label>
            <label className="small">
              every{" "}
              <select
                className="input"
                style={{ width: 120, height: 30 }}
                value={value.interval_minutes}
                onChange={(e) => onChange({ ...value, interval_minutes: Number(e.target.value) })}
              >
                {INTERVALS.map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <div className="small muted">
            {noun} goes to <b>{trafficPct(steps[0])}</b> now
            {steps.length > 1 && (
              <>
                , then {steps.slice(1, 6).map(trafficPct).join(" → ")}
                {steps.length > 6 ? " → …" : ""} — reaching <b>{trafficPct(target)}</b> in about {duration((steps.length - 1) * value.interval_minutes)} (
                {steps.length - 1} scheduled steps). Everyone already in stays in.
              </>
            )}
          </div>
        </>
      )}
    </div>
  );
}

const statusClass: Record<Rollout["status"], string> = { active: "b-accent", done: "b-good", cancelled: "", blocked: "b-warn" };

// RolloutStatus shows the active plan (with progress and cancel) and recent
// ones.
export function RolloutStatus({
  plans,
  current,
  kind,
  canEdit,
  onCancelled,
}: {
  plans: Rollout[];
  current: number;
  kind: Rollout["kind"];
  canEdit: boolean;
  onCancelled: () => void;
}) {
  const [show, setShow] = useState(false);
  const mine = plans.filter((p) => p.kind === kind);
  const live = mine.find((p) => p.status === "active" || p.status === "blocked");
  if (mine.length === 0) return null;
  return (
    <div className="stack-sm">
      {live && (
        <div className={`alert ${live.status === "blocked" ? "alert-warn" : "alert-info"} small`}>
          <div className="row-between">
            <b>
              {live.status === "blocked" ? "Gradual rollout blocked" : "Gradual rollout in progress"}: {trafficPct(current)} → {trafficPct(live.target)}
            </b>
            {canEdit && (
              <button
                className="btn btn-sm"
                onClick={async () => {
                  if (!confirm("Stop the gradual rollout here? The current value stays.")) return;
                  await api.cancelRollout(live.experiment_id, live.id);
                  onCancelled();
                }}
              >
                Stop ramp
              </button>
            )}
          </div>
          <div className="rollout-bar" style={{ margin: "8px 0 4px" }}>
            <div style={{ width: `${(current / 10).toFixed(1)}%` }} />
            <span style={{ left: `${live.target / 10}%` }} />
          </div>
          <div>
            +{trafficPct(live.step)} every {duration(live.interval_secs / 60)}
            {live.status === "active" ? <> · next step {fmtDateTime(live.next_at)}</> : <> · {live.note}</>} · set by {live.created_by}
          </div>
        </div>
      )}
      {mine.length > (live ? 1 : 0) && (
        <button className="btn btn-ghost btn-sm" style={{ alignSelf: "flex-start" }} onClick={() => setShow(!show)}>
          {show ? "Hide" : "Show"} past rollouts ({mine.length - (live ? 1 : 0)})
        </button>
      )}
      {show && (
        <table className="tbl tbl-compact">
          <tbody>
            {mine
              .filter((p) => p !== live)
              .map((p) => (
                <tr key={p.id}>
                  <td className="nowrap small">{fmtDateTime(p.created_at)}</td>
                  <td className="small">
                    {trafficPct(p.start_value)} → {trafficPct(p.target)}, +{trafficPct(p.step)} / {duration(p.interval_secs / 60)}
                  </td>
                  <td>
                    <span className={`badge ${statusClass[p.status]}`}>{p.status}</span> <span className="small faint">{p.note}</span>
                  </td>
                </tr>
              ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
