import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Empty, ErrorBox, Icon, Loading, StatusBadge } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { ago, fmtInt, trafficPct } from "../lib/format";
import { useAsync, useDebounced } from "../lib/hooks";
import { Pager } from "../components/Pager";
import { ListFilters, filterArgs } from "../components/ListFilters";

const STATUS_FILTERS: [string, string][] = [
  ["", "All open"],
  ["active,paused", "Running"],
  ["draft,in_review,approved,rejected", "Not started"],
  ["stopped,launched", "Finished"],
  ["archived", "Archived"],
];

export default function Experiments() {
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const [q, setQ] = useState(params.get("q") ?? "");
  const dq = useDebounced(q, 250);
  const canEdit = useCan("editor");
  const nav = useNavigate();
  const page = Math.max(1, Number(params.get("page")) || 1);
  const size = Number(params.get("size")) || 25;
  const f = filterArgs(params);
  const list = useAsync(() => api.experiments({ ...f, status, q: dq, page, size }), [JSON.stringify(f), status, dq, page, size]);
  const items = list.data?.items;
  // A new search starts at page 1.
  const firstSearch = useRef(true);
  useEffect(() => {
    if (firstSearch.current) {
      firstSearch.current = false;
      return;
    }
    if (params.get("page")) {
      const next = new URLSearchParams(params);
      next.delete("page");
      setParams(next, { replace: true });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dq]);

  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    if (k !== "page") next.delete("page"); // a new filter starts at page 1
    setParams(next, { replace: true });
  };

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Experiments</h1>
          <p>Every A/B test across your businesses. Open one to manage its traffic or read its report.</p>
        </div>
        {canEdit && (
          <Link className="btn btn-primary" to="/experiments/new">
            <Icon name="plus" /> New experiment
          </Link>
        )}
      </div>
      <div className="card">
        <div className="card-head">
          <div className="row">
            <div className="seg">
              {STATUS_FILTERS.map(([v, label]) => (
                <button key={label} className={status === v ? "on" : ""} onClick={() => set("status", v)}>
                  {label}
                </button>
              ))}
            </div>
          </div>
          <input className="input" style={{ width: 240 }} placeholder="Search name, hypothesis or id" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <div style={{ padding: "0 16px 12px" }}>
          <ListFilters params={params} setParams={setParams} />

        </div>
        <ErrorBox error={list.error} />
        {list.loading && !list.data ? (
          <Loading />
        ) : items && items.length === 0 ? (
          <Empty title="No experiments here">
            {canEdit ? (
              <p>
                <Link to="/experiments/new">Create one</Link>, or run <code>libra demo</code> on the server for sample data.
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
                  <th>Experiment</th>
                  <th>Status</th>
                  <th>Business</th>
                  <th>Layer</th>
                  <th className="num">Traffic</th>
                  <th className="num">Units</th>
                  <th>Owner</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {items?.map((e) => (
                  <tr key={e.id} className="clickable" onClick={() => nav(`/experiments/${e.id}`)}>
                    <td>
                      <div style={{ fontWeight: 600 }}>{e.name}</div>
                      <div className="faint small" style={{ maxWidth: 420, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                        #{e.id} · {e.hypothesis || "No hypothesis"}
                      </div>
                    </td>
                    <td>
                      <StatusBadge status={e.status} />
                    </td>
                    <td>
                      {(e.business_names?.length ? e.business_names : [e.business_name]).join(", ")} <span className="faint small">· {e.platform_name}</span>
                    </td>
                    <td className="faint">{e.layer_auto ? "dedicated" : e.layer_name}</td>
                    <td className="num">{e.status === "active" || e.status === "paused" ? trafficPct(e.traffic_held) : <span className="faint">{trafficPct(e.traffic_target)}</span>}</td>
                    <td className="num">{fmtInt(e.units)}</td>
                    <td>{e.owner_name || "—"}</td>
                    <td className="faint nowrap">{ago(e.updated_at)}</td>
                  </tr>
                ))}
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
            onSize={(n) => {
              const next = new URLSearchParams(params);
              next.set("size", String(n));
              next.delete("page");
              setParams(next, { replace: true });
            }}
            noun="experiments"
          />
        )}
      </div>
    </div>
  );
}
