import { useState } from "react";
import { Link } from "react-router-dom";
import { ErrorBox, Field, Icon, Modal } from "../components/ui";
import { Pager, usePaged } from "../components/Pager";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { reloadDiversions, useDiversions } from "../lib/diversions";
import type { DiversionDef } from "../lib/types";

// Diversions: the ids traffic can be split by. user_id and device_id are
// built in; others (e.g. shop_id, session_id) are sent in the "ids" object.
export default function Diversions() {
  const isAdmin = useCan("admin");
  const list = useDiversions();
  const [editing, setEditing] = useState<DiversionDef | "new" | null>(null);
  const [error, setError] = useState("");
  const [q, setQ] = useState("");
  const query = q.trim().toLowerCase();
  const found = list.filter((d) => !query || [d.key, d.name, d.description].some((x) => x.toLowerCase().includes(query)));
  const paged = usePaged(found, 10, query);
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Diversions</h1>
          <p>
            A diversion is the id a layer splits traffic by — the same id always lands in the same bucket, so a user (or device, shop…) keeps their
            variant. Services send <code>user_id</code> and <code>device_id</code> directly and any other diversion in{" "}
            <code>{'"ids": {"shop_id": "…"}'}</code>.
          </p>
        </div>
        {isAdmin && (
          <button className="btn btn-primary" onClick={() => setEditing("new")}>
            <Icon name="plus" /> New diversion
          </button>
        )}
      </div>
      <section className="card">
      <ErrorBox error={error} />
      <div style={{ padding: "10px 16px" }}>
        <input className="input" style={{ maxWidth: 360 }} placeholder="Search diversions by key, name or description…" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      <div className="table-wrap">
        <table className="tbl tbl-compact">
          <thead>
            <tr>
              <th>Diversion</th>
              <th>Key</th>
              <th>Description</th>
              <th title="Traffic layers (shared or dedicated) that split by this id">Used by</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {paged.slice.map((d) => (
              <tr key={d.key}>
                <td style={{ fontWeight: 600 }}>
                  {d.name} {d.builtin && <span className="badge">built in</span>}
                </td>
                <td className="mono">{d.key}</td>
                <td className="faint small">{d.description}</td>
                <td className="nowrap">
                  {d.layers === 0 && <span className="faint">Not used</span>}
                  {d.layers - d.dedicated_layers > 0 && (
                    <Link to={`/layers?diversion=${encodeURIComponent(d.key)}`}>
                      {d.layers - d.dedicated_layers} shared {d.layers - d.dedicated_layers === 1 ? "layer" : "layers"}
                    </Link>
                  )}
                  {d.layers - d.dedicated_layers > 0 && d.dedicated_layers > 0 && <span className="faint"> · </span>}
                  {d.dedicated_layers > 0 && (
                    <span className="faint" title="Experiments with a layer of their own (Auto traffic)">
                      {d.dedicated_layers} dedicated
                    </span>
                  )}
                </td>
                <td className="right nowrap">
                  {isAdmin && (
                    <>
                      <button className="icon-btn" aria-label="Edit" onClick={() => setEditing(d)}>
                        <Icon name="edit" size={15} />
                      </button>
                      {!d.builtin && (
                        <button
                          className="icon-btn"
                          aria-label="Delete"
                          disabled={d.layers > 0}
                          title={d.layers > 0 ? "Layers split by it" : "Delete"}
                          onClick={async () => {
                            if (!confirm(`Delete diversion ${d.key}?`)) return;
                            setError("");
                            try {
                              await api.deleteDiversion(d.key);
                              await reloadDiversions();
                            } catch (e) {
                              setError(e instanceof Error ? e.message : String(e));
                            }
                          }}
                        >
                          <Icon name="trash" size={15} />
                        </button>
                      )}
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {found.length === 0 && <div className="small faint" style={{ padding: 16 }}>No diversions match.</div>}
      </div>
      <Pager page={paged.page} pages={paged.pages} total={paged.total} size={paged.size} onPage={paged.setPage} onSize={paged.setSize} noun="diversions" />
      </section>
      {editing && <DiversionModal d={editing === "new" ? null : editing} onClose={() => setEditing(null)} />}
    </div>
  );
}

function DiversionModal({ d, onClose }: { d: DiversionDef | null; onClose: () => void }) {
  const [key, setKey] = useState(d?.key ?? "");
  const [name, setName] = useState(d?.name ?? "");
  const [description, setDescription] = useState(d?.description ?? "");
  const [error, setError] = useState("");
  const save = async () => {
    setError("");
    try {
      if (d) await api.updateDiversion(d.key, { name, description });
      else await api.createDiversion({ key, name, description });
      await reloadDiversions();
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Modal
      title={d ? `Edit ${d.key}` : "New diversion"}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={save}>
            Save
          </button>
        </>
      }
    >
      <Field label="Name">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Shop id" />
      </Field>
      <Field label="Key" hint="Lowercase letters, digits and _. Services send it in the ids object. It can't change later.">
        <input className="input input-mono" value={key} disabled={!!d} onChange={(e) => setKey(e.target.value)} placeholder="shop_id" />
      </Field>
      <Field label="Description">
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <ErrorBox error={error} />
    </Modal>
  );
}
