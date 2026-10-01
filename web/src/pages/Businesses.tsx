import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Empty, ErrorBox, Field, Icon, Loading, Modal, Segmented } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { useAsync } from "../lib/hooks";
import type { Business, Platform } from "../lib/types";

type View = "cards" | "list";

function useStored<T>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      const raw = localStorage.getItem(key);
      return raw ? (JSON.parse(raw) as T) : initial;
    } catch {
      return initial;
    }
  });
  const set = (x: T) => {
    setV(x);
    try {
      localStorage.setItem(key, JSON.stringify(x));
    } catch {
      /* private mode */
    }
  };
  return [v, set];
}

const matches = (q: string, ...xs: (string | number)[]) => xs.some((x) => String(x).toLowerCase().includes(q));

export default function Businesses() {
  const list = useAsync(() => api.platforms(), []);
  const isAdmin = useCan("admin");
  const nav = useNavigate();
  const [creating, setCreating] = useState<"platform" | number | null>(null);
  const [q, setQ] = useState("");
  const [platform, setPlatform] = useState("");
  const [only, setOnly] = useState<"" | "experiments" | "metrics" | "empty">("");
  const [sort, setSort] = useStored<"name" | "experiments" | "recent">("libra-biz-sort", "name");
  const [view, setView] = useStored<View>("libra-biz-view", "cards");
  // Platforms start collapsed (a platform can hold dozens of businesses);
  // the ones you open are remembered.
  const [expanded, setExpanded] = useStored<number[]>("libra-biz-expanded", []);
  const platforms = list.data ?? [];
  const query = q.trim().toLowerCase();
  const filtering = !!query || !!only;

  // Filter businesses inside each platform; a platform matching the search
  // shows all of its businesses.
  const shown = useMemo(
    () =>
      platforms
        .filter((p) => !platform || String(p.id) === platform)
        .map((p) => {
          const platformHit = !!query && matches(query, p.name, p.key, p.id);
          const bs = p.businesses
            .filter((b) => !query || platformHit || matches(query, b.name, b.key, b.id, b.description))
            .filter((b) => (only === "experiments" ? b.experiments > 0 : only === "metrics" ? b.metrics > 0 : only === "empty" ? b.metrics === 0 && b.experiments === 0 : true))
            .sort((x, y) =>
              sort === "experiments" ? y.experiments - x.experiments || x.name.localeCompare(y.name) : sort === "recent" ? y.created_at.localeCompare(x.created_at) : x.name.localeCompare(y.name)
            );
          return { p, bs, platformHit };
        })
        .filter(({ bs, platformHit }) => !filtering || bs.length > 0 || platformHit),
    [platforms, platform, query, only, sort, filtering]
  );
  const total = platforms.reduce((n, p) => n + p.businesses.length, 0);
  const shownCount = shown.reduce((n, x) => n + x.bs.length, 0);
  const isCollapsed = (id: number) => !filtering && !expanded.includes(id);
  const toggle = (id: number) => setExpanded(expanded.includes(id) ? expanded.filter((x) => x !== id) : [...expanded, id]);

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Businesses & metrics</h1>
          <p>
            A <b>platform</b> (an app or company) holds <b>businesses</b> (search, feed, ads…). Each business sends its own events and defines
            metrics as formulas, like <code>search_gmv / users</code>. Every platform has a <b>default metric group</b> that's reported for every
            experiment of its businesses.
          </p>
        </div>
        {isAdmin && (
          <div className="row">
            <button className="btn" onClick={() => setCreating("platform")}>
              <Icon name="plus" /> New platform
            </button>
            {platforms.length > 0 && (
              <button className="btn btn-primary" onClick={() => setCreating(platform ? Number(platform) : platforms[0].id)}>
                <Icon name="plus" /> New business
              </button>
            )}
          </div>
        )}
      </div>
      {platforms.length > 0 && (
        <div className="card card-pad">
          <div className="row" style={{ gap: 10 }}>
            <input
              className="input"
              style={{ flex: 1, minWidth: 220 }}
              placeholder="Search businesses or platforms by name, key or id…"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
            <select className="input" style={{ width: 180 }} value={platform} onChange={(e) => setPlatform(e.target.value)}>
              <option value="">All platforms</option>
              {platforms.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
            <select className="input" style={{ width: 170 }} value={only} onChange={(e) => setOnly(e.target.value as typeof only)}>
              <option value="">Any business</option>
              <option value="experiments">With experiments</option>
              <option value="metrics">With metrics</option>
              <option value="empty">Not set up yet</option>
            </select>
            <select className="input" style={{ width: 150 }} value={sort} onChange={(e) => setSort(e.target.value as typeof sort)}>
              <option value="name">Sort: name</option>
              <option value="experiments">Sort: experiments</option>
              <option value="recent">Sort: newest</option>
            </select>
            <Segmented<View>
              options={[
                ["cards", "Cards"],
                ["list", "List"],
              ]}
              value={view}
              onChange={setView}
            />
          </div>
          <div className="row small faint" style={{ marginTop: 8 }}>
            <span>
              {filtering ? `${shownCount} of ${total}` : total} businesses in {shown.length} platform{shown.length === 1 ? "" : "s"}
            </span>
            <span className="spacer" />
            <button className="btn btn-ghost btn-sm" onClick={() => setExpanded(platforms.map((p) => p.id))} disabled={filtering}>
              Expand all
            </button>
            <button className="btn btn-ghost btn-sm" onClick={() => setExpanded([])} disabled={filtering}>
              Collapse all
            </button>
          </div>
        </div>
      )}
      <ErrorBox error={list.error} />
      {list.loading && !list.data ? (
        <Loading />
      ) : platforms.length === 0 ? (
        <div className="card">
          <Empty title="No platforms yet">
            <p>
              Create a platform, then its businesses — or run <code>libra demo</code> on the server to load a sample platform.
            </p>
          </Empty>
        </div>
      ) : shown.length === 0 ? (
        <div className="card">
          <Empty title="Nothing matches">
            <p>Try another search or filter.</p>
          </Empty>
        </div>
      ) : (
        <div className="stack">
          {shown.map(({ p, bs }) => {
            const closed = isCollapsed(p.id);
            return (
              <section key={p.id} className="card">
                <div
                  className="card-head"
                  style={{ ...(closed ? { borderBottom: 0 } : {}), cursor: filtering ? undefined : "pointer" }}
                  onClick={(e) => !filtering && !(e.target as HTMLElement).closest("a, button") && toggle(p.id)}
                >
                  <div className="row" style={{ gap: 8, minWidth: 0 }}>
                    <button className="icon-btn" aria-label={closed ? "Expand" : "Collapse"} onClick={() => toggle(p.id)} disabled={filtering}>
                      <span style={{ display: "inline-block", width: 14 }}>{closed ? "▸" : "▾"}</span>
                    </button>
                    <div style={{ minWidth: 0 }}>
                      <div className="row">
                        <h2>
                          <Link to={`/platforms/${p.id}`}>{p.name}</Link>
                        </h2>
                        <span className="chip">{p.key}</span>
                        <span className="chip" title="Platform id">
                          id {p.id}
                        </span>
                        <span className="badge">
                          {filtering && bs.length !== p.businesses.length ? `${bs.length} of ${p.businesses.length}` : p.businesses.length} businesses
                        </span>
                      </div>
                      <p className="small faint">
                        {p.description || "No description"} · shared: {p.metrics} metrics, {p.measures} measures, {p.metric_groups} groups
                      </p>
                    </div>
                  </div>
                  <div className="row">
                    <Link className="btn btn-sm" to={`/platforms/${p.id}`}>
                      Manage platform
                    </Link>
                    {isAdmin && (
                      <button className="btn btn-sm" onClick={() => setCreating(p.id)}>
                        <Icon name="plus" /> Business
                      </button>
                    )}
                  </div>
                </div>
                {!closed &&
                  (bs.length === 0 ? (
                    <div className="card-pad">
                      <p className="faint small">No businesses{filtering ? " match" : " yet"}.</p>
                    </div>
                  ) : (
                    <BusinessPages businesses={bs} view={view} onOpen={(b) => nav(`/businesses/${b.id}`)} />
                  ))}
              </section>
            );
          })}
        </div>
      )}
      {creating === "platform" && <PlatformModal onClose={() => setCreating(null)} onSaved={(id) => nav(`/platforms/${id}`)} />}
      {typeof creating === "number" && (
        <BusinessModal platforms={platforms} platformId={creating} onClose={() => setCreating(null)} onSaved={(b) => nav(`/businesses/${b.id}`)} />
      )}
    </div>
  );
}

// BusinessPages shows a platform's businesses a page at a time — a 3×3
// grid of cards (or 10 rows) with Previous / Next.
function BusinessPages({ businesses, view, onOpen }: { businesses: Business[]; view: string; onOpen: (b: Business) => void }) {
  const size = view === "list" ? 10 : 9;
  const [page, setPage] = useState(0);
  const pages = Math.max(1, Math.ceil(businesses.length / size));
  // Searching or filtering can shrink the list under the current page.
  useEffect(() => {
    if (page >= pages) setPage(pages - 1);
  }, [page, pages]);
  const shown = businesses.slice(page * size, page * size + size);
  const pager =
    pages > 1 ? (
      <div className="row-between small" style={{ padding: "10px 16px", borderTop: "1px solid var(--border)" }}>
        <span className="faint">
          {page * size + 1}–{Math.min(businesses.length, (page + 1) * size)} of {businesses.length}
        </span>
        <div className="row" style={{ gap: 6 }}>
          <button className="btn btn-sm" disabled={page === 0} onClick={() => setPage(page - 1)}>
            ‹ Previous
          </button>
          <span className="faint">
            {page + 1} / {pages}
          </span>
          <button className="btn btn-sm" disabled={page >= pages - 1} onClick={() => setPage(page + 1)}>
            Next ›
          </button>
        </div>
      </div>
    ) : null;
  if (view === "list")
    return (
      <>
        <BusinessTable businesses={shown} />
        {pager}
      </>
    );
  return (
    <>
      <div className="card-pad">
        <div className="grid-3">
          {shown.map((b) => (
            <button key={b.id} className="card card-pad stack-sm" style={{ textAlign: "left", cursor: "pointer" }} onClick={() => onOpen(b)}>
              <div className="row-between">
                <h3>{b.name}</h3>
                <span className="row" style={{ gap: 4 }}>
                  <span className="chip">{b.key}</span>
                  <span className="chip" title="Business id">
                    id {b.id}
                  </span>
                </span>
              </div>
              <p className="muted small" style={{ minHeight: 20 }}>
                {b.description || "No description"}
              </p>
              <div className="row small faint">
                <span>{b.metrics} metrics</span>·<span>{b.measures} measures</span>·<span>{b.experiments} experiments</span>
              </div>
            </button>
          ))}
        </div>
      </div>
      {pager}
    </>
  );
}

// BusinessTable is the compact view: one row per business.
export function BusinessTable({ businesses, actions }: { businesses: Business[]; actions?: (b: Business) => ReactNode }) {
  const nav = useNavigate();
  return (
    <div className="table-wrap">
      <table className="tbl tbl-compact">
        <thead>
          <tr>
            <th>Business</th>
            <th>Key</th>
            <th className="num">Id</th>
            <th className="num">Metrics</th>
            <th className="num">Measures</th>
            <th className="num">Experiments</th>
            <th>Review</th>
            {actions && <th />}
          </tr>
        </thead>
        <tbody>
          {businesses.map((b) => (
            <tr key={b.id} className="clickable" onClick={() => nav(`/businesses/${b.id}`)}>
              <td>
                <div style={{ fontWeight: 600 }}>{b.name}</div>
                {b.description && <div className="small faint">{b.description}</div>}
              </td>
              <td className="mono">{b.key}</td>
              <td className="num mono">{b.id}</td>
              <td className="num">{b.metrics}</td>
              <td className="num">{b.measures}</td>
              <td className="num">{b.experiments}</td>
              <td className="small">{b.require_review ? "required" : "—"}</td>
              {actions && (
                <td className="right nowrap" onClick={(e) => e.stopPropagation()}>
                  {actions(b)}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export function PlatformModal({ platform, onClose, onSaved }: { platform?: Platform; onClose: () => void; onSaved: (id: number) => void }) {
  const [key, setKey] = useState(platform?.key ?? "");
  const [name, setName] = useState(platform?.name ?? "");
  const [description, setDescription] = useState(platform?.description ?? "");
  const [error, setError] = useState("");
  const save = async () => {
    try {
      // Updating answers 204 (no body); keep the id we already have.
      if (platform) {
        await api.updatePlatform(platform.id, { key, name, description });
        onSaved(platform.id);
      } else {
        onSaved((await api.createPlatform({ key, name, description })).id);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Modal
      title={platform ? "Edit platform" : "New platform"}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={save}>
            {platform ? "Save" : "Create"}
          </button>
        </>
      }
    >
      <Field label="Name">
        <input
          className="input"
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            if (!platform && (!key || key === slug(name))) setKey(slug(e.target.value));
          }}
          placeholder="My Shop"
        />
      </Field>
      <Field label="Key" hint="Services send it as platform, and every experiment's parameters sit under it: {&quot;key&quot;: {...}}.">
        <input className="input input-mono" value={key} onChange={(e) => setKey(e.target.value)} placeholder="tiktokshop" />
      </Field>
      {platform && key !== platform.key && (
        <div className="alert alert-warn small">
          Renaming the key moves every experiment's parameters from <code>{platform.key}</code> to <code>{key || "…"}</code>, and services must send
          the new key as <code>platform</code>. Update them at the same time.
        </div>
      )}
      <Field label="Description">
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <ErrorBox error={error} />
    </Modal>
  );
}

export function BusinessModal({
  platforms,
  platformId,
  business,
  onClose,
  onSaved,
}: {
  platforms: Platform[];
  platformId: number;
  business?: Business;
  onClose: () => void;
  onSaved: (b: Business) => void;
}) {
  const [pid, setPid] = useState(business?.platform_id ?? platformId);
  const [key, setKey] = useState(business?.key ?? "");
  const [name, setName] = useState(business?.name ?? "");
  const [description, setDescription] = useState(business?.description ?? "");
  const [review, setReview] = useState(business?.require_review ?? true);
  const [error, setError] = useState("");
  const save = async () => {
    try {
      const body = { platform_id: pid, key, name, description, require_review: review };
      onSaved(business ? await api.updateBusiness(business.id, body) : await api.createBusiness(body));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Modal
      title={business ? `Edit ${business.name}` : "New business"}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={save}>
            {business ? "Save" : "Create"}
          </button>
        </>
      }
    >
      <Field label="Platform" hint={business ? "Moving a business keeps its own metrics; it then shares the new platform's metrics and defaults." : undefined}>
        <select className="input" value={pid} onChange={(e) => setPid(Number(e.target.value))}>
          {platforms.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Name">
        <input
          className="input"
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            if (!business && (!key || key === slug(name))) setKey(slug(e.target.value));
          }}
          placeholder="Ads"
        />
      </Field>
      <Field label="Key" hint="Services send events with this key. It can't change later.">
        <input className="input input-mono" value={key} disabled={!!business} onChange={(e) => setKey(e.target.value)} placeholder="ads" />
      </Field>
      <Field label="Description">
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <label className="check">
        <input type="checkbox" checked={review} onChange={(e) => setReview(e.target.checked)} /> Experiments need review before they start
      </label>
      <ErrorBox error={error} />
    </Modal>
  );
}

// DeleteBusinessModal explains what deleting a business would remove, or
// why it can't be deleted, before anything happens.
export function DeleteBusinessModal({ business, onClose, onDeleted }: { business: Business; onClose: () => void; onDeleted: () => void }) {
  const check = useAsync(() => api.deleteCheck(business.id), [business.id]);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const c = check.data;
  const blocked = !!c && c.experiments > 0;
  const hasData = !!c && c.measures + c.metrics + c.groups + c.events > 0;
  const ready = !!c && !blocked && (!hasData || typed === business.key);
  const del = async () => {
    setBusy(true);
    setError("");
    try {
      await api.deleteBusiness(business.id);
      onDeleted();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={blocked ? `${business.name} can't be deleted` : `Delete ${business.name}?`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {blocked ? "Close" : "Cancel"}
          </button>
          {!blocked && (
            <button className="btn btn-danger" disabled={!ready || busy} onClick={del}>
              Delete business
            </button>
          )}
        </>
      }
    >
      {check.loading && !c ? (
        <Loading label="Checking what depends on it…" />
      ) : !c ? (
        <ErrorBox error={check.error} />
      ) : blocked ? (
        <div className="stack-sm">
          <div className="alert alert-warn small">
            It has <b>{c.experiments}</b> experiment{c.experiments === 1 ? "" : "s"}
            {c.active_experiments > 0 && <> ({c.active_experiments} running)</>}, archived ones included. Experiments keep their reports and history,
            which need their business — so a business with experiments can't be deleted. Its metrics aren't what blocks it; they would be deleted along
            with it.
          </div>
          <p className="small muted">
            Options: keep it (rename it or edit its description), or <b>move it</b> to another platform with the edit button.
          </p>
          <Link className="btn btn-sm" style={{ alignSelf: "flex-start" }} to={`/experiments?business=${business.id}`} onClick={onClose}>
            View its experiments
          </Link>
        </div>
      ) : (
        <div className="stack-sm">
          {hasData ? (
            <>
              <p className="muted">This permanently deletes the business and everything defined in it:</p>
              <ul className="small" style={{ margin: 0, paddingLeft: 18 }}>
                <li>{c.metrics} metrics and {c.measures} measures</li>
                <li>{c.groups} metric groups</li>
                <li>{c.events.toLocaleString()} received events</li>
              </ul>
              {c.other_experiments_use > 0 && (
                <div className="alert alert-warn small">
                  {c.other_experiments_use} experiment(s) of other businesses report its metric groups; those sections will disappear from their reports.
                </div>
              )}
              <Field label={`Type ${business.key} to confirm`}>
                <input className="input input-mono" value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus />
              </Field>
            </>
          ) : (
            <p className="muted">It has no experiments, metrics or events. It can be deleted safely.</p>
          )}
        </div>
      )}
      <ErrorBox error={error} />
    </Modal>
  );
}

export const slug = (s: string) =>
  s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .replace(/^(\d)/, "_$1")
    .slice(0, 40);
