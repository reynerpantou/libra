import { Fragment, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Empty, ErrorBox, Loading, Segmented, StatusBadge } from "../components/ui";
import { api } from "../lib/api";
import { trafficPct } from "../lib/format";
import { useAsync } from "../lib/hooks";
import type { ParamValue } from "../lib/types";

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
  const list = useAsync(() => api.parameters(), []);
  const [view, setView] = useState<View>("tree");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [q, setQ] = useState("");
  const [business, setBusiness] = useState("");
  const [open, setOpen] = useState<Set<string>>(new Set());
  const all = list.data ?? [];
  const businesses = useMemo(() => Array.from(new Set(all.map((v) => v.business))).sort(), [all]);
  const filtered = useMemo(() => {
    const s = q.trim().toLowerCase();
    return all.filter(
      (v) =>
        (status === "all" || v.status === status) &&
        (!business || v.business === business) &&
        (!s || v.path.toLowerCase().includes(s) || v.experiment.toLowerCase().includes(s) || show(v.value).toLowerCase().includes(s))
    );
  }, [all, q, status, business]);
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
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Parameters</h1>
          <p>
            Everything Libra serves right now: <b>launched</b> configs (the default for everyone) and the variants of <b>running</b> experiments, each
            linked to its experiment. Browse the JSON as a tree or as flat fields.
          </p>
        </div>
      </div>
      <div className="card card-pad">
        <div className="row" style={{ gap: 10 }}>
          <input className="input" style={{ flex: 1, minWidth: 220 }} placeholder="Search a field, value or experiment…" value={q} onChange={(e) => setQ(e.target.value)} />
          <select className="input" style={{ width: 180 }} value={business} onChange={(e) => setBusiness(e.target.value)}>
            <option value="">All businesses</option>
            {businesses.map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </select>
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
          <StatusBadge status={v.status} /> {v.business}
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
