import { Link } from "react-router-dom";
import type { Attribute, AttrType, Rule, Targeting } from "../lib/types";
import { Combobox } from "./Combobox";
import { Icon } from "./ui";

export const OP_LABEL: Record<string, string> = {
  eq: "=",
  neq: "≠",
  in: "is one of",
  not_in: "is not one of",
  gt: ">",
  gte: "≥",
  lt: "<",
  lte: "≤",
  version_eq: "=",
  version_gt: ">",
  version_gte: "≥",
  version_lt: "<",
  version_lte: "≤",
  exists: "is set",
  not_exists: "is not set",
};

const OPS_BY_TYPE: Record<AttrType, string[]> = {
  string: ["eq", "neq", "in", "not_in", "exists", "not_exists"],
  number: ["eq", "neq", "gt", "gte", "lt", "lte", "in", "exists", "not_exists"],
  version: ["version_gte", "version_gt", "version_lte", "version_lt", "version_eq", "exists", "not_exists"],
  boolean: ["eq", "exists", "not_exists"],
};

const multi = (op: string) => op === "in" || op === "not_in";
const noValue = (op: string) => op === "exists" || op === "not_exists";

// TargetingEditor edits OR-of-AND-groups targeting. Attributes come from the
// managed list; values offer the attribute's known options.
export function TargetingEditor({
  value,
  onChange,
  attributes,
}: {
  value: Targeting;
  onChange: (t: Targeting) => void;
  attributes: Attribute[];
}) {
  const groups = value.groups;
  const byKey = new Map(attributes.map((a) => [a.key, a]));
  const set = (g: Rule[][]) => onChange({ groups: g });
  const newRule = (): Rule => {
    const a = attributes[0];
    return { attr: a?.key ?? "", op: a ? OPS_BY_TYPE[a.type][0] : "eq", values: [] };
  };
  const setRule = (gi: number, ri: number, patch: Partial<Rule>) =>
    set(groups.map((g, i) => (i !== gi ? g : g.map((r, j) => (j === ri ? { ...r, ...patch } : r)))));
  const removeRule = (gi: number, ri: number) => {
    const next = groups.map((g, i) => (i !== gi ? g : g.filter((_, j) => j !== ri))).filter((g) => g.length > 0);
    set(next);
  };

  const attrOptions = attributes.map((a) => ({ value: a.key, label: a.key, hint: `${a.name} · ${a.type}` }));

  if (attributes.length === 0) {
    return (
      <div className="alert alert-warn small">
        No targeting attributes are registered yet. Add them under <Link to="/attributes">Targeting attributes</Link>.
      </div>
    );
  }

  return (
    <div className="stack-sm">
      {groups.length === 0 && <div className="faint small">No conditions: everyone in the experiment's traffic is eligible.</div>}
      {groups.map((g, gi) => (
        <div key={gi}>
          {gi > 0 && (
            <div className="or-divider">
              <span>OR</span>
            </div>
          )}
          <div className="tgroup">
            {g.map((r, ri) => {
              const a = byKey.get(r.attr);
              const type: AttrType = a?.type ?? "string";
              const ops = OPS_BY_TYPE[type];
              return (
                <div key={ri} className="trule">
                  <span className="and-label">{ri === 0 ? "IF" : "AND"}</span>
                  <Combobox
                    width={220}
                    options={attrOptions}
                    value={r.attr}
                    placeholder="attribute"
                    onChange={(key) => {
                      const na = byKey.get(key);
                      const nops = OPS_BY_TYPE[na?.type ?? "string"];
                      setRule(gi, ri, { attr: key, op: nops.includes(r.op) ? r.op : nops[0], values: [] });
                    }}
                  />
                  <select className="input" style={{ width: 130 }} value={r.op} onChange={(e) => setRule(gi, ri, { op: e.target.value, values: multi(e.target.value) ? r.values : r.values.slice(0, 1) })}>
                    {ops.map((op) => (
                      <option key={op} value={op}>
                        {OP_LABEL[op]}
                      </option>
                    ))}
                  </select>
                  {!noValue(r.op) && <ValueInput attr={a} rule={r} onChange={(values) => setRule(gi, ri, { values })} />}
                  <button className="icon-btn" aria-label="Remove condition" onClick={() => removeRule(gi, ri)}>
                    <Icon name="trash" size={15} />
                  </button>
                </div>
              );
            })}
            <button className="btn btn-sm btn-ghost" style={{ alignSelf: "flex-start" }} onClick={() => set(groups.map((x, i) => (i === gi ? [...x, newRule()] : x)))}>
              <Icon name="plus" /> AND condition
            </button>
          </div>
        </div>
      ))}
      <div>
        <button className="btn btn-sm" onClick={() => set([...groups, [newRule()]])}>
          <Icon name="plus" /> {groups.length === 0 ? "Add condition" : "OR group"}
        </button>
      </div>
      {groups.length > 0 && (
        <div className="small faint">
          Reads as: <span className="mono">{describeTargeting(value)}</span>
        </div>
      )}
    </div>
  );
}

function ValueInput({ attr, rule, onChange }: { attr?: Attribute; rule: Rule; onChange: (v: string[]) => void }) {
  const options = attr?.options ?? [];
  if (attr?.type === "boolean") {
    return (
      <select className="input" style={{ width: 120 }} value={rule.values[0] ?? ""} onChange={(e) => onChange([e.target.value])}>
        <option value="" disabled>
          choose
        </option>
        <option value="true">true</option>
        <option value="false">false</option>
      </select>
    );
  }
  if (multi(rule.op)) {
    const listId = `opts-${attr?.key ?? "x"}`;
    return (
      <div className="chips-input" style={{ flex: 1, minWidth: 200 }}>
        {rule.values.map((v) => (
          <span key={v} className="chip">
            {v}
            <button aria-label={`Remove ${v}`} onClick={() => onChange(rule.values.filter((x) => x !== v))}>
              ×
            </button>
          </span>
        ))}
        <input
          list={options.length ? listId : undefined}
          placeholder={rule.values.length ? "" : options.length ? "type or pick values" : "type a value, Enter"}
          onKeyDown={(e) => {
            const t = e.currentTarget;
            if ((e.key === "Enter" || e.key === ",") && t.value.trim()) {
              e.preventDefault();
              const v = t.value.trim();
              if (!rule.values.includes(v)) onChange([...rule.values, v]);
              t.value = "";
            } else if (e.key === "Backspace" && !t.value && rule.values.length) {
              onChange(rule.values.slice(0, -1));
            }
          }}
          onChange={(e) => {
            // Picking from the datalist fills the input with an exact option.
            const v = e.target.value;
            if (options.includes(v) && !rule.values.includes(v)) {
              onChange([...rule.values, v]);
              e.target.value = "";
            }
          }}
        />
        {options.length > 0 && (
          <datalist id={listId}>
            {options.filter((o) => !rule.values.includes(o)).map((o) => (
              <option key={o} value={o} />
            ))}
          </datalist>
        )}
      </div>
    );
  }
  if (options.length > 0) {
    return (
      <div style={{ flex: 1, minWidth: 160 }}>
        <Combobox options={options.map((o) => ({ value: o, label: o }))} value={rule.values[0] ?? ""} onChange={(v) => onChange([v])} placeholder="value" />
      </div>
    );
  }
  return (
    <input
      className="input"
      style={{ flex: 1, minWidth: 140 }}
      placeholder={attr?.type === "version" ? "e.g. 3.400" : "value"}
      value={rule.values[0] ?? ""}
      onChange={(e) => onChange(e.target.value.trim() ? [e.target.value.trim()] : [])}
    />
  );
}

export function describeRule(r: Rule): string {
  if (noValue(r.op)) return `${r.attr} ${OP_LABEL[r.op]}`;
  if (multi(r.op)) return `${r.attr} ${OP_LABEL[r.op]} (${r.values.join(", ")})`;
  return `${r.attr} ${OP_LABEL[r.op] ?? r.op} ${r.values[0] ?? ""}`;
}

export function describeTargeting(t: Targeting): string {
  if (!t.groups.length) return "everyone";
  return t.groups
    .map((g) => {
      const s = g.map(describeRule).join(" AND ");
      return t.groups.length > 1 && g.length > 1 ? `(${s})` : s;
    })
    .join(" OR ");
}

// TargetingView shows targeting read-only, one OR group per line.
export function TargetingView({ value }: { value: Targeting }) {
  if (!value.groups.length) return <span className="faint">Everyone</span>;
  return (
    <div className="stack-sm" style={{ gap: 4 }}>
      {value.groups.map((g, i) => (
        <div key={i} className="row" style={{ gap: 6 }}>
          {i > 0 && <span className="badge">OR</span>}
          {g.map((r, j) => (
            <span key={j} className="row" style={{ gap: 6 }}>
              {j > 0 && <span className="faint small">AND</span>}
              <span className="chip">{describeRule(r)}</span>
            </span>
          ))}
        </div>
      ))}
    </div>
  );
}
