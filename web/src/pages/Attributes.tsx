import { useState } from "react";
import { Empty, ErrorBox, Field, Icon, Loading, Modal } from "../components/ui";
import { api } from "../lib/api";
import { useCan } from "../lib/auth";
import { useAsync } from "../lib/hooks";
import { Pager, usePaged } from "../components/Pager";
import type { Attribute, AttrType } from "../lib/types";

const TYPE_HELP: Record<AttrType, string> = {
  string: "Text, compared exactly (case-insensitive): region, device, language…",
  number: "Numbers, compared with < > ≤ ≥: age, order count…",
  version: "Dotted versions compared part by part, so 10.2 > 9.9: app_version…",
  boolean: "true / false: is_new_user…",
};

// Targeting attributes: the request attributes experiments can target on.
export default function Attributes() {
  const list = useAsync(() => api.attributes(), []);
  const discovered = useAsync(() => api.discoveredAttributes(), []);
  const [q, setQ] = useState("");
  const [type, setType] = useState("");
  const query = q.trim().toLowerCase();
  const found = (list.data ?? []).filter(
    (a) => (!type || a.type === type) && (!query || [a.key, a.name, a.description, ...a.options].some((x) => x.toLowerCase().includes(query)))
  );
  const paged = usePaged(found, 25, `${query}|${type}`);
  const canEdit = useCan("editor");
  const [editing, setEditing] = useState<Attribute | Partial<Attribute> | null>(null);
  const [error, setError] = useState("");
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Targeting attributes</h1>
          <p>
            The request attributes experiments can target on. Services send them in <code>attrs</code> when they call resolve; the targeting editor offers
            only these, with their known values.
          </p>
        </div>
        {canEdit && (
          <button className="btn btn-primary" onClick={() => setEditing({ type: "string", options: [] })}>
            <Icon name="plus" /> New attribute
          </button>
        )}
      </div>
      <ErrorBox error={list.error || error} />
      {list.loading && !list.data ? (
        <Loading />
      ) : (
        <div className="stack">
          <section className="card">
            {(list.data?.length ?? 0) > 0 && (
              <div className="card-head">
                <div className="row" style={{ gap: 10, flex: 1 }}>
                  <input className="input" style={{ flex: 1, maxWidth: 380 }} placeholder="Search attributes by key, name or value…" value={q} onChange={(e) => setQ(e.target.value)} />
                  <select className="input" style={{ width: 150 }} value={type} onChange={(e) => setType(e.target.value)} aria-label="Type">
                    <option value="">Any type</option>
                    <option value="string">string</option>
                    <option value="number">number</option>
                    <option value="version">version</option>
                    <option value="boolean">boolean</option>
                  </select>
                  <span className="small faint">
                    {found.length} of {list.data?.length}
                  </span>
                </div>
              </div>
            )}
            {found.length === 0 ? (
              <Empty title={list.data?.length ? "No attributes match" : "No attributes yet"} />
            ) : (
              <div className="table-wrap">
                <table className="tbl">
                  <thead>
                    <tr>
                      <th>Attribute</th>
                      <th>Type</th>
                      <th>Known values</th>
                      <th className="num">Used by</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {paged.slice.map((a) => (
                      <tr key={a.id}>
                        <td>
                          <div className="mono" style={{ fontWeight: 600 }}>
                            {a.key}
                          </div>
                          <div className="faint small">
                            {a.name}
                            {a.description && ` — ${a.description}`}
                          </div>
                        </td>
                        <td>
                          <span className="badge">{a.type}</span>
                        </td>
                        <td>
                          <div className="pill-row">
                            {a.options.slice(0, 12).map((o) => (
                              <span key={o} className="chip">
                                {o}
                              </span>
                            ))}
                            {a.options.length > 12 && <span className="faint small">+{a.options.length - 12}</span>}
                            {a.options.length === 0 && <span className="faint small">free input</span>}
                          </div>
                        </td>
                        <td className="num">{a.used_by ? `${a.used_by} experiment${a.used_by > 1 ? "s" : ""}` : "—"}</td>
                        <td className="right nowrap">
                          {canEdit && (
                            <>
                              <button className="icon-btn" aria-label="Edit" onClick={() => setEditing(a)}>
                                <Icon name="edit" size={16} />
                              </button>
                              <button
                                className="icon-btn"
                                aria-label="Delete"
                                disabled={a.used_by > 0}
                                title={a.used_by > 0 ? "Used by experiments" : "Delete"}
                                onClick={async () => {
                                  if (!confirm(`Delete attribute ${a.key}?`)) return;
                                  setError("");
                                  try {
                                    await api.deleteAttribute(a.id);
                                    list.reload();
                                  } catch (e) {
                                    setError(e instanceof Error ? e.message : String(e));
                                  }
                                }}
                              >
                                <Icon name="trash" size={16} />
                              </button>
                            </>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <Pager page={paged.page} pages={paged.pages} total={paged.total} size={paged.size} onPage={paged.setPage} onSize={paged.setSize} noun="attributes" />
          </section>
          {(discovered.data?.length ?? 0) > 0 && (
            <section className="card">
              <div className="card-head">
                <div>
                  <h2>Seen in traffic, not registered</h2>
                  <p>Attributes services already send with resolve calls. Register one to target on it.</p>
                </div>
              </div>
              <table className="tbl tbl-compact">
                <tbody>
                  {discovered.data?.map((d) => (
                    <tr key={d.key}>
                      <td className="mono">{d.key}</td>
                      <td>
                        <div className="pill-row">
                          {d.values.slice(0, 10).map((v) => (
                            <span key={v} className="chip">
                              {v}
                            </span>
                          ))}
                        </div>
                      </td>
                      <td className="right">
                        {canEdit && (
                          <button className="btn btn-sm" onClick={() => setEditing({ key: d.key, name: d.key, type: "string", options: d.values })}>
                            Register
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
          )}
        </div>
      )}
      {editing && (
        <AttributeModal
          attr={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            list.reload();
            discovered.reload();
          }}
        />
      )}
    </div>
  );
}

function AttributeModal({ attr, onClose, onSaved }: { attr: Partial<Attribute>; onClose: () => void; onSaved: () => void }) {
  const [key, setKey] = useState(attr.key ?? "");
  const [name, setName] = useState(attr.name ?? "");
  const [description, setDescription] = useState(attr.description ?? "");
  const [type, setType] = useState<AttrType>(attr.type ?? "string");
  const [options, setOptions] = useState((attr.options ?? []).join("\n"));
  const [error, setError] = useState("");
  const save = async () => {
    setError("");
    const body = { key, name, description, type, options: options.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean) };
    try {
      if (attr.id) await api.updateAttribute(attr.id, body);
      else await api.createAttribute(body);
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <Modal
      title={attr.id ? `Edit ${attr.key}` : "New targeting attribute"}
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
      <div className="grid-2">
        <Field label="Key" hint={attr.id ? "Can't change: rules refer to it." : "What services send in attrs, e.g. app_version."}>
          <input className="input input-mono" value={key} disabled={!!attr.id} onChange={(e) => setKey(e.target.value)} />
        </Field>
        <Field label="Name">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
      </div>
      <Field label="Type" hint={TYPE_HELP[type]}>
        <select className="input" value={type} onChange={(e) => setType(e.target.value as AttrType)}>
          <option value="string">Text</option>
          <option value="number">Number</option>
          <option value="version">Version</option>
          <option value="boolean">Boolean</option>
        </select>
      </Field>
      {type !== "boolean" && (
        <Field label="Known values" hint="One per line. Offered as choices in the targeting editor; leave empty for free input.">
          <textarea className="input input-mono" rows={5} value={options} onChange={(e) => setOptions(e.target.value)} />
        </Field>
      )}
      <Field label="Description">
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <ErrorBox error={error} />
    </Modal>
  );
}
