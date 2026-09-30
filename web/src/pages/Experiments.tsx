import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Empty, ErrorBox, Icon, Loading, StatusBadge } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { ago, fmtInt, trafficPct } from "../lib/format";
import { useAsync, useDebounced } from "../lib/hooks";

const STATUS_FILTERS: [string, string][] = [
  ["", "All open"],
  ["active,paused", "Running"],
  ["draft,in_review,approved,rejected", "Not started"],
  ["stopped,launched", "Finished"],
  ["archived", "Archived"],
];

export default function Experiments() {
  const [params, setParams] = useSearchParams();
  const platform = params.get("platform") ?? "";
  const business = params.get("business") ?? "";
  const status = params.get("status") ?? "";
  const [q, setQ] = useState(params.get("q") ?? "");
  const dq = useDebounced(q, 250);
  const canEdit = useCan("editor");
  const nav = useNavigate();
  const businesses = useAsync(() => api.businesses(), []);
  const list = useAsync(() => api.experiments({ platform, business, status, q: dq }), [platform, business, status, dq]);
  const platforms = Array.from(new Map((businesses.data ?? []).map((b) => [b.platform_key, b.platform_name])).entries());

  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
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
            <select
              className="input"
              style={{ width: 180 }}
              value={platform}
              aria-label="Platform"
              onChange={(e) => {
                const next = new URLSearchParams(params);
                if (e.target.value) next.set("platform", e.target.value);
                else next.delete("platform");
                next.delete("business");
                setParams(next, { replace: true });
              }}
            >
              <option value="">All platforms</option>
              {platforms.map(([key, name]) => (
                <option key={key} value={key}>
                  {name}
                </option>
              ))}
            </select>
            <select className="input" style={{ width: 180 }} value={business} aria-label="Business" onChange={(e) => set("business", e.target.value)}>
              <option value="">All businesses</option>
              {(businesses.data ?? [])
                .filter((b) => !platform || b.platform_key === platform)
                .map((b) => (
                  <option key={b.id} value={String(b.id)}>
                    {platform ? b.name : `${b.name} · ${b.platform_name}`}
                  </option>
                ))}
            </select>
          </div>
          <input className="input" style={{ width: 240 }} placeholder="Search name or hypothesis" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <ErrorBox error={list.error} />
        {list.loading && !list.data ? (
          <Loading />
        ) : list.data && list.data.length === 0 ? (
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
                {list.data?.map((e) => (
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
                      {e.business_name} <span className="faint small">· {e.platform_name}</span>
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
      </div>
    </div>
  );
}
