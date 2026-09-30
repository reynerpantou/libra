import { useState } from "react";
import { useAuth } from "../lib/auth";
import type { User } from "../lib/types";

// ReviewerPicker chooses reviewers among editors and admins. You can pick
// yourself; people already invited are shown as such.
export function ReviewerPicker({ users, value, onChange, already = [] }: { users: User[]; value: number[]; onChange: (ids: number[]) => void; already?: number[] }) {
  const { user: me } = useAuth();
  const [q, setQ] = useState("");
  const eligible = users
    .filter((u) => u.role === "editor" || u.role === "admin")
    .sort((a, b) => Number(b.id === me?.id) - Number(a.id === me?.id) || a.display_name.localeCompare(b.display_name));
  const s = q.trim().toLowerCase();
  const shown = eligible.filter((u) => !s || [u.display_name, u.username, u.email ?? ""].some((x) => x.toLowerCase().includes(s)));
  return (
    <div className="stack-sm">
      {eligible.length > 6 && <input className="input" placeholder="Search people…" value={q} onChange={(e) => setQ(e.target.value)} />}
      <div className="reviewer-list">
        {shown.map((u) => {
          const invited = already.includes(u.id);
          const on = invited || value.includes(u.id);
          return (
            <label key={u.id} className={`gpick-item ${on ? "on" : ""}`}>
              <input
                type="checkbox"
                checked={on}
                disabled={invited}
                onChange={() => onChange(value.includes(u.id) ? value.filter((x) => x !== u.id) : [...value, u.id])}
              />
              <span>
                <span style={{ fontWeight: 600 }}>
                  {u.display_name || u.username}
                  {u.id === me?.id && <span className="faint"> (you)</span>}
                </span>
                <span className="faint small" style={{ display: "block" }}>
                  {u.email || u.username} · {u.role}
                  {invited && " · already invited"}
                </span>
              </span>
            </label>
          );
        })}
        {shown.length === 0 && <div className="small faint">No editors or admins match.</div>}
      </div>
    </div>
  );
}
