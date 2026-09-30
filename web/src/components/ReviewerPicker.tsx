import { useEffect, useRef, useState } from "react";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useDebounced } from "../lib/hooks";
import type { User } from "../lib/types";

// ReviewerPicker: type a name or email and pick from the matches (editors
// and admins — they can review). Picked people show as chips. Works the
// same with ten people or ten thousand: the server does the search.
export function ReviewerPicker({ value, onChange, already = [] }: { value: number[]; onChange: (ids: number[]) => void; already?: { user_id: number; name: string }[] }) {
  const { user: me } = useAuth();
  const [q, setQ] = useState("");
  const dq = useDebounced(q, 200);
  const [open, setOpen] = useState(false);
  const [results, setResults] = useState<User[]>([]);
  const [active, setActive] = useState(0);
  const [known, setKnown] = useState<Map<number, User>>(new Map());
  const [error, setError] = useState("");
  const box = useRef<HTMLDivElement>(null);
  const alreadyIds = already.map((a) => a.user_id);

  useEffect(() => {
    if (!open) return;
    let live = true;
    api
      .searchUsers(dq)
      .then((r) => {
        if (!live) return;
        setResults(r);
        setActive(0);
        setError("");
      })
      .catch((e) => live && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      live = false;
    };
  }, [dq, open]);
  useEffect(() => {
    const close = (e: MouseEvent) => box.current && !box.current.contains(e.target as Node) && setOpen(false);
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, []);

  const pick = (u: User) => {
    if (alreadyIds.includes(u.id) || value.includes(u.id)) return;
    setKnown(new Map(known).set(u.id, u));
    onChange([...value, u.id]);
    setQ("");
  };
  const options = results.filter((u) => !value.includes(u.id) && !alreadyIds.includes(u.id));
  const canReview = me && (me.role === "editor" || me.role === "admin");
  const nameOf = (id: number) => {
    const u = known.get(id) ?? (me && me.id === id ? (me as User) : undefined);
    return u ? `${u.display_name || u.username}${u.id === me?.id ? " (you)" : ""}` : `#${id}`;
  };

  return (
    <div className="stack-sm" ref={box}>
      {(already.length > 0 || value.length > 0) && (
        <div className="chip-row">
          {already.map((a) => (
            <span key={a.user_id} className="gchip locked" title="Already invited">
              {a.name}
            </span>
          ))}
          {value.map((id) => (
            <span key={id} className="gchip">
              {nameOf(id)}
              <button type="button" aria-label="Remove" onClick={() => onChange(value.filter((x) => x !== id))}>
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      <div style={{ position: "relative" }}>
        <div className="row" style={{ gap: 8 }}>
          <input
            className="input"
            style={{ flex: 1 }}
            placeholder="Type a name or email…"
            value={q}
            onFocus={() => setOpen(true)}
            onChange={(e) => {
              setQ(e.target.value);
              setOpen(true);
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setOpen(true);
                setActive((a) => Math.min(a + 1, options.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((a) => Math.max(a - 1, 0));
              } else if (e.key === "Enter" && open && options[active]) {
                e.preventDefault();
                pick(options[active]);
              } else if (e.key === "Escape") setOpen(false);
            }}
            role="combobox"
            aria-expanded={open}
          />
          {canReview && me && !value.includes(me.id) && !alreadyIds.includes(me.id) && (
            <button type="button" className="btn btn-sm" onClick={() => pick(me as User)}>
              Add me
            </button>
          )}
        </div>
        {open && (
          <ul className="combo-list" role="listbox">
            {options.length === 0 ? (
              <li className="faint small" style={{ padding: "8px 10px" }}>
                {error || (dq ? "No editor or admin matches." : "Type to search.")}
              </li>
            ) : (
              options.map((u, i) => (
                <li
                  key={u.id}
                  role="option"
                  aria-selected={i === active}
                  className={i === active ? "active" : ""}
                  onMouseEnter={() => setActive(i)}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    pick(u);
                  }}
                >
                  <span style={{ fontWeight: 600 }}>
                    {u.display_name || u.username}
                    {u.id === me?.id && <span className="faint"> (you)</span>}
                  </span>
                  <span className="faint small">
                    {" "}
                    {u.email || u.username} · {u.role}
                  </span>
                </li>
              ))
            )}
          </ul>
        )}
      </div>
    </div>
  );
}
