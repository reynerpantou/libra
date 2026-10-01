import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Pager } from "../components/Pager";
import { ListFilters, filterArgs } from "../components/ListFilters";
import { Empty, ErrorBox, Icon, Loading, StatusBadge } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { ago } from "../lib/format";
import { useAsync, useDebounced } from "../lib/hooks";
import { algorithmName } from "../lib/tuning";

const statuses: [string, string][] = [
  ["", "All open"],
  ["active,paused", "Running"],
  ["draft,in_review,approved,rejected", "Not started"],
  ["stopped,launched", "Finished"],
  ["archived", "Archived"],
];

// AB Tuning: studies that search numeric parameters round by round.
export default function Tuning() {
  const [sp, setSp] = useSearchParams();
  const canEdit = useCan("editor");
  const nav = useNavigate();
  const status = sp.get("status") ?? "";
  const [q, setQ] = useState(sp.get("q") ?? "");
  const query = useDebounced(q, 250);
  const page = Math.max(1, Number(sp.get("page")) || 1);
  const size = Number(sp.get("size")) || 25;
  const f = filterArgs(sp);
  const list = useAsync(() => api.tunings({ ...f, status, q: query, page, size }), [JSON.stringify(f), status, query, page, size]);
  const set = (k: string, v: string) => {
    const n = new URLSearchParams(sp);
    if (v) n.set(k, v);
    else n.delete(k);
    if (k !== "page") n.delete("page");
    setSp(n, { replace: true });
  };
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>AB Tuning</h1>
          <p>
            Find the best values for numeric parameters. v0 keeps today's values; every round, treatment arms try candidate points chosen from the
            earlier rounds' results — random, quasi-random, Bayesian or constrained search.
          </p>
        </div>
        {canEdit && (
          <Link className="btn btn-primary" to="/tuning/new">
            <Icon name="plus" /> New tuning study
          </Link>
        )}
      </div>
      <div className="card">
        <div className="card-head">
          <div className="seg">
            {statuses.map(([v, l]) => (
              <button key={l} className={status === v ? "on" : ""} onClick={() => set("status", v)}>
                {l}
              </button>
            ))}
          </div>
          <input className="input" style={{ width: 260 }} placeholder="Search name or id" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <div style={{ padding: "0 16px 12px" }}>
          <ListFilters params={sp} setParams={setSp} />
        </div>
        <ErrorBox error={list.error} />
        {list.loading && !list.data ? (
          <Loading />
        ) : list.data?.items.length === 0 ? (
          <Empty title="No tuning studies here">
            {canEdit ? (
              <p>
                <Link to="/tuning/new">Create one</Link>, or run <code>libra demo-tuning</code> on the server for a sample study.
              </p>
            ) : (
              <p>Nothing matches these filters.</p>
            )}
          </Empty>
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Study</th>
                  <th>Status</th>
                  <th>Algorithm</th>
                  <th>Progress</th>
                  <th>Objective</th>
                  <th className="num">Best lift so far</th>
                  <th>Owner</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {list.data?.items.map((t) => {
                  const done = t.finished_at ? t.max_rounds : t.status === "active" || t.status === "paused" ? t.round - 1 : 0;
                  return (
                    <tr key={t.id} className="clickable" onClick={() => nav(`/tuning/${t.id}`)}>
                      <td>
                        <div style={{ fontWeight: 600 }}>{t.name}</div>
                        <div className="faint small">
                          #{t.id} · {t.params.map((p) => p.split(".").pop()).join(", ")} · {t.platform_name}
                        </div>
                      </td>
                      <td>
                        <StatusBadge status={t.status} />
                      </td>
                      <td className="small">{algorithmName(t.algorithm)}</td>
                      <td>
                        <span className="round-pill small">
                          <span className="round-bar">
                            <span style={{ width: `${(100 * done) / t.max_rounds}%` }} />
                          </span>
                          {t.finished_at ? `done · ${t.max_rounds} rounds` : t.status === "active" || t.status === "paused" ? `round ${t.round} of ${t.max_rounds}` : `${t.max_rounds} rounds planned`}
                        </span>
                      </td>
                      <td className="small">{t.objective}</td>
                      <td className="num">
                        {t.best_lift === null ? (
                          <span className="faint">—</span>
                        ) : (
                          <span className={t.best_lift > 0 ? "tone-good" : "tone-bad"}>
                            {t.best_lift > 0 ? "+" : "−"}
                            {Math.abs(t.best_lift * 100).toFixed(1)}%
                          </span>
                        )}
                      </td>
                      <td>{t.owner_name || "—"}</td>
                      <td className="faint nowrap">{ago(t.updated_at)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {list.data && (
          <Pager
            page={page}
            pages={Math.max(1, Math.ceil(list.data.total / size))}
            total={list.data.total}
            size={size}
            onPage={(p) => set("page", String(p))}
            onSize={(s) => set("size", String(s))}
            noun="studies"
          />
        )}
      </div>
    </div>
  );
}
