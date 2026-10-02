import { useEffect, useMemo, useState, type ReactNode } from "react";
import { copyText } from "../lib/exportTable";
import { Segmented } from "./ui";

// JsonView shows a JSON payload two ways: Raw (exactly what goes over the
// wire, compact) and Pretty (an indented, coloured tree you can fold).
export function JsonView({ title, value, raw, extra }: { title: ReactNode; value: unknown; raw?: string; extra?: { label: string; text: string } }) {
  const [mode, setMode] = useState<"pretty" | "raw" | "extra">("pretty");
  const [copied, setCopied] = useState(false);
  // Strings that hold JSON (e.g. "{\"order\": …}") can be opened as trees.
  const [expandStrings, setExpandStrings] = useState(false);
  const hasJsonStrings = useMemo(() => containsJsonString(value), [value]);
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
          {mode === "pretty" && hasJsonStrings && (
            <label className="check small" title="Show strings that contain JSON as trees. The value is still a string on the wire.">
              <input type="checkbox" checked={expandStrings} onChange={(e) => setExpandStrings(e.target.checked)} /> Expand JSON strings
            </label>
          )}
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
            <Node value={value} depth={0} expandStrings={expandStrings} />
          </div>
        ) : (
          <pre className="code json-raw">{shown}</pre>
        )}
      </div>
    </section>
  );
}

function Node({
  value,
  depth,
  name,
  last = true,
  expandStrings,
}: {
  value: unknown;
  depth: number;
  name?: string;
  last?: boolean;
  expandStrings: boolean;
}) {
  const [open, setOpen] = useState(depth < 3);
  const comma = last ? "" : ",";
  const label = name !== undefined && <span className="jt-key">"{name}"</span>;
  const parsed = typeof value === "string" ? parseJsonString(value) : undefined;
  if (parsed !== undefined) {
    return <JsonStringNode raw={value as string} parsed={parsed} label={label} comma={comma} depth={depth} expandStrings={expandStrings} />;
  }
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
              <Node key={k} name={arr ? undefined : k} value={v} depth={depth + 1} last={i === entries.length - 1} expandStrings={expandStrings} />
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

// parseJsonString returns the object or array a string holds, or undefined
// when it isn't JSON (plain strings, numbers-as-strings stay strings).
export function parseJsonString(s: string): unknown {
  const t = s.trim();
  if (t.length < 2 || !((t[0] === "{" && t.endsWith("}")) || (t[0] === "[" && t.endsWith("]")))) return undefined;
  try {
    const v = JSON.parse(t);
    return v && typeof v === "object" ? v : undefined;
  } catch {
    return undefined;
  }
}

function containsJsonString(v: unknown, depth = 0): boolean {
  if (depth > 40) return false;
  if (typeof v === "string") return parseJsonString(v) !== undefined;
  if (v && typeof v === "object") return Object.values(v as Record<string, unknown>).some((x) => containsJsonString(x, depth + 1));
  return false;
}

// JsonStringNode: a string that holds JSON. Collapsed it reads as the string
// (with a JSON tag); opened it shows the decoded tree, marked as decoded so
// it's clear the value is still a string on the wire.
function JsonStringNode({
  raw,
  parsed,
  label,
  comma,
  depth,
  expandStrings,
}: {
  raw: string;
  parsed: unknown;
  label: ReactNode;
  comma: string;
  depth: number;
  expandStrings: boolean;
}) {
  const [open, setOpen] = useState(expandStrings);
  useEffect(() => setOpen(expandStrings), [expandStrings]);
  const tag = (
    <button className="jt-jsonstr" onClick={() => setOpen(!open)} title={open ? "Show as the string it is" : "Decode this JSON string"}>
      {open ? "▾ decoded JSON string" : "JSON ▸"}
    </button>
  );
  if (!open)
    return (
      <div className="jt-row">
        {label}
        {label && ": "}
        <span className="jt-str">{JSON.stringify(raw)}</span> {tag}
        {comma}
      </div>
    );
  return (
    <div>
      <div className="jt-row">
        {label}
        {label && ": "}
        {tag}
      </div>
      <div className="jt-children jt-decoded">
        <Node value={parsed} depth={0} expandStrings={expandStrings} last />
      </div>
      {comma && <div className="jt-row">{comma}</div>}
    </div>
  );
}
