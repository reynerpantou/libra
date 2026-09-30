import { useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { ErrorBox, Icon, Loading, Tabs } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { useAsync } from "../lib/hooks";
import type { EventSummary, Scope } from "../lib/types";
import { Groups, Measures, Metrics } from "./BusinessDetail";
import { PlatformModal } from "./Businesses";

type Tab = "metrics" | "measures" | "groups" | "businesses";

// A platform's shared definitions: measures over the events of all its
// businesses, metrics, and groups (default groups apply to every
// experiment of every business under it).
export default function PlatformDetail() {
  const id = Number(useParams().pid);
  const [params, setParams] = useSearchParams();
  const tab = (params.get("tab") as Tab) || "metrics";
  const scope: Scope = useMemo(() => ({ kind: "platform", id }), [id]);
  const plat = useAsync(() => api.platform(id), [id]);
  const measures = useAsync(() => api.measures(scope), [scope]);
  const metrics = useAsync(() => api.metrics(scope), [scope]);
  const groups = useAsync(() => api.groups(scope), [scope]);
  // Events seen by any business under the platform (for the measure form).
  const events = useAsync(async (): Promise<EventSummary | null> => {
    const p = await api.platform(id);
    const all = await Promise.all(p.businesses.map((b) => api.eventSummary(b.id)));
    if (all.length === 0) return null;
    const by = new Map<string, EventSummary["events"][number]>();
    for (const s of all)
      for (const e of s.events) {
        const cur = by.get(e.name);
        if (!cur) by.set(e.name, { ...e, props: [...e.props] });
        else {
          cur.total += e.total;
          cur.units += e.units;
          cur.props = Array.from(new Set([...cur.props, ...e.props])).sort();
        }
      }
    return { since: all[0].since, events: Array.from(by.values()).sort((a, b) => b.total - a.total) };
  }, [id]);
  const isAdmin = useCan("admin");
  const [editing, setEditing] = useState(false);
  const p = plat.data;
  if (plat.loading && !p) return <div className="page"><Loading /></div>;
  if (!p) return <div className="page"><ErrorBox error={plat.error || "Not found"} /></div>;
  const reloadDefs = () => {
    measures.reload();
    metrics.reload();
    groups.reload();
  };
  return (
    <div className="page">
      <div className="crumbs">
        <Link to="/businesses">Businesses</Link> / {p.key}
      </div>
      <div className="page-head">
        <div>
          <div className="row">
            <h1>{p.name}</h1>
            <span className="chip">{p.key}</span>
            <span className="chip" title="Platform id">
              id {p.id}
            </span>
            <span className="badge">platform</span>
          </div>
          <p>{p.description}</p>
        </div>
        {isAdmin && (
          <button className="btn" onClick={() => setEditing(true)}>
            <Icon name="edit" /> Edit
          </button>
        )}
      </div>
      <div className="alert alert-info small">
        Definitions here are shared by <b>every business</b> of {p.name}: platform measures count events from all of them, and <b>default</b> groups are
        added to every experiment automatically.
      </div>
      <Tabs<Tab>
        tabs={[
          ["metrics", `Metrics (${metrics.data?.length ?? 0})`],
          ["measures", `Measures (${measures.data?.length ?? 0})`],
          ["groups", `Metric groups (${groups.data?.length ?? 0})`],
          ["businesses", `Businesses (${p.businesses.length})`],
        ]}
        value={tab}
        onChange={(t) => setParams({ tab: t }, { replace: true })}
      />
      {tab === "metrics" && <Metrics scope={scope} metrics={metrics.data ?? []} measures={measures.data ?? []} reload={reloadDefs} />}
      {tab === "measures" && <Measures scope={scope} measures={measures.data ?? []} events={events.data} reload={reloadDefs} />}
      {tab === "groups" && <Groups scope={scope} groups={groups.data ?? []} metrics={metrics.data ?? []} reload={groups.reload} />}
      {tab === "businesses" && (
        <section className="card">
          <div className="table-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Business</th>
                  <th>Key</th>
                  <th className="num">Id</th>
                  <th className="num">Metrics</th>
                  <th className="num">Experiments</th>
                </tr>
              </thead>
              <tbody>
                {p.businesses.map((b) => (
                  <tr key={b.id}>
                    <td>
                      <Link to={`/businesses/${b.id}`}>{b.name}</Link>
                    </td>
                    <td className="mono">{b.key}</td>
                    <td className="num mono">{b.id}</td>
                    <td className="num">{b.metrics}</td>
                    <td className="num">{b.experiments}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
      {editing && (
        <PlatformModal
          platform={p}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            plat.reload();
          }}
        />
      )}
    </div>
  );
}
