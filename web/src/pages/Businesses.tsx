import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Empty, ErrorBox, Field, Icon, Loading, Modal } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { useAsync } from "../lib/hooks";

export default function Businesses() {
  const list = useAsync(() => api.businesses(), []);
  const isAdmin = useCan("admin");
  const nav = useNavigate();
  const [creating, setCreating] = useState(false);
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Businesses & metrics</h1>
          <p>
            Each business (search, feed, checkout…) sends its own events and defines its own metrics as formulas, like{" "}
            <code>search_gmv / users</code>. Experiments are measured with their business's metrics.
          </p>
        </div>
        {isAdmin && (
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            <Icon name="plus" /> New business
          </button>
        )}
      </div>
      <ErrorBox error={list.error} />
      {list.loading && !list.data ? (
        <Loading />
      ) : list.data?.length === 0 ? (
        <div className="card">
          <Empty title="No businesses yet">
            <p>Create one, or run <code>libra demo</code> on the server to load a sample “search” business.</p>
          </Empty>
        </div>
      ) : (
        <div className="grid-3">
          {list.data?.map((b) => (
            <button key={b.id} className="card card-pad stack-sm" style={{ textAlign: "left", cursor: "pointer" }} onClick={() => nav(`/businesses/${b.id}`)}>
              <div className="row-between">
                <h2>{b.name}</h2>
                <span className="chip">{b.key}</span>
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
      {creating && <BusinessModal onClose={() => setCreating(false)} onSaved={(id) => nav(`/businesses/${id}`)} />}
    </div>
  );
}

function BusinessModal({ onClose, onSaved }: { onClose: () => void; onSaved: (id: number) => void }) {
  const [key, setKey] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [review, setReview] = useState(true);
  const [error, setError] = useState("");
  const save = async () => {
    try {
      const b = await api.createBusiness({ key, name, description, require_review: review });
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
