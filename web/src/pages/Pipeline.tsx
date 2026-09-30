import { useState } from "react";
import { ErrorBox, Icon, Loading, Stat } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { ago, fmtDateTime, fmtInt } from "../lib/format";
import { useAsync } from "../lib/hooks";

export default function Pipeline() {
  const st = useAsync(() => api.pipeline(), []);
  const canRun = useCan("editor");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const s = st.data;
  const last = s?.runs.find((r) => r.status === "succeeded");
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Data pipeline</h1>
          <p>
            Services push raw <b>exposures</b> (who saw which variant) and business <b>events</b> (searches, clicks, orders…). Each run rolls new rows into
            per-unit daily measures that reports read. Runs are incremental and every {s ? Math.round(s.interval_seconds / 60) : "–"} minutes.
          </p>
        </div>
        <div className="row">
          <button className="btn" onClick={st.reload}>
            <Icon name="refresh" /> Refresh
          </button>
          {canRun && (
            <button
              className="btn btn-primary"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                setError("");
                try {
                  await api.runPipeline();
                } catch (e) {
                  setError(e instanceof Error ? e.message : String(e));
                } finally {
                  setBusy(false);
                  st.reload();
                }
              }}
            >
              {busy ? "Running…" : "Run now"}
            </button>
          )}
        </div>
      </div>
      <ErrorBox error={error || st.error} />
      {!s ? (
        <Loading />
      ) : (
        <div className="stack">
          <div className="grid-4">
            <Stat label="Last successful run" value={<span style={{ fontSize: 18 }}>{ago(last?.finished_at)}</span>} sub={fmtDateTime(last?.finished_at)} />
            <Stat label="Waiting to process" value={fmtInt(s.exposures_pending + s.events_pending)} sub={`${fmtInt(s.exposures_pending)} exposures · ${fmtInt(s.events_pending)} events`} />
            <Stat label="Raw rows received" value={fmtInt(s.exposures_total + s.events_total)} sub={`${fmtInt(s.exposures_total)} exposures · ${fmtInt(s.events_total)} events`} />
            <Stat
              label="Computed"
              value={fmtInt(s.measure_rows)}
              sub={`unit-day measure rows · ${fmtInt(s.assignments)} assignments${s.measures_pending_backfill ? ` · ${s.measures_pending_backfill} measures recomputing` : ""}`}
            />
          </div>
          {(s.exposures_dropped ?? 0) > 0 && (
            <div className="alert alert-warn">{fmtInt(s.exposures_dropped)} exposures were dropped since this server started because the write buffer was full.</div>
          )}
          <section className="card">
            <div className="card-head">
              <h2>Recent runs</h2>
            </div>
            <div className="table-wrap">
              <table className="tbl tbl-compact">
                <thead>
                  <tr>
                    <th>Started</th>
                    <th>Trigger</th>
                    <th>Status</th>
                    <th className="num">Exposures</th>
                    <th className="num">Events</th>
                    <th className="num">Rows written</th>
                    <th className="num">Backfills</th>
                    <th className="num">Duration</th>
                  </tr>
                </thead>
                <tbody>
                  {s.runs.map((r) => (
                    <tr key={r.id}>
                      <td className="nowrap">{fmtDateTime(r.started_at)}</td>
                      <td className="faint">{r.trigger}</td>
                      <td>
                        <span className={`badge ${r.status === "succeeded" ? "b-good" : r.status === "failed" ? "b-bad" : "b-warn"}`}>{r.status}</span>
                        {r.error && <div className="small" style={{ color: "var(--bad)" }}>{r.error}</div>}
                      </td>
                      <td className="num">{fmtInt(r.stats?.exposures)}</td>
                      <td className="num">{fmtInt(r.stats?.events)}</td>
                      <td className="num">{fmtInt(r.stats?.rows_written)}</td>
                      <td className="num">{fmtInt(r.stats?.measures_backfilled)}</td>
                      <td className="num">{r.stats ? `${(r.stats.seconds ?? 0).toFixed(1)}s` : "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
