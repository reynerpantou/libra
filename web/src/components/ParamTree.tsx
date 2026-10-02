import { Fragment, useState, type ReactNode } from "react";
import { parseJsonString } from "./JsonView";
import { Link } from "react-router-dom";
import type { ParamUse } from "../lib/types";
import { Icon, StatusBadge } from "./ui";

export type Usage = Record<string, ParamUse[]>;

const hasConflict = (uses?: ParamUse[]) => !!uses?.some((u) => u.conflict);

// ParamTree renders a variant's parameters as JSON with every key clickable.
// Keys another experiment also touches are underlined; keys that can
// actually conflict are highlighted.
export function ParamTree({
  value,
  usage,
  selected,
  onSelect,
}: {
  value: Record<string, unknown>;
  usage: Usage | null;
  selected: string;
  onSelect: (path: string, anchor: HTMLElement) => void;
}) {
  const keyButton = (path: string, key: string) => {
    const uses = usage?.[path];
    const cls = ["pkey", uses?.length ? "used" : "", hasConflict(uses) ? "conflict" : "", selected === path ? "on" : ""].join(" ");
    const title = !usage
      ? "Checking other experiments…"
      : !uses?.length
      ? "No other experiment sets this"
      : hasConflict(uses)
      ? `Can conflict with ${uses.filter((u) => u.conflict).length} experiment(s) — click for details`
      : `Also used by ${uses.length} experiment(s), no conflict — click for details`;
    return (
      <button type="button" className={cls} title={title} onClick={(e) => onSelect(path, e.currentTarget)}>
        "{key}"
      </button>
    );
  };

  const render = (v: unknown, path: string, indent: number): ReactNode => {
    const pad = "  ".repeat(indent);
    if (v && typeof v === "object" && !Array.isArray(v)) {
      const entries = Object.entries(v as Record<string, unknown>);
      if (entries.length === 0) return <span className="pval">{"{}"}</span>;
      return (
        <>
          {"{"}
          {"\n"}
          {entries.map(([k, child], i) => {
            const p = path ? `${path}.${k}` : k;
            return (
              <Fragment key={k}>
                {pad}
                {"  "}
                {keyButton(p, k)}: {render(child, p, indent + 1)}
                {i < entries.length - 1 ? "," : ""}
                {"\n"}
              </Fragment>
            );
          })}
          {pad}
          {"}"}
        </>
      );
    }
    if (typeof v === "string") {
      const parsed = parseJsonString(v);
      if (parsed !== undefined) return <JsonStr raw={v} parsed={parsed} pad={pad} />;
    }
    return <span className={typeof v === "string" ? "pstr" : "pval"}>{JSON.stringify(v)}</span>;
  };

  return <pre className="ptree">{render(value, "", 0)}</pre>;
}

// JsonStr: a string that holds JSON. The JSON tag shows it decoded (marked
// as such — it's still a string to the service reading it).
function JsonStr({ raw, parsed, pad }: { raw: string; parsed: unknown; pad: string }) {
  const [open, setOpen] = useState(false);
  const tag = (
    <button type="button" className="jt-jsonstr" onClick={() => setOpen(!open)} title={open ? "Show as the string it is" : "Decode this JSON string"}>
      {open ? "▾ decoded JSON string" : "JSON ▸"}
    </button>
  );
  if (!open)
    return (
      <>
        <span className="pstr">{JSON.stringify(raw)}</span> {tag}
      </>
    );
  const body = JSON.stringify(parsed, null, 2)
    .split("\n")
    .map((l) => pad + "  " + l)
    .join("\n");
  return (
    <>
      {tag}
      {"\n"}
      <span className="ptree-decoded">{body}</span>
    </>
  );
}

const relationText: Record<ParamUse["relation"], string> = {
  same: "sets the same field",
  inside: "sets some of the same fields inside this object",
  covers: "sets a whole value above this field",
  shares_parent: "sets other fields under the same parent only",
};

// UsagePanel explains, for the selected field, who else uses it and who
// wins. It's compact: it lives in a popover next to the clicked key.
export function UsagePanel({ path, uses, rules, onClose }: { path: string; uses: ParamUse[]; rules: string[]; onClose?: () => void }) {
  const conflicts = uses.filter((u) => u.conflict);
  // Conflicts first, then the rest.
  const sorted = [...uses].sort((a, b) => Number(b.conflict) - Number(a.conflict));
  return (
    <div className="stack-sm">
      <div className="row-between" style={{ alignItems: "flex-start" }}>
        <div style={{ minWidth: 0 }}>
          <div className="faint small">Field</div>
          <div className="mono" style={{ fontSize: 13.5, wordBreak: "break-all" }}>
            {path}
          </div>
        </div>
        <div className="row" style={{ gap: 4, flexShrink: 0 }}>
          {uses.length === 0 ? (
            <span className="badge b-good">Only this experiment</span>
          ) : conflicts.length ? (
            <span className="badge b-warn">Can conflict with {conflicts.length}</span>
          ) : (
            <span className="badge">Shared, no conflict</span>
          )}
          {onClose && (
            <button className="icon-btn" aria-label="Close" onClick={onClose}>
              <Icon name="x" size={14} />
            </button>
          )}
        </div>
      </div>
      {uses.length === 0 ? (
        <p className="muted small">No other experiment that can serve (draft through launched) sets this field or anything above or below it.</p>
      ) : (
        sorted.map((u) => (
          <div key={u.experiment_id} className={`use-item ${u.conflict ? "conflict" : ""}`}>
            <div className="row-between" style={{ gap: 6 }}>
              <Link to={`/experiments/${u.experiment_id}?tab=overview`} style={{ fontWeight: 600 }}>
                {u.experiment}
              </Link>
              <StatusBadge status={u.status} />
            </div>
            <div className="small faint">
              layer {u.layer}
              {u.same_layer && " (same layer — never both for one unit)"} · {relationText[u.relation]}
            </div>
            <div className="mono small faint" style={{ wordBreak: "break-all" }}>
              {u.their_paths.join(", ")}
              {u.variants.length > 0 && <span> · variants {u.variants.join(", ")}</span>}
            </div>
            <div className="small">
              {u.conflict ? (
                <b style={{ color: u.winner === "this" ? "var(--good)" : "var(--warn)" }}>{u.winner === "this" ? "This experiment wins. " : "The other wins. "}</b>
              ) : (
                <b>No conflict. </b>
              )}
              <span className="muted">{u.reason}</span>
            </div>
          </div>
        ))
      )}
      <details>
        <summary className="small" style={{ cursor: "pointer" }}>
          How Libra decides who wins
        </summary>
        <ol className="small muted" style={{ margin: "8px 0 0", paddingLeft: 18 }}>
          {rules.map((r) => (
            <li key={r}>{r}</li>
          ))}
        </ol>
      </details>
    </div>
  );
}
