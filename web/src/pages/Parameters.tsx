import { Fragment, useMemo, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Empty, ErrorBox, Loading, Popover, Segmented, StatusBadge } from "../components/ui";
import { api } from "../lib/api";
import { fmtDate, trafficPct } from "../lib/format";
import { useAsync } from "../lib/hooks";
import type { LaunchedField, ParamValue } from "../lib/types";

type View = "tree" | "fields";
type StatusFilter = "all" | "active" | "launched";

interface Node {
  name: string;
  path: string;
  children: Map<string, Node>;
  values: ParamValue[]; // leaf values at exactly this path
  count: number; // values in the subtree
  exps: Set<number>;
}

const newNode = (name: string, path: string): Node => ({ name, path, children: new Map(), values: [], count: 0, exps: new Set() });

function buildTree(list: ParamValue[]): Node {
  const root = newNode("", "");
  for (const v of list) {
    let n = root;
    n.count++;
    n.exps.add(v.experiment_id);
    const parts = v.path.split(".");
    parts.forEach((p, i) => {
      const path = parts.slice(0, i + 1).join(".");
      if (!n.children.has(p)) n.children.set(p, newNode(p, path));
      n = n.children.get(p)!;
      n.count++;
      n.exps.add(v.experiment_id);
    });
    n.values.push(v);
  }
  return root;
}

const show = (v: unknown) => JSON.stringify(v);

// Parameters lists everything currently served — launched configs and
// running experiments' variants — as a tree of the JSON or as flat fields,
// each linked to the experiment that sets it.
export default function Parameters() {
  const [params, setParams] = useSearchParams();
  const source = (params.get("view") as "live" | "launched") || "live";
  const platforms = useAsync(() => api.platforms(), []);
  const list = platforms.data ?? [];
  // One platform: nothing to choose. Several: pick one (or all).
  const platform = params.get("platform") ?? (list.length === 1 ? list[0].key : "");
  const business = params.get("business") ?? "";
  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    if (k === "platform") next.delete("business");
    setParams(next, { replace: true });
  };
  const plat = list.find((p) => p.key === platform);
  const picker = (
    <div className="row" style={{ gap: 8 }}>
      <select className="input" style={{ width: 190 }} value={platform} onChange={(e) => set("platform", e.target.value)} aria-label="Platform">
        <option value="">All platforms</option>
        {list.map((p) => (
          <option key={p.id} value={p.key}>
            {p.name} ({p.key})
          </option>
        ))}
      </select>
      <select className="input" style={{ width: 190 }} value={business} onChange={(e) => set("business", e.target.value)} disabled={!plat} aria-label="Business">
        <option value="">All businesses</option>
        {plat?.businesses.map((b) => (
          <option key={b.id} value={b.key}>
            {b.name} ({b.key})
          </option>
        ))}
      </select>
    </div>
  );
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Parameters</h1>
          <p>
            <b>Live</b>: everything served right now — running experiments' variants and launched values, each linked to its experiment.{" "}
            <b>Launched config</b>: the released defaults per business (what everyone gets without experiments), where each value came from, and
            the launch history.
          </p>
        </div>
        <Segmented<"live" | "launched">
          options={[
            ["live", "Live now"],
            ["launched", "Launched config"],
          ]}
          value={source}
          onChange={(v) => set("view", v)}
        />
      </div>
      {source === "live" ? (
        <Live platform={platform} business={business} picker={picker} />
      ) : (
        <Launched platform={platform} business={business} picker={picker} />
      )}
    </div>
  );
}

function Live({ platform, business, picker }: { platform: string; business: string; picker: ReactNode }) {
  const list = useAsync(() => api.parameters(), []);
  const [view, setView] = useState<View>("tree");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<Set<string>>(new Set());
  const all = list.data ?? [];
  const filtered = useMemo(() => {
    const s = q.trim().toLowerCase();
    return all.filter(
      (v) =>
        (status === "all" || v.status === status) &&
        (!platform || v.platform === platform) &&
        (!business || v.business === business) &&
        (!s || v.path.toLowerCase().includes(s) || v.experiment.toLowerCase().includes(s) || show(v.value).toLowerCase().includes(s))
    );
  }, [all, q, status, platform, business]);
  const tree = useMemo(() => buildTree(filtered), [filtered]);
  const byPath = useMemo(() => {
    const m = new Map<string, ParamValue[]>();
    for (const v of filtered) {
      if (!m.has(v.path)) m.set(v.path, []);
      m.get(v.path)!.push(v);
    }
    return m;
  }, [filtered]);
  // Searching opens everything.
  const isOpen = (path: string) => !!q.trim() || open.has(path);
  const toggle = (path: string) => {
    const next = new Set(open);
    if (next.has(path)) next.delete(path);
    else next.add(path);
    setOpen(next);
  };
  const expandAll = () => {
    const next = new Set<string>();
    const walk = (n: Node) => n.children.forEach((c) => (next.add(c.path), walk(c)));
    walk(tree);
    setOpen(next);
  };

  const paths = Array.from(byPath.keys()).sort();
  const nExps = new Set(filtered.map((v) => v.experiment_id)).size;
  return (
    <div className="stack">
      <div className="card card-pad">
        <div className="row" style={{ gap: 10 }}>
          <input className="input" style={{ flex: 1, minWidth: 220 }} placeholder="Search a field, value or experiment…" value={q} onChange={(e) => setQ(e.target.value)} />
          {picker}
          <Segmented<StatusFilter>
            options={[
              ["all", "All"],
              ["launched", "Launched"],
              ["active", "Running"],
            ]}
            value={status}
            onChange={setStatus}
          />
          <Segmented<View>
            options={[
              ["tree", "Tree"],
              ["fields", "Fields"],
            ]}
            value={view}
            onChange={setView}
          />
        </div>
        <div className="small faint" style={{ marginTop: 8 }}>
          {paths.length} fields · {nExps} experiments · {filtered.length} values
        </div>
      </div>
      <ErrorBox error={list.error} />
      {list.loading && !list.data ? (
        <Loading />
      ) : filtered.length === 0 ? (
        <div className="card">
          <Empty title={all.length ? "Nothing matches" : "Nothing is served yet"}>
            <p>Start or launch an experiment and its parameters show up here.</p>
          </Empty>
        </div>
      ) : view === "tree" ? (
        <section className="card">
          <div className="card-head">
            <h2>Tree</h2>
            <div className="row" style={{ gap: 6 }}>
              <button className="btn btn-sm" onClick={expandAll}>
                Expand all
              </button>
              <button className="btn btn-sm" onClick={() => setOpen(new Set())}>
                Collapse
              </button>
            </div>
          </div>
          <div className="ptree-page">
            {Array.from(tree.children.values())
              .sort((a, b) => a.name.localeCompare(b.name))
              .map((n) => (
                <TreeNode key={n.path} n={n} depth={0} isOpen={isOpen} toggle={toggle} />
              ))}
          </div>
        </section>
      ) : (
        <section className="card">
          <div className="table-wrap">
            <table className="tbl tbl-compact">
              <thead>
                <tr>
                  <th>Field</th>
                  <th>Value</th>
                  <th>Experiment</th>
                  <th>Variant</th>
                  <th>Layer</th>
                </tr>
              </thead>
              <tbody>
                {paths.map((p) => {
                  const vs = byPath.get(p)!;
                  return (
                    <Fragment key={p}>
                      {vs.map((v, i) => (
                        <tr key={`${v.experiment_id}-${v.variant_key}`} style={i === 0 ? { borderTop: "2px solid var(--border)" } : undefined}>
                          {i === 0 && (
                            <td rowSpan={vs.length} className="mono" style={{ verticalAlign: "top", fontWeight: 600 }}>
                              {p}
                              {new Set(vs.map((x) => x.experiment_id)).size > 1 && (
                                <div className="small faint" style={{ fontWeight: 400 }}>
                                  set by {new Set(vs.map((x) => x.experiment_id)).size} experiments
                                </div>
                              )}
                            </td>
                          )}
                          <ValueCells v={v} />
                        </tr>
                      ))}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
}

function ValueCells({ v }: { v: ParamValue }) {
  return (
    <>
      <td className="mono" style={{ color: typeof v.value === "string" ? "var(--good)" : undefined, maxWidth: 320, wordBreak: "break-all" }}>
        {show(v.value)}
        {v.default && (
          <span className="badge b-accent" style={{ marginLeft: 6 }} title="The launched value everyone gets unless a running experiment overrides it">
            default
          </span>
        )}
      </td>
      <td>
        <Link to={`/experiments/${v.experiment_id}?tab=overview`}>{v.experiment}</Link>
        <div className="row small faint" style={{ gap: 6 }}>
          <StatusBadge status={v.status} /> {v.platform} › {v.business}
        </div>
      </td>
      <td className="nowrap">
        {v.variant_name || v.variant_key} {v.is_control && <span className="badge">control</span>}
      </td>
      <td className="small nowrap">
        {v.layer_auto ? "dedicated" : v.layer} <span className="faint">· {v.diversion}</span>
        {v.status === "active" && <div className="faint">{trafficPct(v.traffic)} traffic</div>}
      </td>
    </>
  );
}

function TreeNode({ n, depth, isOpen, toggle }: { n: Node; depth: number; isOpen: (p: string) => boolean; toggle: (p: string) => void }) {
  const kids = Array.from(n.children.values()).sort((a, b) => a.name.localeCompare(b.name));
  const leaf = kids.length === 0;
  const open = isOpen(n.path);
  return (
    <div>
      <button className="tnode" style={{ paddingLeft: 12 + depth * 18 }} onClick={() => toggle(n.path)}>
        <span className="caret">{open ? "▾" : "▸"}</span>
        <span className="mono" style={{ fontWeight: 600 }}>
          {n.name}
        </span>
        {leaf ? (
          <span className="small faint mono" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {Array.from(new Set(n.values.map((v) => show(v.value)))).slice(0, 4).join(" | ")}
          </span>
        ) : (
          <span className="small faint">{kids.length} keys</span>
        )}
        <span className="spacer" />
        <span className="small faint nowrap">
          {n.exps.size} exp{n.exps.size === 1 ? "" : "s"}
        </span>
      </button>
      {open &&
        (leaf ? (
          <div className="table-wrap" style={{ marginLeft: 30 + depth * 18, marginRight: 12, marginBottom: 8 }}>
            <table className="tbl tbl-compact">
              <tbody>
                {n.values.map((v) => (
                  <tr key={`${v.experiment_id}-${v.variant_key}`}>
                    <ValueCells v={v} />
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <>
            {kids.map((c) => (
              <TreeNode key={c.path} n={c} depth={depth + 1} isOpen={isOpen} toggle={toggle} />
            ))}
            {n.values.length > 0 && (
              <div style={{ marginLeft: 30 + depth * 18 }} className="small faint">
                also set as a whole value by {n.values.length}
              </div>
            )}
          </>
        ))}
    </div>
  );
}

// ---------- launched config ----------

interface LNode {
  name: string;
  path: string;
  children: Map<string, LNode>;
  field?: LaunchedField;
}

function launchedTree(fields: LaunchedField[]): LNode {
  const root: LNode = { name: "", path: "", children: new Map() };
  for (const f of fields) {
    let n = root;
    const parts = f.path.split(".");
    parts.forEach((p, i) => {
      if (!n.children.has(p)) n.children.set(p, { name: p, path: parts.slice(0, i + 1).join("."), children: new Map() });
      n = n.children.get(p)!;
    });
    n.field = f;
  }
  return root;
}

function Launched({ platform, business, picker }: { platform: string; business: string; picker: ReactNode }) {
  const data = useAsync(() => api.launchedConfig(), []);
  const [view, setView] = useState<"json" | "fields">("json");
  const [q, setQ] = useState("");
  const [sel, setSel] = useState<{ f: LaunchedField; anchor: HTMLElement } | null>(null);
  const bs = (data.data?.businesses ?? []).filter((b) => (!platform || b.platform === platform) && (!business || b.business === business));
  const [biz, setBiz] = useState("");
  const cur = bs.find((b) => b.key === biz) ?? bs[0];
  const query = q.trim().toLowerCase();
  const fields = (cur?.fields ?? []).filter(
    (f) => !query || f.path.toLowerCase().includes(query) || f.experiment.toLowerCase().includes(query) || show(f.value).toLowerCase().includes(query)
  );
  const history = (data.data?.history ?? []).filter((h) => !cur || (h.platform === cur.platform && h.business === cur.business));

  const renderNode = (n: LNode, indent: number): ReactNode => {
    const pad = "  ".repeat(indent);
    const kids = Array.from(n.children.values()).sort((a, b) => a.name.localeCompare(b.name));
    return (
      <>
        {"{"}
        {"\n"}
        {kids.map((c, i) => (
          <Fragment key={c.path}>
            {pad}
            {"  "}
            {c.field ? (
              <button
                type="button"
                className={`pkey ${c.field.replaced.length ? "used" : ""} ${sel?.f.path === c.path ? "on" : ""}`}
                title={`from ${c.field.experiment} — click for details`}
                onClick={(e) => setSel(sel?.f.path === c.path ? null : { f: c.field!, anchor: e.currentTarget })}
              >
                "{c.name}"
              </button>
            ) : (
              <span className="pkey" style={{ cursor: "default" }}>
                "{c.name}"
              </span>
            )}
            : {c.field ? <span className={typeof c.field.value === "string" ? "pstr" : "pval"}>{show(c.field.value)}</span> : renderNode(c, indent + 1)}
            {c.field && (c.field.rollout < 1000 || c.field.targeted) && (
              <span className="faint" style={{ fontFamily: "var(--sans)", fontSize: 11 }}>
                {"  "}
                {c.field.rollout < 1000 && `· ${trafficPct(c.field.rollout)} of units`} {c.field.targeted && "· targeted"}
              </span>
            )}
            {i < kids.length - 1 ? "," : ""}
            {"\n"}
          </Fragment>
        ))}
        {pad}
        {"}"}
      </>
    );
  };

  if (data.loading && !data.data) return <Loading />;
  return (
    <div className="stack">
      <ErrorBox error={data.error} />
      <div className="card card-pad">
        <div className="row" style={{ gap: 10 }}>
          {picker}
          {bs.length > 1 && (
            <select className="input" style={{ width: 240 }} value={cur?.key} onChange={(e) => setBiz(e.target.value)} aria-label="Launched config of">
              {bs.map((b) => (
                <option key={b.key} value={b.key}>
                  {b.name} ({b.fields.length} fields)
                </option>
              ))}
            </select>
          )}
          <input className="input" style={{ flex: 1, minWidth: 200 }} placeholder="Search a field, value or experiment…" value={q} onChange={(e) => setQ(e.target.value)} />
          <Segmented<"json" | "fields">
            options={[
              ["json", "JSON"],
              ["fields", "Fields"],
            ]}
            value={view}
            onChange={setView}
          />
        </div>
        {cur && (
            <p className="small faint" style={{ marginTop: 8 }}>
              The default config of <b>{cur?.name}</b>: launched variants merged in launch order (a later launch replaces the same field). Running
              experiments can still override it for their units. Fields marked with a share are still rolling out.
            </p>
        )}
      </div>
      {bs.length === 0 ? (
        <div className="card">
          <Empty title={platform ? "Nothing launched here yet" : "Nothing launched yet"}>
            <p>When an experiment is launched, its winning variant's parameters become the default here.</p>
          </Empty>
        </div>
      ) : (
        <>
          {view === "json" ? (
            <section className="card card-pad">
              <pre className="ptree">{renderNode(launchedTree(fields), 0)}</pre>
              <p className="small faint">
                Click a key to see which launch set it. <span className="ptree" style={{ padding: "0 4px", display: "inline" }}><span className="pkey used">Underlined</span></span> keys replaced
                an earlier launch.
              </p>
            </section>
          ) : (
            <section className="card">
              <div className="table-wrap">
                <table className="tbl tbl-compact">
                  <thead>
                    <tr>
                      <th>Field</th>
                      <th>Value</th>
                      <th>Launched by</th>
                      <th>Since</th>
                      <th>Reach</th>
                      <th>Replaced</th>
                    </tr>
                  </thead>
                  <tbody>
                    {fields.map((f) => (
                      <tr key={f.path}>
                        <td className="mono" style={{ fontWeight: 600 }}>
                          {f.path}
                        </td>
                        <td className="mono" style={{ color: typeof f.value === "string" ? "var(--good)" : undefined }}>
                          {show(f.value)}
                        </td>
                        <td>
                          <Link to={`/experiments/${f.experiment_id}?tab=overview`}>{f.experiment}</Link>
                          <div className="small faint">variant {f.variant}</div>
                        </td>
                        <td className="nowrap small">{fmtDate(f.launched_at)}</td>
                        <td className="small nowrap">
                          {f.rollout < 1000 ? <span className="badge b-warn">{trafficPct(f.rollout)} rolling out</span> : "everyone"}
                          {f.targeted && <div className="faint">targeted units only</div>}
                        </td>
                        <td className="small">
                          {f.replaced.map((r, i) => (
                            <div key={i} className="faint">
                              <span className="mono">{show(r.value)}</span> from{" "}
                              <Link to={`/experiments/${r.experiment_id}?tab=overview`}>{r.experiment}</Link>
                            </div>
                          ))}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          )}
        </>
      )}
      {sel && (
        <Popover anchor={sel.anchor} onClose={() => setSel(null)} width={440}>
          <div className="stack-sm">
            <div className="mono" style={{ wordBreak: "break-all" }}>
              {sel.f.path} = <b>{show(sel.f.value)}</b>
            </div>
            <div className="small">
              Launched by <Link to={`/experiments/${sel.f.experiment_id}?tab=overview`}>{sel.f.experiment}</Link> (variant {sel.f.variant}) on{" "}
              {fmtDate(sel.f.launched_at)} · {sel.f.rollout < 1000 ? `rolling out: ${trafficPct(sel.f.rollout)} of units` : "all units"}
              {sel.f.targeted && ", targeted units only"}
            </div>
            {sel.f.replaced.length > 0 && (
              <div className="small">
                <b>Replaced</b>
                {sel.f.replaced.map((r, i) => (
                  <div key={i} className="faint">
                    <span className="mono">
                      {r.path} = {show(r.value)}
                    </span>{" "}
                    from <Link to={`/experiments/${r.experiment_id}?tab=overview`}>{r.experiment}</Link> ({fmtDate(r.launched_at)})
                  </div>
                ))}
              </div>
            )}
          </div>
        </Popover>
      )}
      {history.length > 0 && (
        <section className="card">
          <div className="card-head">
            <div>
              <h2>Launch history{cur ? ` — ${cur.name}` : ""}</h2>
              <p>Every launch, newest first. Retired launches (archived) no longer serve.</p>
            </div>
          </div>
          <div className="table-wrap">
            <table className="tbl tbl-compact">
              <thead>
                <tr>
                  <th>Launched</th>
                  <th>Experiment</th>
                  <th>Variant</th>
                  <th>Reach</th>
                  <th>Fields in effect</th>
                  <th>Parameters</th>
                </tr>
              </thead>
              <tbody>
                {history.map((h) => (
                  <tr key={h.experiment_id}>
                    <td className="nowrap small">{fmtDate(h.launched_at)}</td>
                    <td>
                      <Link to={`/experiments/${h.experiment_id}?tab=overview`}>{h.experiment}</Link>
                      <div>
                        <StatusBadge status={h.status} />
                      </div>
                    </td>
                    <td className="small">{h.variant_name || h.variant}</td>
                    <td className="small nowrap">
                      {h.status !== "launched" ? "retired" : h.rollout < 1000 ? `${trafficPct(h.rollout)} (rolling out)` : "everyone"}
                      {h.targeted && <div className="faint">targeted</div>}
                    </td>
                    <td className="small">
                      {h.status === "launched" ? (
                        <>
                          {h.live_fields} of {h.fields}
                          {h.live_fields < h.fields && <div className="faint">rest replaced by later launches</div>}
                        </>
                      ) : (
                        <span className="faint">—</span>
                      )}
                    </td>
                    <td className="mono small" style={{ maxWidth: 360, wordBreak: "break-all" }}>
                      {JSON.stringify(h.params)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
}
