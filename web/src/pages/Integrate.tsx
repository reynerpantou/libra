import { Link } from "react-router-dom";
import { useState } from "react";
import { api, BASE } from "../lib/api";
import { useAsync } from "../lib/hooks";
import { useDiversions } from "../lib/diversions";

const origin = (typeof window !== "undefined" ? window.location.origin : "https://libra.example.com") + BASE;

export default function Integrate() {
  const diversions = useDiversions();
  const platforms = useAsync(() => api.platforms(), []);
  const [pick, setPick] = useState<{ platform: string; business: string } | null>(null);
  const list = platforms.data ?? [];
  const P = pick?.platform ?? list[0]?.key ?? "shop";
  const plat = list.find((p) => p.key === P);
  const B = pick?.business ?? plat?.businesses[0]?.key ?? "search";
  const several = list.length > 1;
  const extra = diversions.filter((d) => d.key !== "user_id" && d.key !== "device_id");
  return (
    <div className="page" style={{ maxWidth: 920 }}>
      <div className="page-head">
        <div>
          <h1>Integration guide</h1>
          <p>Three calls connect a service to Libra. All use an API key from Settings, sent as <code>Authorization: Bearer lk_…</code>.</p>
        </div>
      </div>
      <div className="stack">
        <section className="card card-pad stack">
          <div className="row-between">
            <h2>Your platform and business</h2>
            <div className="row">
              <span className="small faint">Show examples for</span>
              <select className="input" style={{ width: 180 }} value={P} onChange={(e) => setPick({ platform: e.target.value, business: list.find((p) => p.key === e.target.value)?.businesses[0]?.key ?? "" })}>
                {list.map((p) => (
                  <option key={p.id} value={p.key}>
                    {p.name}
                  </option>
                ))}
              </select>
              <select className="input" style={{ width: 180 }} value={B} onChange={(e) => setPick({ platform: P, business: e.target.value })}>
                {plat?.businesses.map((b) => (
                  <option key={b.id} value={b.key}>
                    {b.name}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <p className="muted small">
            Every call names its <b>platform</b> (the app, e.g. <code>{P}</code>) and <b>business</b> (e.g. <code>{B}</code>) by key. Business keys are
            unique within a platform, so two platforms can both have <code>search</code>.
          </p>
          <ul className="small muted" style={{ margin: 0, paddingLeft: 18 }}>
            <li>
              <b>resolve</b>: <code>platform</code> is required {several ? "(there are several platforms)" : "once you have more than one platform"} — without
              it, experiments of different apps would mix. <code>business</code> is optional: leave it out to get every experiment of the platform (a
              whole-app config), or set it to get only that business's.
            </li>
            <li>
              <b>events</b>: <code>business</code> is required; add <code>platform</code> when the business key exists on more than one platform.
            </li>
            <li>A request with an unknown or ambiguous key is rejected with a message saying which.</li>
            <li>
              <b>params</b> are namespaced by the platform key: everything a {plat?.name ?? "platform"} experiment serves is under{" "}
              <code>{`"${P}": {...}`}</code>, so platforms never overwrite each other's fields.
            </li>
          </ul>
          {list.length > 0 && (
            <details>
              <summary className="small" style={{ cursor: "pointer" }}>
                All platform and business keys
              </summary>
              <table className="tbl tbl-compact" style={{ marginTop: 8 }}>
                <thead>
                  <tr>
                    <th>Platform</th>
                    <th>platform key</th>
                    <th>business keys</th>
                  </tr>
                </thead>
                <tbody>
                  {list.map((p) => (
                    <tr key={p.id}>
                      <td>{p.name}</td>
                      <td className="mono">{p.key}</td>
                      <td className="mono small">{p.businesses.map((b) => b.key).join(", ") || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </details>
          )}
        </section>
        <section className="card card-pad stack">
          <h2>1. Ask Libra what a user gets</h2>
          <p className="muted">
            Call <code>resolve</code> when serving a request. It returns merged parameters from every experiment the unit is in (and fully launched
            configs), and logs an exposure for each experiment. Needs a key with the <b>runtime</b> scope.
          </p>
          <pre className="code">{`curl -X POST ${origin}/api/v1/resolve \\
  -H "Authorization: Bearer $LIBRA_KEY" -H "Content-Type: application/json" \\
  -d '{"platform": "${P}", "business": "${B}", "user_id": "user-42", "device_id": "dev-9f3a",
       "attrs": {"region": "ID", "os": "android", "app_version": "10.3.0"}}'

{
  "params": {"${P}": {"search": {"ranking": {"formula": "ctr * cvr * price_score", "price_boost": 0.3}}}},
  "hits": [{"experiment_id": 1, "experiment": "Ranking formula v2", "variant_id": 2,
            "variant": "treatment", "source": "experiment", "unit_type": "user_id"}],
  "platform": "${P}", "business": "${B}",
  "snapshot_version": 12
}`}</pre>
          <p className="muted small">
            Send both <code>user_id</code> and <code>device_id</code> when you have them: each layer splits traffic by one of them (its{" "}
            <i>diversion</i>), and an experiment whose id is missing from the request is skipped.
            {extra.length > 0 && (
              <>
                {" "}
                Other diversions go in an <code>ids</code> object: <code>{`"ids": {${extra.map((d) => `"${d.key}": "…"`).join(", ")}}`}</code>.
              </>
            )}{" "}
            Current diversions:{" "}
            {diversions.map((d, i) => (
              <span key={d.key}>
                {i > 0 && ", "}
                <code>{d.key}</code> ({d.name})
              </span>
            ))}
            . <code>attrs</code> are matched against targeting rules and kept (up to 10 short values) for report breakdowns. Pass{" "}
            <code>"log_exposure": false</code> to prefetch without logging, then report the exposure when the user actually sees the change:
          </p>
          <pre className="code">{`curl -X POST ${origin}/api/v1/exposures -H "Authorization: Bearer $LIBRA_KEY" -H "Content-Type: application/json" \\
  -d '{"exposures": [{"experiment_id": 1, "variant_id": 2, "user_id": "user-42", "attrs": {"region": "ID"}}]}'`}</pre>
        </section>

        <section className="card card-pad stack">
          <h2>2. Send business events</h2>
          <p className="muted">
            Push the raw events your metrics are made of — searches, clicks, orders — in batches of up to 5,000. Each has a business key, an event name,
            the <code>user_id</code> and/or <code>device_id</code> (and <code>ids</code> for other diversions), an optional numeric <code>value</code> and optional <code>props</code>. Needs the <b>ingest</b> scope. Timestamps up to 30 days
            old are accepted for backfills.
          </p>
          <pre className="code">{`curl -X POST ${origin}/api/v1/events -H "Authorization: Bearer $LIBRA_KEY" -H "Content-Type: application/json" \\
  -d '{"events": [
    {"platform": "${P}", "business": "${B}", "event": "search", "user_id": "user-42", "device_id": "dev-9f3a", "props": {"impressions": 20}},
    {"platform": "${P}", "business": "${B}", "event": "search_click", "user_id": "user-42", "device_id": "dev-9f3a"},
    {"platform": "${P}", "business": "${B}", "event": "order", "user_id": "user-42", "device_id": "dev-9f3a", "value": 35.90,
     "ts": "2026-09-30T08:15:00Z", "props": {"source": "search", "is_ads": true}}
  ]}'`}</pre>
        </section>

        <section className="card card-pad stack">
          <h2>3. Define measures and metrics</h2>
          <p className="muted">
            In <Link to="/businesses">Businesses & metrics</Link>, turn events into <b>measures</b> (e.g. <i>search_gmv = sum of value of order where
            source = search</i>), then write <b>metrics</b> as formulas over them:
          </p>
          <table className="tbl tbl-compact">
            <thead>
              <tr>
                <th>Metric</th>
                <th>Formula</th>
              </tr>
            </thead>
            <tbody>
              {[
                ["Search GMV / User", "search_gmv / users"],
                ["Search GMV", "search_gmv"],
                ["Search Ads GMV", "search_ads_gmv"],
                ["CTR", "search_clicks / search_impressions"],
                ["CVR", "search_orders / search_clicks"],
                ["Ads share", "ads_gmv / gmv   (metrics can use other metrics)"],
              ].map(([a, b]) => (
                <tr key={a}>
                  <td>{a}</td>
                  <td className="mono">{b}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="muted small">
            Every unit's measures are counted from the day of its first exposure. Reports compute each formula per variant with delta-method confidence
            intervals, so ratios like CTR get correct error bars.
          </p>
        </section>

        <section className="card card-pad stack-sm">
          <h2>Go client</h2>
          <pre className="code">{`import "github.com/reynerpantou/libra/pkg/client"

c := client.New("${origin}", os.Getenv("LIBRA_KEY"))
res, err := c.Resolve(ctx, client.ResolveRequest{Platform: "${P}", Business: "${B}", UserID: "user-42", DeviceID: "dev-9f3a",
    Attrs: map[string]any{"region": "ID"}})
formula := res.String("${P}.search.ranking.formula", "ctr * cvr")

c.Track(client.Event{Platform: "${P}", Business: "${B}", Event: "order", UserID: "user-42", DeviceID: "dev-9f3a", Value: 35.9,
    Props: map[string]any{"source": "search"}})   // batched in the background
defer c.Close()                                     // flushes pending events`}</pre>
        </section>
      </div>
    </div>
  );
}
