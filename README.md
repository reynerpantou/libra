# Libra

An experimentation platform: design A/B tests, split traffic safely, collect
business events, and read reports built from **metrics you define as
formulas**. For example, `search_gmv / users` or
`search_clicks / search_impressions`.

It's one Go binary serving the web app, the management API, the runtime API
for your services, and the data pipeline, all backed by Postgres.

```
 your services ──abtest───▶ ┌──────────── Libra ────────────┐
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
./libra demo                    # the full demo (every feature) on an empty database
./libra                         # serve on :8080 and print the owner setup link
```

Libra lives under **`/libra`**: the app at `http://localhost:8080/libra/`, the
API at `/libra/api/…` (anything else redirects there), so other apps can share
the domain. With Google sign-in, register
`<LIBRA_PUBLIC_URL>/libra/api/auth/google/callback` as the redirect URI.

`libra demo` (or `make demo`) builds a demo of every feature in about three
minutes:

- two platforms, **Demo Shop** (`search`, `reco`) and **Market App** (`search`,
  `promo`) — both have a `search` business, to show that equal keys don't
  collide;
- metrics, measures and metric groups (platform defaults included), custom
  diversions (`shop_id`, `session_id`), shared and dedicated layers;
- 17 experiments covering every state: draft, in review (reviewers invited),
  approved, rejected (with the reviewer's note), running, paused, stopped,
  launched (one still ramping out), archived. They also cover targeting rules,
  test users, an 8-variant test, a multi-business experiment, and a traffic
  ramp in progress;
- about two million simulated events with built-in effects, so the reports
  show real results ("Ranking formula v2" raises CTR by about 5%, and the
  report recovers it);
- two AB Tuning studies: "Ranking weights auto-tune" (Bayesian, running round
  7) and "Result page size" (quasi-random, finished, ready to launch);
- a demo team (bob admin; alice, dina, evan editors; carol viewer) and two API
  keys, printed once.

The demo needs an empty database. `make reset` drops everything (it asks
first) and `make demo-fresh` resets and rebuilds the demo.

With Docker only: `cp .env.example .env`, set `POSTGRES_PASSWORD`, then
`docker compose up --build`, followed by
`docker compose exec libra /libra demo`. The setup link is in
`docker compose logs libra`.

### Becoming the owner

There's no admin account to configure. While nobody can sign in as the
owner, every start prints a one-time setup link to the server's log:

```
  ┌─ Libra has no owner yet ─────────────────────────────────────────
  │ Open this link and sign in with Google or Apple to become the owner:
  │
  │   http://localhost:8080/libra/setup#…
```

Open it and continue with Google or Apple; that account becomes the owner
(admin). The link works once and expires after 24 hours; `libra setup-link`
(`make claim`) prints a new one. Everyone else is invited from **Settings →
People** by email. For recovery, `libra sign-in-link <username>`
(`make link user=…`) prints a one-time sign-in link, and `libra list-users`
(`make users`) lists accounts.

## Concepts

| Concept | What it is |
|---|---|
| **Business** | A product area (search, feed, checkout) that sends its own events and owns its metrics. |
| **Event** | A raw fact pushed by a service: `{business, event, user_id, device_id, ts, value, props}` (at least one of the ids). |
| **Measure** | Events → one number per unit per day: `count`, `sum`, `max` or `any` (0/1) of an event, with optional property filters. Example: *search_gmv = sum(value) of `order` where `source = search`*. |
| **Metric** | A **formula** over measures, the built-in `users` (exposed units), and other metrics. Edit it any time; reports use it immediately. |
| **Metric group** | An ordered set of metrics, used as a report template for an experiment. |
| **Layer** | 1,000 traffic buckets and a **diversion**: whether it splits by `user_id` or `device_id`. Experiments in the same layer never share a unit; experiments in different layers overlap independently. |
| **Experiment** | Variants (with JSON parameters and weights), a traffic %, targeting, test users, and a lifecycle with review. Ids are random 15-digit numbers, so they can't be guessed or walked. |
| **Targeting attribute** | A request attribute experiments may target on (`device`, `app_version`, `region`…), with a type and known values, managed under **Targeting attributes**. |

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
- A draft can't plan more traffic than its layer has free.
- **Targeting** is OR of AND-groups over registered attributes, e.g.
  `(device = android AND app_version ≥ 3.400) OR (device = ios AND app_version ≥ 2.300) OR device in (desktop, mobile)`.
  Versions compare part by part (`10.2 > 9.9`).
- **Diversion**: a layer that splits by device keeps a device in one variant
  whoever signs in on it; splitting by user keeps a person consistent across
  devices. Requests carry `user_id` and/or `device_id`; an experiment whose id
  is missing is skipped. Measures are computed per user and per device, so
  either kind of experiment gets metrics (send both ids on events when known).
- **Test users** (whitelist) get a fixed variant even before start, and are
  never counted in reports. They're user or device ids per the layer.
- **Parameter conflicts.** On an experiment's page, click any field of a
  variant's parameters to see every other experiment that sets the same field,
  a field inside it, or a whole value above it, and who wins. Sharing only a
  parent object (`search.ranking.formula` vs `search.ads.slot`) is never a
  conflict, and neither is anything in the same layer. Otherwise: a running
  experiment (or test-user assignment) beats a launched default; between
  experiments, **the one that started first wins** (never-started ones come
  last; same second → smaller id); between launched defaults, the most recent
  launch wins.
- Every change is in the experiment's history (audit log).

## AB Tuning

A tuning study searches numeric parameters (say two ranking weights in
[0.2, 1.0]) instead of comparing a few fixed variants:

1. define the tunable params, their bounds and today's values (v0);
2. pick an objective metric and optional guardrails ("latency at most 5% worse");
3. choose N treatment arms and a search algorithm;
4. each round, every arm serves one candidate point; v0 stays the control;
5. when a round ends (00:00 UTC, once the pipeline has the data), each arm's
   lift versus v0 is measured and the algorithm picks the next round's points
   from every result so far.

Algorithms (`internal/tuning`): **random** and **quasi-random** (scrambled
Halton) sampling explore; **bayesian** fits a Gaussian process to all results
and picks a batch by expected improvement; **constrained** adds a Gaussian
process per guardrail, multiplies in the probability they hold, and searches a
trust region around the best feasible point. Candidates come from a model of
the whole space, not from mutating one winner. Units are re-randomised across
arms every round (a new salt) and each round's arms get new variant ids, so a
unit's earlier arm never leaks into the next measurement. The recommendation
is the tested point the model rates best among those likely to keep the
guardrails; launch it like any variant. `libra demo-tuning` plays a sample
study.

## Integrating a service

Create an API key in **Settings**, or with `libra api-key <name> runtime,ingest`.

```bash
# What does this user get? (logs exposures)
# LIBRA=http://localhost:8080/libra
curl -X POST $LIBRA/api/v1/abtest/experiments -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"user_id":"user-42","device_id":"dev-9f3a","business":"search","attrs":{"region":"ID","device":"android","app_version":"3.500"}}'

# Business events (batches up to 5,000; ts may be up to 30 days old)
curl -X POST $LIBRA/api/v1/events -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"events":[{"business":"search","event":"order","user_id":"user-42","device_id":"dev-9f3a","value":35.9,"props":{"source":"search","is_ads":true}}]}'
```

Go client: `github.com/reynerpantou/libra/pkg/client` (`Resolve`, `Track` with
background batching, `LogExposures`). Services that assign units locally must
use the same hash: the first 8 bytes of SHA-256, big-endian, mod 1000, over
`layer:<layer salt>:<unit>` and `exp:<experiment salt>:<unit>`, where the
unit is the user or device id per the layer's diversion. (`unit_id` is still
accepted as an alias for `user_id`.)

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `LIBRA_ADDR` | `:8080` | listen address |
| `LIBRA_DATABASE_URL` | `postgres://libra:libra@localhost:5433/libra?sslmode=disable` | Postgres |
| `LIBRA_DB_PORT` | `5433` | host port of the compose Postgres container |
| `LIBRA_PIPELINE_INTERVAL_SECONDS` | `300` | pipeline schedule (0 = on demand only) |
| `LIBRA_COOKIE_SECURE` | `true` | HTTPS-only cookies (browsers accept them on `http://localhost`) |
| `LIBRA_PUBLIC_URL` | `http://localhost:8080` | sign-in callbacks return here |
| `LIBRA_GOOGLE_CLIENT_ID` / `_SECRET` | empty | Sign in with Google |
| `LIBRA_APPLE_*` | empty | Sign in with Apple |
| `LIBRA_TRUSTED_PROXIES` | empty | reverse proxies whose `X-Forwarded-For` is trusted |

## Commands

```
libra                                        run the server
libra setup-link                             new owner setup link (only while nobody can sign in as owner)
libra list-users                             list accounts
libra sign-in-link <username>                one-time sign-in link (15 minutes)
libra api-key <name> <scope>[,<scope>]       create an API key (runtime, ingest)
libra pipeline                               run the data pipeline once
libra demo [-users N] [-days N]              build the full demo on an empty database
libra reset -yes                             drop every table and all data (make reset asks first)
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
