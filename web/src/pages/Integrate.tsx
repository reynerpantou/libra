import { Link } from "react-router-dom";
import { BASE } from "../lib/api";
import { useDiversions } from "../lib/diversions";

const origin = (typeof window !== "undefined" ? window.location.origin : "https://libra.example.com") + BASE;

export default function Integrate() {
  const diversions = useDiversions();
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
          <h2>1. Ask Libra what a user gets</h2>
          <p className="muted">
            Call <code>resolve</code> when serving a request. It returns merged parameters from every experiment the unit is in (and fully launched
            configs), and logs an exposure for each experiment. Needs a key with the <b>runtime</b> scope.
          </p>
          <pre className="code">{`curl -X POST ${origin}/api/v1/resolve \\
  -H "Authorization: Bearer $LIBRA_KEY" -H "Content-Type: application/json" \\
  -d '{"user_id": "user-42", "device_id": "dev-9f3a", "business": "search",
       "attrs": {"region": "ID", "os": "android", "app_version": "10.3.0"}}'

{
  "params": {"search": {"ranking": {"formula": "ctr * cvr * price_score", "price_boost": 0.3}}},
  "hits": [{"experiment_id": 1, "experiment": "Ranking formula v2", "variant_id": 2,
            "variant": "treatment", "source": "experiment", "unit_type": "user_id"}],
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
    {"business": "search", "event": "search", "user_id": "user-42", "device_id": "dev-9f3a", "props": {"impressions": 20}},
    {"business": "search", "event": "search_click", "user_id": "user-42", "device_id": "dev-9f3a"},
    {"business": "search", "event": "order", "user_id": "user-42", "device_id": "dev-9f3a", "value": 35.90,
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
res, err := c.Resolve(ctx, client.ResolveRequest{UserID: "user-42", DeviceID: "dev-9f3a", Business: "search",
    Attrs: map[string]any{"region": "ID"}})
formula := res.String("search.ranking.formula", "ctr * cvr")

c.Track(client.Event{Business: "search", Event: "order", UserID: "user-42", DeviceID: "dev-9f3a", Value: 35.9,
    Props: map[string]any{"source": "search"}})   // batched in the background
defer c.Close()                                     // flushes pending events`}</pre>
        </section>
      </div>
    </div>
  );
}
