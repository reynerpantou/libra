# Libra

An experimentation platform: design A/B tests, split traffic safely, collect
business events, and read reports built from **metrics you define as
formulas**. For example, `search_gmv / users` or
`search_clicks / search_impressions`.

It's one Go binary serving the web app, the management API, the runtime API
for your services, and the data pipeline, all backed by Postgres.

```
 your services ──resolve──▶ ┌──────────── Libra ────────────┐
   (who gets what)          │ in-memory snapshot → assign   │──▶ exposures ─┐
                            └───────────────────────────────┘               │
 your services ──events───────────────────────────────────────▶ events ────┤
   (search, click, order)                                                   ▼
                                 ┌──────────── pipeline (incremental) ─────────────┐
                                 │ exposures → assignments (first exposure/unit)   │
                                 │ events + measure definitions → unit_measure_daily│
                                 └─────────────────────────────────────────────────┘
                                                        │
             metric formulas (configurable any time) ──▶ report: value, lift, CI, p, SRM
```

## Quick start

```bash
make setup                      # frontend deps
make build                      # builds the SPA and ./libra
docker compose up -d postgres   # or point LIBRA_DATABASE_URL at any Postgres
./libra demo                    # sample "search" business + 14 days of simulated traffic
./libra sign-in-link admin      # open the printed link to sign in
./libra                         # serve on :8080
```

`libra demo` creates a **Search** business with the metrics below, two traffic
layers, three experiments, and about 700k simulated events. The simulated
treatments have real built-in effects, so the reports show real results. For
example, "Ranking formula v2" raises CTR by 5%, and the report recovers that
figure.

With Docker only: `cp .env.example .env`, set `POSTGRES_PASSWORD`, then
`docker compose up --build`, followed by
`docker compose exec libra /libra demo` and
`docker compose exec libra /libra sign-in-link admin`.

## Concepts

| Concept | What it is |
|---|---|
| **Business** | A product area (search, feed, checkout) that sends its own events and owns its metrics. |
| **Event** | A raw fact pushed by a service: `{business, event, unit_id, ts, value, props}`. |
| **Measure** | Events → one number per unit per day: `count`, `sum`, `max` or `any` (0/1) of an event, with optional property filters. Example: *search_gmv = sum(value) of `order` where `source = search`*. |
| **Metric** | A **formula** over measures, the built-in `users` (exposed units), and other metrics. Edit it any time; reports use it immediately. |
| **Metric group** | An ordered set of metrics, used as a report template for an experiment. |
| **Layer** | 1,000 traffic buckets. Experiments in the same layer never share a unit; experiments in different layers overlap independently. |
| **Experiment** | Variants (with JSON parameters and weights), a traffic %, targeting rules, test users, and a lifecycle with review. |

### The demo's search metrics

| Metric | Formula |
|---|---|
| Search GMV / User | `search_gmv / users` |
| Search GMV | `search_gmv` |
| Search Ads GMV | `search_ads_gmv` |
| CTR | `search_clicks / search_impressions` |
| CVR | `search_orders / search_clicks` |
| Buyer rate | `search_buyers / users` |
| Ads share of GMV | `ads_gmv / gmv` (metrics can reference other metrics) |

### How reports are computed

- A unit counts from the **day of its first exposure**, up to the end of the
  report window. Units seen in two variants (a misconfiguration or client bug)
  are excluded and counted separately.
- For each variant, the pipeline tables give the unit count and, for every
  measure used, the sum and cross-sum over units. This happens in one SQL pass,
  so no per-unit data leaves Postgres.
- A formula `f` is evaluated on per-unit means, and its variance comes from
  the **delta method**: `∇fᵀ Σ ∇f / n`. The gradient comes from automatic
  differentiation of the formula. This gives correct confidence intervals for
  ratio metrics like CTR, not only for plain averages.
- **Kind detection.** Formulas that scale with traffic are marked *total*
  (for example `search_gmv`). Variants of different sizes can't compare totals
  directly, so totals are shown per variant and compared per exposed unit.
  Scale-free formulas are marked *ratio* (CTR, GMV/user) and are compared
  directly.
- Each treatment gets relative lift and its interval, a two-sided z-test
  p-value, and a verdict (better / worse / not significant) based on the
  metric's desired direction.
- A **sample ratio mismatch** check (chi-square against the configured
  weights) flags broken splits.
- Reports can be broken down by any request attribute seen at first exposure
  (region, os, …). A cumulative trend chart per metric shows how the estimate
  settles over time.

### Traffic and lifecycle

`draft → in_review → approved → active ⇄ paused → stopped | launched → archived`

- Review is on by default. A reviewer must be an editor other than the owner;
  admins may self-approve, and it's recorded. A business can turn review off.
- Starting an experiment allocates buckets in its layer. Ramping up adds
  buckets, so units already in stay in with the same variant. Ramping down
  removes the most recently added buckets. Variants and weights lock once the
  experiment starts.
- **Launch** serves the winning variant's parameters to everyone who matches
  the targeting, and releases the experiment's traffic. Running experiments can
  still override launched parameters.
- **Test users** (whitelist) get a fixed variant even before start, and are
  never counted in reports.
- Every change is in the experiment's history (audit log).

## Integrating a service

Create an API key in **Settings**, or with `libra api-key <name> runtime,ingest`.

```bash
# What does this user get? (logs exposures)
curl -X POST $LIBRA/api/v1/resolve -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"unit_id":"user-42","business":"search","attrs":{"region":"ID","os":"android"}}'

# Business events (batches up to 5,000; ts may be up to 30 days old)
curl -X POST $LIBRA/api/v1/events -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"events":[{"business":"search","event":"order","unit_id":"user-42","value":35.9,"props":{"source":"search","is_ads":true}}]}'
```

Go client: `github.com/reynerpantou/libra/pkg/client` (`Resolve`, `Track` with
background batching, `LogExposures`). Services that assign units locally must
use the same hash: the first 8 bytes of SHA-256, big-endian, mod 1000, over
`layer:<layer salt>:<unit>` and `exp:<experiment salt>:<unit>`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `LIBRA_ADDR` | `:8080` | listen address |
| `LIBRA_DATABASE_URL` | `postgres://libra:libra@localhost:5432/libra?sslmode=disable` | Postgres |
| `LIBRA_PIPELINE_INTERVAL_SECONDS` | `300` | pipeline schedule (0 = on demand only) |
| `LIBRA_COOKIE_SECURE` | `true` | HTTPS-only cookies (browsers accept them on `http://localhost`) |
| `LIBRA_PUBLIC_URL` | `http://localhost:8080` | sign-in callbacks return here |
| `LIBRA_ADMIN_USER` / `LIBRA_ADMIN_EMAIL` | `admin` / empty | owner created on first run |
| `LIBRA_GOOGLE_CLIENT_ID` / `_SECRET` | empty | Sign in with Google |
| `LIBRA_APPLE_*` | empty | Sign in with Apple |
| `LIBRA_TRUSTED_PROXIES` | empty | reverse proxies whose `X-Forwarded-For` is trusted |

## Commands

```
libra                                        run the server
libra sign-in-link <username>                one-time sign-in link (15 minutes)
libra api-key <name> <scope>[,<scope>]       create an API key (runtime, ingest)
libra pipeline                               run the data pipeline once
libra demo [-users N] [-days N]              seed the demo business and simulate traffic
libra simulate [-business K] [-users N] [-days N] [-seed S]
```

## Layout

```
cmd/libra/          entrypoint: routes, server, CLI commands
internal/formula/   formula parser, evaluator with gradients, kind detection
internal/stats/     delta-method comparisons, SRM, distributions
internal/assign/    layers, buckets, targeting, variant choice, parameter merging
internal/serving/   in-memory snapshot (hot reload), batched exposure/event writers
internal/pipeline/  measure definitions → SQL, incremental rollups, scheduler
internal/report/    metric definitions, report/trend/preview computation
internal/handlers/  HTTP API (management + runtime)
internal/simulate/  demo seeding and traffic simulation
pkg/client/         Go client for the runtime API
web/                React + TypeScript app (built into the binary)
```

## Tests

`go test ./...` runs unit tests for formulas, statistics (including a null
simulation that checks the false-positive rate is about 5%) and assignment
(stickiness, ramping, mutual exclusion, targeting). With
`LIBRA_TEST_DATABASE_URL` pointing at an empty Postgres database, an end-to-end
test also seeds, simulates, runs the pipeline and checks that the report
recovers the simulated effects.

## Not built yet

- Holdouts and reverse (post-launch) experiments
- Variance reduction (CUPED)
- Sequential testing
- Multi-armed bandits
- External review webhooks
- Multi-stage rollouts
- A columnar store (ClickHouse) for very high event volumes; the pipeline's
  rollup tables are the seam for that.
