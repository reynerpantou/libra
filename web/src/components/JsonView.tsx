import { useState, type ReactNode } from "react";
import { copyText } from "../lib/exportTable";
import { Segmented } from "./ui";

// JsonView shows a JSON payload two ways: Raw (exactly what goes over the
// wire, compact) and Pretty (an indented, coloured tree you can fold).
export function JsonView({ title, value, raw, extra }: { title: ReactNode; value: unknown; raw?: string; extra?: { label: string; text: string } }) {
  const [mode, setMode] = useState<"pretty" | "raw" | "extra">("pretty");
  const [copied, setCopied] = useState(false);
  const rawText = raw ?? JSON.stringify(value);
  const shown = mode === "raw" ? rawText : mode === "extra" && extra ? extra.text : JSON.stringify(value, null, 2);
  const opts: ["pretty" | "raw" | "extra", string][] = [
    ["pretty", "Pretty"],
    ["raw", "Raw"],
  ];
  if (extra) opts.push(["extra", extra.label]);
  return (
    <section className="card">
      <div className="card-head">
        <h2>{title}</h2>
        <div className="row" style={{ gap: 6 }}>
          <Segmented options={opts} value={mode} onChange={setMode} />
          <button
            className="btn btn-sm"
            onClick={async () => {
              await copyText(shown);
              setCopied(true);
              setTimeout(() => setCopied(false), 1200);
            }}
          >
            {copied ? "Copied ✓" : "Copy"}
          </button>
        </div>
      </div>
      <div className="card-pad" style={{ paddingTop: 0 }}>
        {mode === "pretty" ? (
          <div className="json-tree">
            <Node value={value} depth={0} />
          </div>
        ) : (
          <pre className="code json-raw">{shown}</pre>
        )}
      </div>
    </section>
  );
}

function Node({ value, depth, name, last = true }: { value: unknown; depth: number; name?: string; last?: boolean }) {
  const [open, setOpen] = useState(depth < 3);
  const comma = last ? "" : ",";
  const label = name !== undefined && <span className="jt-key">"{name}"</span>;
  if (value === null || typeof value !== "object") {
    return (
      <div className="jt-row">
        {label}
        {label && ": "}
        <Scalar v={value} />
        {comma}
      </div>
    );
  }
  const arr = Array.isArray(value);
  const entries = arr ? (value as unknown[]).map((v, i) => [String(i), v] as const) : Object.entries(value as Record<string, unknown>);
  const [l, r] = arr ? ["[", "]"] : ["{", "}"];
  if (entries.length === 0)
    return (
      <div className="jt-row">
        {label}
        {label && ": "}
        {l + r}
        {comma}
      </div>
    );
  return (
    <div>
      <div className="jt-row">
        <button className="jt-toggle" onClick={() => setOpen(!open)} aria-label={open ? "Collapse" : "Expand"}>
          {open ? "▾" : "▸"}
        </button>
        {label}
        {label && ": "}
        {l}
        {!open && (
          <button className="jt-more" onClick={() => setOpen(true)}>
            {entries.length} {arr ? (entries.length === 1 ? "item" : "items") : entries.length === 1 ? "key" : "keys"}
          </button>
        )}
        {!open && r + comma}
      </div>
      {open && (
        <>
          <div className="jt-children">
            {entries.map(([k, v], i) => (
              <Node key={k} name={arr ? undefined : k} value={v} depth={depth + 1} last={i === entries.length - 1} />
            ))}
          </div>
          <div className="jt-row">
            {r}
            {comma}
          </div>
        </>
      )}
    </div>
  );
}

function Scalar({ v }: { v: unknown }) {
  if (v === null) return <span className="jt-null">null</span>;
  if (typeof v === "string") return <span className="jt-str">{JSON.stringify(v)}</span>;
  if (typeof v === "number") return <span className="jt-num">{v}</span>;
  if (typeof v === "boolean") return <span className="jt-bool">{String(v)}</span>;
  return <span>{String(v)}</span>;
}
