import type { SetURLSearchParams } from "react-router-dom";
import { Combobox } from "./Combobox";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useAsync } from "../lib/hooks";

// ListFilters: platform, business and people filters for the experiment and
// tuning lists, kept in the URL (?platform=&business=&owner=&mine=1&review=me).
export function ListFilters({ params, setParams }: { params: URLSearchParams; setParams: SetURLSearchParams }) {
  const { user } = useAuth();
  const businesses = useAsync(() => api.businesses(), []);
  const users = useAsync(() => api.users(), []);
  const platform = params.get("platform") ?? "";
  const business = params.get("business") ?? "";
  const owner = params.get("owner") ?? "";
  const people = params.get("mine") === "1" ? "mine" : params.get("review") === "me" ? "review" : owner ? "owner" : "";
  const platforms = Array.from(new Map((businesses.data ?? []).map((b) => [b.platform_key, b.platform_name])).entries());
  const update = (patch: Record<string, string>) => {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    next.delete("page"); // a new filter starts at page 1
    setParams(next, { replace: true });
  };
  return (
    <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
      <select className="input" style={{ width: 160 }} value={platform} aria-label="Platform" onChange={(e) => update({ platform: e.target.value, business: "" })}>
        <option value="">All platforms</option>
        {platforms.map(([key, name]) => (
          <option key={key} value={key}>
            {name}
          </option>
        ))}
      </select>
      <select className="input" style={{ width: 170 }} value={business} aria-label="Business" onChange={(e) => update({ business: e.target.value })}>
        <option value="">All businesses</option>
        {(businesses.data ?? [])
          .filter((b) => !platform || b.platform_key === platform)
          .map((b) => (
            <option key={b.id} value={String(b.id)}>
              {platform ? b.name : `${b.name} · ${b.platform_name}`}
            </option>
          ))}
      </select>
      <div className="seg" role="group" aria-label="People">
        <button className={people === "" ? "on" : ""} onClick={() => update({ mine: "", review: "", owner: "" })}>
          Everyone
        </button>
        <button className={people === "mine" ? "on" : ""} onClick={() => update({ mine: "1", review: "", owner: "" })} title={`Owned by ${user?.display_name ?? "me"}`}>
          Owned by me
        </button>
        <button className={people === "review" ? "on" : ""} onClick={() => update({ mine: "", review: "me", owner: "" })} title="In review, waiting for your decision">
          To review
        </button>
      </div>
      <div style={{ width: 190 }}>
        <Combobox
          options={(users.data ?? []).map((u) => ({ value: String(u.id), label: u.display_name || u.username, hint: u.email ?? u.username }))}
          value={owner}
          onChange={(v) => update({ owner: v, mine: "", review: "" })}
          placeholder="Owner: anyone"
        />
      </div>
      {owner && (
        <button className="btn btn-sm btn-ghost" onClick={() => update({ owner: "" })}>
          Clear owner
        </button>
      )}
    </div>
  );
}

// filterArgs reads the filters for the list API.
export function filterArgs(params: URLSearchParams) {
  return {
    platform: params.get("platform") ?? "",
    business: params.get("business") ?? "",
    owner: params.get("owner") ?? "",
    mine: params.get("mine") ?? "",
    review: params.get("review") ?? "",
  };
}
