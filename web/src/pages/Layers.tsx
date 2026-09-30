import { useState } from "react";
import { Link } from "react-router-dom";
import { Empty, ErrorBox, Field, Icon, Loading, Modal, StatusBadge, seriesColor } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { trafficPct } from "../lib/format";
import { useAsync } from "../lib/hooks";
import type { Diversion } from "../lib/types";

export default function Layers() {
  const layers = useAsync(() => api.layers(), []);
  const isAdmin = useCan("admin");
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [diversion, setDiversion] = useState<Diversion>("user_id");
  const [error, setError] = useState("");
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Traffic layers</h1>
          <p>
            Each layer splits units into 1,000 buckets. Experiments in the same layer get separate buckets, so they never share a unit — use one layer
            per area that could interfere (ranking, UI, pricing). Experiments in different layers overlap independently.
          </p>
        </div>
        {isAdmin && (
          <button className="btn btn-primary" onClick={() => setOpen(true)}>
            <Icon name="plus" /> New layer
          </button>
        )}
      </div>
      <ErrorBox error={layers.error} />
      {layers.loading && !layers.data ? (
        <Loading />
      ) : layers.data?.length === 0 ? (
        <div className="card">
          <Empty title="No layers yet" />
        </div>
      ) : (
        <div className="stack">
          {layers.data?.map((l) => (
            <section key={l.id} className="card card-pad stack-sm">
              <div className="row-between">
                <div>
                  <div className="row">
                    <h2>{l.name}</h2>
                    <span className="badge b-accent">split by {l.diversion === "device_id" ? "device id" : "user id"}</span>
                  </div>
                  {l.description && <p className="faint small">{l.description}</p>}
                </div>
                <div className="right">
                  <b>{trafficPct(1000 - l.used_buckets)}</b> <span className="faint small">free</span>
                </div>
              </div>
              <div className="bucketbar">
                {l.holders.map((h, i) => (
                  <div key={h.experiment_id} title={`${h.name}: ${trafficPct(h.buckets)}`} style={{ width: `${h.buckets / 10}%`, background: seriesColor(i), opacity: h.status === "paused" ? 0.45 : 1 }} />
                ))}
              </div>
              {l.holders.length > 0 && (
                <div className="stack-sm" style={{ marginTop: 4 }}>
                  {l.holders.map((h, i) => (
                    <div key={h.experiment_id} className="row small">
                      <span style={{ width: 10, height: 10, borderRadius: 3, background: seriesColor(i) }} />
                      <Link to={`/experiments/${h.experiment_id}`}>{h.name}</Link>
                      <StatusBadge status={h.status} />
                      <span className="faint">{trafficPct(h.buckets)}</span>
                    </div>
                  ))}
                </div>
              )}
            </section>
          ))}
        </div>
      )}
      {open && (
        <Modal
          title="New layer"
          onClose={() => setOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setOpen(false)}>Cancel</button>
              <button
                className="btn btn-primary"
                onClick={async () => {
                  try {
                    await api.createLayer(name, description, diversion);
                    setOpen(false);
                    setName("");
                    setDescription("");
                    layers.reload();
                  } catch (e) {
                    setError(e instanceof Error ? e.message : String(e));
                  }
                }}
              >
                Create
              </button>
            </>
          }
        >
          <Field label="Name">
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="search_ranking" />
          </Field>
          <Field label="Description">
            <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <Field
            label="Diversion (split traffic by)"
            hint="Every experiment in the layer randomizes on this id. User id keeps a person's experience consistent across devices; device id works before sign-in. It can't change later."
          >
            <select className="input" value={diversion} onChange={(e) => setDiversion(e.target.value as Diversion)}>
              <option value="user_id">User id</option>
              <option value="device_id">Device id</option>
            </select>
          </Field>
          <ErrorBox error={error} />
        </Modal>
      )}
    </div>
  );
}
