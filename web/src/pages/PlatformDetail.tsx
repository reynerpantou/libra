import { useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Empty, ErrorBox, Icon, Loading, Tabs } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { useAsync } from "../lib/hooks";
import type { Business, EventSummary, Platform, Scope } from "../lib/types";
import { Groups, Measures, Metrics } from "./BusinessDetail";
import { BusinessModal, BusinessTable, PlatformModal } from "./Businesses";

type Tab = "businesses" | "metrics" | "measures" | "groups";

// A platform's shared definitions: measures over the events of all its
// businesses, metrics, and groups (default groups apply to every
// experiment of every business under it).
export default function PlatformDetail() {
  const id = Number(useParams().pid);
  const [params, setParams] = useSearchParams();
  const tab = (params.get("tab") as Tab) || "businesses";
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
  const nav = useNavigate();
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
          <div className="row">
            <button className="btn" onClick={() => setEditing(true)}>
              <Icon name="edit" /> Edit
            </button>
            <button
              className="btn btn-danger"
              disabled={p.businesses.length > 0}
              title={p.businesses.length > 0 ? "Move or delete its businesses first" : "Delete this platform"}
              onClick={async () => {
                if (!confirm(`Delete platform ${p.name} and its shared metrics?`)) return;
                try {
                  await api.deletePlatform(p.id);
                  nav("/businesses");
                } catch (e) {
                  alert(e instanceof Error ? e.message : String(e));
                }
              }}
            >
              <Icon name="trash" /> Delete
            </button>
          </div>
        )}
      </div>
      <div className="alert alert-info small">
        Definitions here are shared by <b>every business</b> of {p.name}: platform measures count events from all of them, and <b>default</b> groups are
        added to every experiment automatically.
      </div>
      <Tabs<Tab>
        tabs={[
          ["businesses", `Businesses (${p.businesses.length})`],
          ["groups", `Metric groups (${groups.data?.length ?? 0})`],
          ["metrics", `Shared metrics (${metrics.data?.length ?? 0})`],
          ["measures", `Shared measures (${measures.data?.length ?? 0})`],
        ]}
        value={tab}
        onChange={(t) => setParams({ tab: t }, { replace: true })}
      />
      {tab === "metrics" && <Metrics scope={scope} metrics={metrics.data ?? []} measures={measures.data ?? []} reload={reloadDefs} />}
      {tab === "measures" && <Measures scope={scope} measures={measures.data ?? []} events={events.data} reload={reloadDefs} />}
      {tab === "groups" && <Groups scope={scope} groups={groups.data ?? []} metrics={metrics.data ?? []} reload={groups.reload} />}
      {tab === "businesses" && <ManageBusinesses platform={p} reload={plat.reload} />}
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

// ManageBusinesses lists the platform's businesses with search, and lets
// admins add, edit, move and delete them.
function ManageBusinesses({ platform, reload }: { platform: Platform; reload: () => void }) {
  const isAdmin = useCan("admin");
  const nav = useNavigate();
  const all = useAsync(() => api.platforms(), []);
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<Business | "new" | null>(null);
  const [error, setError] = useState("");
  const query = q.trim().toLowerCase();
  const list = platform.businesses.filter(
    (b) => !query || [b.name, b.key, String(b.id), b.description].some((x) => x.toLowerCase().includes(query))
  );
  return (
    <section className="card">
      <div className="card-head">
        <div className="row" style={{ gap: 10, flex: 1 }}>
          <input className="input" style={{ maxWidth: 360 }} placeholder="Search businesses…" value={q} onChange={(e) => setQ(e.target.value)} />
          <span className="small faint">
            {list.length} of {platform.businesses.length}
          </span>
        </div>
        {isAdmin && (
          <button className="btn btn-primary btn-sm" onClick={() => setEditing("new")}>
            <Icon name="plus" /> New business
          </button>
        )}
      </div>
      <ErrorBox error={error} />
      {list.length === 0 ? (
        <Empty title={platform.businesses.length ? "Nothing matches" : "No businesses yet"}>
          {!platform.businesses.length && <p>Add the businesses of {platform.name} — e.g. Search, Ads, Recommendation.</p>}
        </Empty>
      ) : (
        <BusinessTable
          businesses={list}
          actions={
            isAdmin
              ? (b) => (
                  <>
                    <button className="icon-btn" aria-label="Edit" title="Edit or move to another platform" onClick={() => setEditing(b)}>
                      <Icon name="edit" size={15} />
                    </button>
                    <button
                      className="icon-btn"
                      aria-label="Delete"
                      disabled={b.experiments > 0}
                      title={b.experiments > 0 ? "Businesses with experiments can't be deleted" : "Delete"}
                      onClick={async () => {
                        if (!confirm(`Delete ${b.name} with its measures, metrics, groups and events?`)) return;
                        setError("");
                        try {
                          await api.deleteBusiness(b.id);
                          reload();
                        } catch (e) {
                          setError(e instanceof Error ? e.message : String(e));
                        }
                      }}
                    >
                      <Icon name="trash" size={15} />
                    </button>
                  </>
                )
              : undefined
          }
        />
      )}
      {editing && (
        <BusinessModal
          platforms={all.data ?? [platform]}
          platformId={platform.id}
          business={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={(b) => {
            setEditing(null);
            if (editing === "new") nav(`/businesses/${b.id}`);
            else reload();
          }}
        />
      )}
    </section>
  );
}
