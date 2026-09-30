-- ===== People and access =====

CREATE TABLE users (
    id           BIGSERIAL PRIMARY KEY,
    username     TEXT NOT NULL,
    email        TEXT,
    display_name TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('admin', 'editor', 'viewer')),
    is_owner     BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_username_key ON users (lower(username));
CREATE UNIQUE INDEX users_email_key ON users (lower(email));
CREATE UNIQUE INDEX users_one_owner ON users (is_owner) WHERE is_owner;

CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE user_identities (
    provider   TEXT NOT NULL,
    subject    TEXT NOT NULL,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);

CREATE TABLE auth_flows (
    id         TEXT PRIMARY KEY,
    browser    TEXT NOT NULL,
    provider   TEXT NOT NULL,
    nonce      TEXT NOT NULL,
    verifier   TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE sign_in_links (
    token      TEXT PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);

-- Keys for services calling the runtime API (resolve, exposures) and the
-- ingestion API (events). Only a hash of the key is stored.
CREATE TABLE api_keys (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    key_hash     TEXT NOT NULL UNIQUE,
    scopes       TEXT[] NOT NULL,
    created_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

-- ===== Businesses and their metric definitions =====

CREATE TABLE businesses (
    id             BIGSERIAL PRIMARY KEY,
    key            TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    require_review BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A measure turns raw events into one number per unit per day: e.g.
-- search_gmv = SUM(value) of `order` events where source = 'search'.
CREATE TABLE measures (
    id            BIGSERIAL PRIMARY KEY,
    business_id   BIGINT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    key           TEXT NOT NULL,
    name          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    event_name    TEXT NOT NULL,
    aggregation   TEXT NOT NULL CHECK (aggregation IN ('count', 'sum', 'max', 'any')),
    value_field   TEXT NOT NULL DEFAULT '',  -- '' = the event's value; else a numeric property
    filters       JSONB NOT NULL DEFAULT '[]',
    needs_backfill BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (business_id, key)
);

-- A metric is a formula over measures (and `users`, and other metrics).
CREATE TABLE metrics (
    id          BIGSERIAL PRIMARY KEY,
    business_id BIGINT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    key         TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    formula     TEXT NOT NULL,
    format      TEXT NOT NULL DEFAULT 'number' CHECK (format IN ('number', 'percent', 'currency')),
    decimals    INT NOT NULL DEFAULT 2 CHECK (decimals BETWEEN 0 AND 6),
    direction   TEXT NOT NULL DEFAULT 'increase' CHECK (direction IN ('increase', 'decrease', 'neutral')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (business_id, key)
);

CREATE TABLE metric_groups (
    id          BIGSERIAL PRIMARY KEY,
    business_id BIGINT NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    metric_ids  BIGINT[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (business_id, name)
);

-- ===== Experiments =====

CREATE TABLE layers (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    salt        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE experiments (
    id                  BIGSERIAL PRIMARY KEY,
    business_id         BIGINT NOT NULL REFERENCES businesses(id),
    layer_id            BIGINT NOT NULL REFERENCES layers(id),
    name                TEXT NOT NULL,
    hypothesis          TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    owner_id            BIGINT REFERENCES users(id) ON DELETE SET NULL,
    status              TEXT NOT NULL DEFAULT 'draft',
    salt                TEXT NOT NULL,
    traffic_target      INT NOT NULL DEFAULT 0 CHECK (traffic_target BETWEEN 0 AND 1000), -- buckets (per mille)
    buckets             INT[] NOT NULL DEFAULT '{}',                                        -- held while active
    targeting           JSONB NOT NULL DEFAULT '[]',
    metric_group_id     BIGINT REFERENCES metric_groups(id) ON DELETE SET NULL,
    review_note         TEXT NOT NULL DEFAULT '',
    reviewer_id         BIGINT REFERENCES users(id) ON DELETE SET NULL,
    launched_variant_id BIGINT,
    started_at          TIMESTAMPTZ,
    ended_at            TIMESTAMPTZ,
    launched_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX experiments_status ON experiments (status);

CREATE TABLE variants (
    id            BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    key           TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    is_control    BOOLEAN NOT NULL DEFAULT false,
    weight        INT NOT NULL CHECK (weight BETWEEN 0 AND 1000),
    params        JSONB NOT NULL DEFAULT '{}',
    position      INT NOT NULL DEFAULT 0,
    UNIQUE (experiment_id, key)
);

CREATE TABLE whitelist (
    experiment_id BIGINT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
    unit_id       TEXT NOT NULL,
    variant_id    BIGINT NOT NULL REFERENCES variants(id) ON DELETE CASCADE,
    note          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (experiment_id, unit_id)
);

CREATE TABLE audit_log (
    id            BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT REFERENCES experiments(id) ON DELETE CASCADE,
    entity        TEXT NOT NULL,
    entity_id     BIGINT,
    actor_id      BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action        TEXT NOT NULL,
    from_status   TEXT,
    to_status     TEXT,
    detail        JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_experiment ON audit_log (experiment_id, id DESC);

-- Serving instances poll this and rebuild their snapshot when it moves.
CREATE TABLE config_version (
    id      BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    version BIGINT NOT NULL
);
INSERT INTO config_version VALUES (true, 1);

-- ===== Raw data (append-only) =====

CREATE TABLE exposures (
    id            BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT NOT NULL,
    variant_id    BIGINT NOT NULL,
    unit_id       TEXT NOT NULL,
    ts            TIMESTAMPTZ NOT NULL,
    attrs         JSONB NOT NULL DEFAULT '{}', -- request attributes, for dimension breakdowns
    ingested_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE events (
    id          BIGSERIAL PRIMARY KEY,
    business_id BIGINT NOT NULL,
    event_name  TEXT NOT NULL,
    unit_id     TEXT NOT NULL,
    ts          TIMESTAMPTZ NOT NULL,
    value       DOUBLE PRECISION NOT NULL DEFAULT 0,
    props       JSONB NOT NULL DEFAULT '{}',
    ingested_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX events_business_event_ts ON events (business_id, event_name, ts);

-- ===== Pipeline outputs =====

-- First exposure of each unit to each experiment.
CREATE TABLE assignments (
    experiment_id BIGINT NOT NULL,
    unit_id       TEXT NOT NULL,
    variant_id    BIGINT NOT NULL,
    first_ts      TIMESTAMPTZ NOT NULL,
    first_day     DATE NOT NULL,
    multi_variant BOOLEAN NOT NULL DEFAULT false, -- seen in more than one variant: excluded from reports
    dims          JSONB NOT NULL DEFAULT '{}',     -- attributes at first exposure (region, os, ...)
    PRIMARY KEY (experiment_id, unit_id)
);
CREATE INDEX assignments_day ON assignments (experiment_id, first_day);

-- Each measure's value per unit per day.
CREATE TABLE unit_measure_daily (
    measure_id BIGINT NOT NULL REFERENCES measures(id) ON DELETE CASCADE,
    day        DATE NOT NULL,
    unit_id    TEXT NOT NULL,
    value      DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (measure_id, unit_id, day)
);

CREATE TABLE pipeline_state (
    id                BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    exposure_watermark BIGINT NOT NULL DEFAULT 0,
    event_watermark    BIGINT NOT NULL DEFAULT 0
);
INSERT INTO pipeline_state VALUES (true, 0, 0);

CREATE TABLE pipeline_runs (
    id          BIGSERIAL PRIMARY KEY,
    trigger     TEXT NOT NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    status      TEXT NOT NULL DEFAULT 'running',
    stats       JSONB NOT NULL DEFAULT '{}',
    error       TEXT NOT NULL DEFAULT ''
);

-- Casts text to a number, or NULL when it isn't one — so one malformed event
-- property can't fail a whole pipeline run.
CREATE FUNCTION libra_num(t TEXT) RETURNS DOUBLE PRECISION AS $$
BEGIN
    RETURN t::DOUBLE PRECISION;
EXCEPTION WHEN others THEN
    RETURN NULL;
END;
$$ LANGUAGE plpgsql IMMUTABLE;
