import { Field } from "./ui";

// Values are ISO strings (or ""); inputs are local date-times.
export const toLocalInput = (iso: string | null | undefined) => {
  if (!iso) return "";
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
};
export const fromLocalInput = (v: string) => (v ? new Date(v).toISOString() : "");

const days = (a: string, b: string) => Math.round((new Date(b).getTime() - new Date(a).getTime()) / 86400000);

export function fmtWhen(iso: string) {
  return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

// relative says "in 3 days" / "2 hours ago".
export function relative(iso: string) {
  const ms = new Date(iso).getTime() - Date.now();
  const abs = Math.abs(ms);
  const unit = abs >= 86400000 ? ["day", 86400000] : abs >= 3600000 ? ["hour", 3600000] : ["minute", 60000];
  const n = Math.max(1, Math.round(abs / (unit[1] as number)));
  const s = `${n} ${unit[0]}${n === 1 ? "" : "s"}`;
  return ms >= 0 ? `in ${s}` : `${s} ago`;
}

// ScheduleFields: a planned start (a reminder to start — starting stays a
// reviewed decision) and an end date (stopped then, unless extended).
// Tuning studies pass their length instead of an end date.
export function ScheduleFields({
  start,
  end,
  onStart,
  onEnd,
  startLocked,
  studyDays,
}: {
  start: string;
  end?: string;
  onStart: (v: string) => void;
  onEnd?: (v: string) => void;
  startLocked?: boolean;
  studyDays?: number;
}) {
  const from = start || new Date().toISOString();
  const preset = (n: number) => onEnd?.(new Date(new Date(from).getTime() + n * 86400000).toISOString());
  return (
    <div className="grid-2">
      <Field label="Planned start" hint={startLocked ? "It has started." : "You'll get a notification when it's time to start. Starting stays a click (after review)."}>
        <input className="input" type="datetime-local" disabled={startLocked} value={toLocalInput(start)} onChange={(e) => onStart(fromLocalInput(e.target.value))} />
      </Field>
      {studyDays !== undefined ? (
        <Field group label="Expected end">
          <div className="input" style={{ display: "flex", alignItems: "center", background: "var(--surface-2)" }}>
            {fmtWhen(new Date(new Date(from).getTime() + studyDays * 86400000).toISOString())}
          </div>
          <div className="small faint" style={{ marginTop: 4 }}>
            Rounds × round length = {studyDays} day{studyDays === 1 ? "" : "s"} after it starts. Extend later by adding rounds.
          </div>
        </Field>
      ) : (
        <Field
          group
          label="End date"
          hint={
            end
              ? `Runs ${days(from, end)} day${days(from, end) === 1 ? "" : "s"}${start ? "" : " from now"}. It stops then unless extended; you'll be notified a day before.`
              : "Optional. Without one it runs until someone stops it."
          }
        >
          <div className="row" style={{ gap: 6 }}>
            <input className="input" style={{ flex: 1 }} type="datetime-local" value={toLocalInput(end)} onChange={(e) => onEnd?.(fromLocalInput(e.target.value))} />
            {[7, 14, 28].map((n) => (
              <button key={n} type="button" className="btn btn-sm" onClick={() => preset(n)}>
                {n}d
              </button>
            ))}
            {end && (
              <button type="button" className="btn btn-sm btn-ghost" onClick={() => onEnd?.("")}>
                None
              </button>
            )}
          </div>
        </Field>
      )}
    </div>
  );
}
