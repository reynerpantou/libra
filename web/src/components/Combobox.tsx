import { useEffect, useMemo, useRef, useState } from "react";

export interface ComboOption {
  value: string;
  label: string;
  hint?: string;
}

// A dropdown you can type into to filter: arrow keys move, Enter picks,
// Escape closes. Only listed options can be chosen.
export function Combobox({
  options,
  value,
  onChange,
  placeholder,
  width,
}: {
  options: ComboOption[];
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  width?: number | string;
}) {
  const current = options.find((o) => o.value === value);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const box = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLUListElement>(null);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((o) => `${o.value} ${o.label} ${o.hint ?? ""}`.toLowerCase().includes(q));
  }, [options, query]);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);

  useEffect(() => setActive(0), [query]);
  useEffect(() => {
    list.current?.children[active]?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const pick = (o: ComboOption) => {
    onChange(o.value);
    setOpen(false);
    setQuery("");
  };

  return (
    <div className="combo" ref={box} style={{ width }}>
      <input
        className="input"
        value={open ? query : current ? current.label : value}
        placeholder={current ? current.label : placeholder}
        onFocus={() => {
          setOpen(true);
          setQuery("");
        }}
        onChange={(e) => {
          setQuery(e.target.value);
          setOpen(true);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setOpen(true);
            setActive((a) => Math.min(a + 1, filtered.length - 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setActive((a) => Math.max(a - 1, 0));
          } else if (e.key === "Enter") {
            e.preventDefault();
            if (open && filtered[active]) pick(filtered[active]);
          } else if (e.key === "Escape") {
            setOpen(false);
          }
        }}
        role="combobox"
        aria-expanded={open}
      />
      {open && (
        <ul className="combo-list" ref={list} role="listbox">
          {filtered.length === 0 ? (
            <li className="combo-empty">No match</li>
          ) : (
            filtered.map((o, i) => (
              <li
                key={o.value}
                role="option"
                aria-selected={o.value === value}
                className={`${i === active ? "active" : ""} ${o.value === value ? "selected" : ""}`}
                onMouseEnter={() => setActive(i)}
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(o);
                }}
              >
                <span>{o.label}</span>
                {o.hint && <small>{o.hint}</small>}
              </li>
            ))
          )}
        </ul>
      )}
    </div>
  );
}
