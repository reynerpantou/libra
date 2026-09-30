import { Fragment, type ReactNode } from "react";
import { Link } from "react-router-dom";
import type { ParamUse } from "../lib/types";
import { StatusBadge } from "./ui";

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
  onSelect: (path: string) => void;
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
      <button type="button" className={cls} title={title} onClick={() => onSelect(path)}>
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
    return <span className={typeof v === "string" ? "pstr" : "pval"}>{JSON.stringify(v)}</span>;
  };

  return <pre className="ptree">{render(value, "", 0)}</pre>;
}

const relationText: Record<ParamUse["relation"], string> = {
  same: "sets the same field",
  inside: "sets some of the same fields inside this object",
  covers: "sets a whole value above this field",
  shares_parent: "sets other fields under the same parent only",
};

// UsagePanel explains, for the selected field, who else uses it and who wins.
export function UsagePanel({ path, uses, rules }: { path: string; uses: ParamUse[]; rules: string[] }) {
  const conflicts = uses.filter((u) => u.conflict);
  return (
    <div className="stack">
      <div className="row-between">
        <div>
          <div className="faint small">Selected field</div>
          <div className="mono" style={{ fontSize: 14 }}>
            {path}
          </div>
        </div>
        {uses.length === 0 ? (
          <span className="badge b-good">Only this experiment</span>
        ) : conflicts.length ? (
          <span className="badge b-warn">Can conflict with {conflicts.length}</span>
        ) : (
          <span className="badge">Shared, no conflict</span>
        )}
      </div>
      {uses.length === 0 ? (
        <p className="muted small">No other experiment that can serve (draft through launched) sets this field or anything above or below it.</p>
      ) : (
        <div className="table-wrap">
          <table className="tbl tbl-compact">
            <thead>
              <tr>
                <th>Experiment</th>
                <th>How it overlaps</th>
                <th>Outcome</th>
              </tr>
            </thead>
            <tbody>
              {uses.map((u) => (
                <tr key={u.experiment_id}>
                  <td style={{ minWidth: 200 }}>
                    <Link to={`/experiments/${u.experiment_id}?tab=overview`}>{u.experiment}</Link>
                    <div className="row small faint" style={{ gap: 6, marginTop: 2 }}>
                      <StatusBadge status={u.status} /> layer {u.layer}
                      {u.same_layer && <span className="badge">same layer</span>}
                    </div>
                  </td>
                  <td className="small">
                    {relationText[u.relation]}
                    <div className="mono faint">{u.their_paths.join(", ")}</div>
                    {u.variants.length > 0 && <div className="faint">variants: {u.variants.join(", ")}</div>}
                  </td>
                  <td className="small" style={{ minWidth: 260 }}>
                    {u.conflict ? (
                      <span className={`badge ${u.winner === "this" ? "b-good" : "b-warn"}`}>{u.winner === "this" ? "Conflict — this experiment wins" : "Conflict — the other wins"}</span>
                    ) : (
                      <span className="badge">No conflict</span>
                    )}
                    <div className="muted" style={{ marginTop: 4 }}>
                      {u.reason}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
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
