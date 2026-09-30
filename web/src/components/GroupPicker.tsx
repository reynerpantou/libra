import { useMemo, useState } from "react";
import type { MetricBrief, MetricGroup } from "../lib/types";
import { Pager, usePaged } from "./Pager";

// GroupPicker chooses metric groups at any scale: what's picked shows as
// chips; finding more is a search with platform and owner filters over a
// paged list. Empty groups have nothing to report and aren't offered. With
// lockDefaults, the defaults of the experiment's own business and platform
// are always in (shown, not removable).
export function GroupPicker({
  groups,
  metrics,
  value,
  onChange,
  businessId,
  platformId,
  platformName,
  lockDefaults = true,
}: {
  groups: MetricGroup[];
  metrics: MetricBrief[];
  value: number[];
  onChange: (ids: number[]) => void;
  businessId: number;
  platformId: number;
  platformName: string;
  lockDefaults?: boolean;
}) {
  const [q, setQ] = useState("");
  const [scope, setScope] = useState<"platform" | "all">(platformName ? "platform" : "all");
  const [owner, setOwner] = useState("");
  const metricName = useMemo(() => new Map(metrics.map((m) => [m.id, m.name])), [metrics]);
  const mine = (g: MetricGroup) => (g.business_id ?? 0) === businessId || (!!platformId && (g.platform_id ?? 0) === platformId);
  const isLocked = (g: MetricGroup) => lockDefaults && g.is_default && mine(g);
  const ids = (g: MetricGroup) => g.metric_ids ?? [];
  const selected = new Set(value);
  const byId = new Map(groups.map((g) => [g.id, g]));
  const locked = groups.filter(isLocked);
  const picked = value.map((id) => byId.get(id)).filter((g): g is MetricGroup => !!g && !isLocked(g));

  const inScope = groups.filter((g) => !isLocked(g) && ids(g).length > 0 && (scope === "all" || g.platform === platformName));
  const owners = Array.from(new Map(inScope.map((g) => [`${g.owner_kind}:${g.owner}`, g])).values()).sort(
    (a, b) => Number(mine(b)) - Number(mine(a)) || Number(b.owner_kind === "platform") - Number(a.owner_kind === "platform") || a.owner.localeCompare(b.owner)
  );
  const s = q.trim().toLowerCase();
  const results = inScope
    .filter((g) => !owner || `${g.owner_kind}:${g.owner}` === owner)
    .filter((g) => !s || g.name.toLowerCase().includes(s) || g.owner.toLowerCase().includes(s) || ids(g).some((id) => (metricName.get(id) ?? "").toLowerCase().includes(s)))
    .sort((a, b) => Number(mine(b)) - Number(mine(a)) || a.owner.localeCompare(b.owner) || a.name.localeCompare(b.name, undefined, { numeric: true }));
  const paged = usePaged(results, 10, `${s}|${scope}|${owner}`);
  const emptyHidden = groups.filter((g) => !isLocked(g) && ids(g).length === 0).length;
  const toggle = (id: number) => onChange(selected.has(id) ? value.filter((x) => x !== id) : [...value, id]);
  const label = (g: MetricGroup) => `${g.name} · ${g.owner}`;

  return (
    <div className="gpick">
      {locked.length > 0 && (
        <div className="chip-row">
          <span className="small faint">Always included:</span>
          {locked.map((g) => (
            <span key={g.id} className="gchip locked" title={ids(g).map((id) => metricName.get(id) ?? `#${id}`).join(", ") || "No metrics yet"}>
              {label(g)} {ids(g).length === 0 && <span className="faint">(no metrics yet)</span>}
            </span>
          ))}
        </div>
      )}
      <div className="chip-row">
        <span className="small faint">Selected ({picked.length}):</span>
        {picked.length === 0 && <span className="small faint">none</span>}
        {picked.map((g) => (
          <span key={g.id} className="gchip" title={ids(g).map((id) => metricName.get(id) ?? `#${id}`).join(", ")}>
            {label(g)}
            <button type="button" aria-label={`Remove ${g.name}`} onClick={() => toggle(g.id)}>
              ×
            </button>
          </span>
        ))}
        {picked.length > 0 && (
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => onChange(value.filter((id) => byId.get(id) && isLocked(byId.get(id)!)))}>
            Clear
          </button>
        )}
      </div>
      <div className="gpick-box">
        <div className="row" style={{ gap: 8, padding: 10 }}>
          <input className="input" style={{ flex: 1, minWidth: 200 }} placeholder="Find groups by name, owner or metric…" value={q} onChange={(e) => setQ(e.target.value)} />
          {platformName && (
            <select className="input" style={{ width: 170 }} value={scope} onChange={(e) => setScope(e.target.value as "platform" | "all")} aria-label="Scope">
              <option value="platform">{platformName} only</option>
              <option value="all">All platforms</option>
            </select>
          )}
          <select className="input" style={{ width: 190 }} value={owner} onChange={(e) => setOwner(e.target.value)} aria-label="Owner">
            <option value="">Any business or platform</option>
            {owners.map((g) => (
              <option key={`${g.owner_kind}:${g.owner}`} value={`${g.owner_kind}:${g.owner}`}>
                {g.owner} ({g.owner_kind})
              </option>
            ))}
          </select>
          <button
            type="button"
            className="btn btn-sm"
            disabled={results.length === 0}
            onClick={() => onChange(Array.from(new Set([...value, ...results.map((g) => g.id)])))}
            title="Add every group matching the filters"
          >
            Select all {results.length}
          </button>
        </div>
        {results.length === 0 ? (
          <div className="small faint" style={{ padding: "0 12px 12px" }}>
            No groups match.{scope === "platform" && " Try All platforms."}
          </div>
        ) : (
          <div className="gpick-rows">
            {paged.slice.map((g) => {
              const on = selected.has(g.id);
              return (
                <label key={g.id} className={`gpick-row ${on ? "on" : ""}`}>
                  <input type="checkbox" checked={on} onChange={() => toggle(g.id)} />
                  <span style={{ minWidth: 0, flex: 1 }}>
                    <span style={{ fontWeight: 600 }}>{g.name}</span>{" "}
                    <span className="faint small">
                      {g.owner} · {g.owner_kind}
                    </span>{" "}
                    {mine(g) && <span className="badge b-accent">this experiment's</span>}{" "}
                    {g.is_default && !mine(g) && <span className="badge">default of {g.owner}</span>}
                    <span className="faint small ellipsis">
                      {ids(g).length} metrics: {ids(g).map((id) => metricName.get(id) ?? `#${id}`).join(", ")}
                    </span>
                  </span>
                </label>
              );
            })}
          </div>
        )}
        <Pager page={paged.page} pages={paged.pages} total={paged.total} size={paged.size} onPage={paged.setPage} onSize={paged.setSize} noun="groups" />
      </div>
      {emptyHidden > 0 && <div className="small faint">{emptyHidden} empty group(s) not shown — add metrics to them to use them.</div>}
    </div>
  );
}
