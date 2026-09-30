import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Empty, ErrorBox, Field, Icon, Loading, Modal } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { useAsync } from "../lib/hooks";
import type { Platform } from "../lib/types";

export default function Businesses() {
  const list = useAsync(() => api.platforms(), []);
  const isAdmin = useCan("admin");
  const nav = useNavigate();
  const [creating, setCreating] = useState<"platform" | number | null>(null);
  const platforms = list.data ?? [];
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Businesses & metrics</h1>
          <p>
            A <b>platform</b> (an app or company) holds <b>businesses</b> (search, feed, checkout…). Each business sends its own events and defines
            metrics as formulas, like <code>search_gmv / users</code>; platform metrics and default groups apply to every business under it.
          </p>
        </div>
        {isAdmin && (
          <div className="row">
            <button className="btn" onClick={() => setCreating("platform")}>
              <Icon name="plus" /> New platform
            </button>
            {platforms.length > 0 && (
              <button className="btn btn-primary" onClick={() => setCreating(platforms[0].id)}>
                <Icon name="plus" /> New business
              </button>
            )}
          </div>
        )}
      </div>
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
      ) : (
        <div className="stack">
          {platforms.map((p) => (
            <section key={p.id} className="card">
              <div className="card-head">
                <div>
                  <div className="row">
                    <h2>
                      <Link to={`/platforms/${p.id}`}>{p.name}</Link>
                    </h2>
                    <span className="chip">{p.key}</span>
                    <span className="chip" title="Platform id">
                      id {p.id}
                    </span>
                  </div>
                  <p className="small faint">
                    {p.description || "No description"} · shared: {p.metrics} metrics, {p.measures} measures, {p.metric_groups} groups
                  </p>
                </div>
                <div className="row">
                  <Link className="btn btn-sm" to={`/platforms/${p.id}`}>
                    Platform metrics
                  </Link>
                  {isAdmin && (
                    <button className="btn btn-sm" onClick={() => setCreating(p.id)}>
                      <Icon name="plus" /> Business
                    </button>
                  )}
                </div>
              </div>
              <div className="card-pad">
                {p.businesses.length === 0 ? (
                  <p className="faint small">No businesses yet.</p>
                ) : (
                  <div className="grid-3">
                    {p.businesses.map((b) => (
                      <button key={b.id} className="card card-pad stack-sm" style={{ textAlign: "left", cursor: "pointer" }} onClick={() => nav(`/businesses/${b.id}`)}>
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
                )}
              </div>
            </section>
          ))}
        </div>
      )}
      {creating === "platform" && <PlatformModal onClose={() => setCreating(null)} onSaved={(id) => nav(`/platforms/${id}`)} />}
      {typeof creating === "number" && (
        <BusinessModal platforms={platforms} platformId={creating} onClose={() => setCreating(null)} onSaved={(id) => nav(`/businesses/${id}`)} />
      )}
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
      const p = platform ? await api.updatePlatform(platform.id, { key, name, description }) : await api.createPlatform({ key, name, description });
      onSaved(p.id);
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
      <Field label="Key" hint="A short permanent id.">
        <input className="input input-mono" value={key} disabled={!!platform} onChange={(e) => setKey(e.target.value)} placeholder="shop" />
      </Field>
      <Field label="Description">
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <ErrorBox error={error} />
    </Modal>
  );
}

function BusinessModal({ platforms, platformId, onClose, onSaved }: { platforms: Platform[]; platformId: number; onClose: () => void; onSaved: (id: number) => void }) {
  const [pid, setPid] = useState(platformId);
  const [key, setKey] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [review, setReview] = useState(true);
  const [error, setError] = useState("");
  const save = async () => {
    try {
      const b = await api.createBusiness({ platform_id: pid, key, name, description, require_review: review });
      onSaved(b.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Modal
      title="New business"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={save}>
            Create
          </button>
        </>
      }
    >
      <Field label="Platform">
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
            if (!key || key === slug(name)) setKey(slug(e.target.value));
          }}
          placeholder="Search"
        />
      </Field>
      <Field label="Key" hint="Services send events with this key. It can't change later.">
        <input className="input input-mono" value={key} onChange={(e) => setKey(e.target.value)} placeholder="search" />
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

export const slug = (s: string) =>
  s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .replace(/^(\d)/, "_$1")
    .slice(0, 40);
