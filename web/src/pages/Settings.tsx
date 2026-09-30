import { useState } from "react";
import { Empty, ErrorBox, Field, Icon, Loading, Modal, Tabs } from "../components/ui";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { ago, fmtDate } from "../lib/format";
import { useAsync } from "../lib/hooks";
import type { ApiKey, User } from "../lib/types";

export default function Settings() {
  const [tab, setTab] = useState<"keys" | "users">("keys");
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Settings</h1>
          <p>API keys for services, and who can use Libra.</p>
        </div>
      </div>
      <Tabs
        tabs={[
          ["keys", "API keys"],
          ["users", "People"],
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === "keys" ? <Keys /> : <Users />}
    </div>
  );
}

function Keys() {
  const keys = useAsync(() => api.apiKeys(), []);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["runtime", "ingest"]);
  const [created, setCreated] = useState<ApiKey | null>(null);
  const [error, setError] = useState("");
  const toggle = (s: string) => setScopes(scopes.includes(s) ? scopes.filter((x) => x !== s) : [...scopes, s]);
  return (
    <div className="stack">
      <section className="card card-pad stack">
        <h2>Create a key</h2>
        <div className="row" style={{ alignItems: "flex-end" }}>
          <div style={{ flex: 1, minWidth: 200 }}>
            <Field label="Name">
              <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="search-api production" />
            </Field>
          </div>
          <label className="check" style={{ height: 34 }}>
            <input type="checkbox" checked={scopes.includes("runtime")} onChange={() => toggle("runtime")} /> runtime (resolve, exposures)
          </label>
          <label className="check" style={{ height: 34 }}>
            <input type="checkbox" checked={scopes.includes("ingest")} onChange={() => toggle("ingest")} /> ingest (events)
          </label>
          <button
            className="btn btn-primary"
            disabled={!name.trim() || scopes.length === 0}
            onClick={async () => {
              setError("");
              try {
                setCreated(await api.createApiKey(name, scopes));
                setName("");
                keys.reload();
              } catch (e) {
                setError(e instanceof Error ? e.message : String(e));
              }
            }}
          >
            Create
          </button>
        </div>
        <ErrorBox error={error} />
      </section>
      <section className="card">
        {keys.loading && !keys.data ? (
          <Loading />
        ) : keys.data?.length === 0 ? (
          <Empty title="No API keys" />
        ) : (
          <table className="tbl">
            <thead>
              <tr>
                <th>Name</th>
                <th>Key</th>
                <th>Scopes</th>
                <th>Created</th>
                <th>Last used</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {keys.data?.map((k) => (
                <tr key={k.id} style={k.revoked_at ? { opacity: 0.5 } : undefined}>
                  <td>{k.name}</td>
                  <td className="mono">{k.prefix}…</td>
                  <td>{k.scopes.join(", ")}</td>
                  <td className="small">
                    {fmtDate(k.created_at)} <span className="faint">{k.created_by && `by ${k.created_by}`}</span>
                  </td>
                  <td className="small">{ago(k.last_used_at)}</td>
                  <td className="right">
                    {k.revoked_at ? (
                      <span className="badge">revoked</span>
                    ) : (
                      <button
                        className="btn btn-sm btn-danger"
                        onClick={async () => {
                          if (confirm(`Revoke ${k.name}? Services using it stop working within 30 seconds.`)) {
                            await api.revokeApiKey(k.id);
                            keys.reload();
                          }
                        }}
                      >
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
      {created && (
        <Modal title="Your new API key" onClose={() => setCreated(null)} footer={<button className="btn btn-primary" onClick={() => setCreated(null)}>Done</button>}>
          <p className="muted">Copy it now — it isn't shown again.</p>
          <pre className="code" style={{ userSelect: "all" }}>{created.key}</pre>
        </Modal>
      )}
    </div>
  );
}

function Users() {
  const users = useAsync(() => api.users(), []);
  const { user: me } = useAuth();
  const [editing, setEditing] = useState<User | "new" | null>(null);
  return (
    <section className="card">
      <div className="card-head">
        <div>
          <h2>People</h2>
          <p>Admins manage settings; editors create and run experiments and metrics; viewers read.</p>
        </div>
        <button className="btn btn-primary btn-sm" onClick={() => setEditing("new")}>
          <Icon name="plus" /> Invite
        </button>
      </div>
      <table className="tbl">
        <thead>
          <tr>
            <th>Name</th>
            <th>Email (signs in with Google/Apple)</th>
            <th>Role</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {users.data?.map((u) => (
            <tr key={u.id}>
              <td>
                {u.display_name} <span className="faint mono small">{u.username}</span> {u.is_owner && <span className="badge b-accent">owner</span>}
              </td>
              <td>{u.email || <span className="faint">not set — use a sign-in link</span>}</td>
              <td>{u.role}</td>
              <td className="right nowrap">
                <button className="icon-btn" aria-label="Edit" onClick={() => setEditing(u)}>
                  <Icon name="edit" size={16} />
                </button>
                {!u.is_owner && u.id !== me?.id && (
                  <button
                    className="icon-btn"
                    aria-label="Delete"
                    onClick={async () => {
                      if (confirm(`Remove ${u.display_name}?`)) {
                        await api.deleteUser(u.id);
                        users.reload();
                      }
                    }}
                  >
                    <Icon name="trash" size={16} />
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {editing && (
        <UserModal
          user={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            users.reload();
          }}
        />
      )}
    </section>
  );
}

function UserModal({ user, onClose, onSaved }: { user: User | null; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState({ username: user?.username ?? "", email: user?.email ?? "", display_name: user?.display_name ?? "", role: user?.role ?? "editor" });
  const [error, setError] = useState("");
  return (
    <Modal
      title={user ? `Edit ${user.display_name}` : "Invite someone"}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button
            className="btn btn-primary"
            onClick={async () => {
              try {
                if (user) await api.updateUser(user.id, f);
                else await api.createUser(f);
                onSaved();
              } catch (e) {
                setError(e instanceof Error ? e.message : String(e));
              }
            }}
          >
            Save
          </button>
        </>
      }
    >
      {!user && (
        <Field label="Username">
          <input className="input" value={f.username} onChange={(e) => setF({ ...f, username: e.target.value })} />
        </Field>
      )}
      <Field label="Display name">
        <input className="input" value={f.display_name} onChange={(e) => setF({ ...f, display_name: e.target.value })} />
      </Field>
      <Field label="Email" hint="Whoever proves this address with Google or Apple signs in as this person.">
        <input className="input" value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} />
      </Field>
      <Field label="Role">
        <select className="input" value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as User["role"] })} disabled={user?.is_owner}>
          <option value="admin">Admin</option>
          <option value="editor">Editor</option>
          <option value="viewer">Viewer</option>
        </select>
      </Field>
      <ErrorBox error={error} />
    </Modal>
  );
}
