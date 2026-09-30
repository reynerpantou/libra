import { useMemo, useState } from "react";
import type { MetricBrief, MetricGroup } from "../lib/types";

// GroupPicker chooses an experiment's metric groups from every business and
// platform. Default groups of the experiment's own business and platform are
// always included and shown locked.
export function GroupPicker({
  groups,
  metrics,
  value,
  onChange,
  businessId,
  platformId,
}: {
  groups: MetricGroup[];
  metrics: MetricBrief[];
  value: number[];
  onChange: (ids: number[]) => void;
  businessId: number;
  platformId: number;
}) {
  const [q, setQ] = useState("");
  const metricName = useMemo(() => new Map(metrics.map((m) => [m.id, m.name])), [metrics]);
  const isAuto = (g: MetricGroup) => g.is_default && ((g.business_id ?? 0) === businessId || ((g.platform_id ?? 0) === platformId && !!platformId));
  const selected = new Set(value);

  // Owners in order: this experiment's platform & business first.
  const owners = useMemo(() => {
    const by = new Map<string, { key: string; label: string; kind: string; mine: boolean; groups: MetricGroup[] }>();
    for (const g of groups) {
      const key = `${g.owner_kind}:${g.business_id ?? g.platform_id}`;
      const mine = (g.business_id ?? 0) === businessId || ((g.platform_id ?? 0) === platformId && !!platformId);
      if (!by.has(key)) by.set(key, { key, label: g.owner, kind: g.owner_kind, mine, groups: [] });
      by.get(key)!.groups.push(g);
    }
    return Array.from(by.values()).sort((a, b) => Number(b.mine) - Number(a.mine) || Number(b.kind === "platform") - Number(a.kind === "platform") || a.label.localeCompare(b.label));
  }, [groups, businessId, platformId]);

  const match = (g: MetricGroup) => {
    if (!q.trim()) return true;
    const s = q.toLowerCase();
    return (
      g.name.toLowerCase().includes(s) ||
      g.owner.toLowerCase().includes(s) ||
      g.metric_ids.some((id) => (metricName.get(id) ?? "").toLowerCase().includes(s))
    );
  };
  const toggle = (id: number) => onChange(selected.has(id) ? value.filter((x) => x !== id) : [...value, id]);
  const setMany = (ids: number[], on: boolean) => onChange(on ? Array.from(new Set([...value, ...ids])) : value.filter((x) => !ids.includes(x)));
  const pickable = groups.filter((g) => !isAuto(g));
  const count = new Set([...value, ...groups.filter(isAuto).map((g) => g.id)]).size;

  return (
    <div className="gpick">
      <div className="row" style={{ gap: 8 }}>
        <input className="input" style={{ flex: 1, minWidth: 180 }} placeholder="Search groups, businesses or metrics…" value={q} onChange={(e) => setQ(e.target.value)} />
        <span className="small faint nowrap">{count} groups in the report</span>
        <button type="button" className="btn btn-sm" onClick={() => setMany(pickable.filter(match).map((g) => g.id), true)}>
          Select all{q ? " shown" : ""}
        </button>
        <button type="button" className="btn btn-sm" onClick={() => onChange([])} disabled={value.length === 0}>
          Clear
        </button>
      </div>
      <div className="gpick-list">
        {owners.map((o) => {
          const shown = o.groups.filter(match);
          if (shown.length === 0) return null;
          const ids = shown.filter((g) => !isAuto(g)).map((g) => g.id);
          const all = ids.length > 0 && ids.every((id) => selected.has(id));
          return (
            <div key={o.key} className="gpick-owner">
              <div className="row-between">
                <div className="small" style={{ fontWeight: 600 }}>
                  {o.label} <span className="faint">· {o.kind}</span> {o.mine && <span className="badge b-accent">this experiment</span>}
                </div>
                {ids.length > 1 && (
                  <button type="button" className="btn btn-ghost btn-sm" onClick={() => setMany(ids, !all)}>
                    {all ? "Unselect all" : "Select all"}
                  </button>
                )}
              </div>
              <div className="gpick-items">
                {shown.map((g) => {
                  const auto = isAuto(g);
                  const on = auto || selected.has(g.id);
                  return (
                    <label key={g.id} className={`gpick-item ${on ? "on" : ""}`} title={g.metric_ids.map((id) => metricName.get(id) ?? `#${id}`).join(", ")}>
                      <input type="checkbox" checked={on} disabled={auto} onChange={() => toggle(g.id)} />
                      <span style={{ minWidth: 0 }}>
                        <span style={{ fontWeight: 600 }}>{g.name}</span>{" "}
                        {auto ? <span className="badge b-good">default · always</span> : g.is_default ? <span className="badge">default there</span> : null}
                        <span className="faint small" style={{ display: "block", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                          {g.metric_ids.length} metrics: {g.metric_ids.map((id) => metricName.get(id) ?? `#${id}`).join(", ")}
                        </span>
                      </span>
                    </label>
                  );
                })}
              </div>
            </div>
          );
        })}
        {groups.length === 0 && <div className="faint small">No metric groups yet — the report shows all of the business's metrics.</div>}
      </div>
    </div>
  );
}
