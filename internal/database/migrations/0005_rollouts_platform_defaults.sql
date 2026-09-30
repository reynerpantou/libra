-- Gradual rollouts, partial launches and each platform's built-in default
-- metric group.

-- A launched variant can serve a share of units (per mille) while it rolls
-- out; the rest keep the previous defaults.
ALTER TABLE experiments ADD COLUMN launch_rollout INT NOT NULL DEFAULT 1000
    CHECK (launch_rollout BETWEEN 1 AND 1000);

-- A rollout plan moves an experiment's traffic (kind 'traffic') or its
-- launch share (kind 'launch') up by step every interval until target.
CREATE TABLE rollouts (
    id            BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE ON UPDATE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('traffic', 'launch')),
    start_value   INT NOT NULL,
    target        INT NOT NULL CHECK (target BETWEEN 0 AND 1000),
    step          INT NOT NULL CHECK (step BETWEEN 1 AND 1000),
    interval_secs INT NOT NULL CHECK (interval_secs >= 60),
    next_at       TIMESTAMPTZ NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'done', 'cancelled', 'blocked')),
    note          TEXT NOT NULL DEFAULT '',
    created_by    BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX rollouts_one_active ON rollouts (experiment_id, kind) WHERE status = 'active';
CREATE INDEX rollouts_due ON rollouts (next_at) WHERE status = 'active';

-- Every platform has a built-in default group (its metrics apply to every
-- experiment of its businesses). It can be edited but not deleted.
ALTER TABLE metric_groups ADD COLUMN builtin BOOLEAN NOT NULL DEFAULT false;
INSERT INTO metric_groups (platform_id, name, description, is_default, builtin, metric_ids)
SELECT p.id, 'Default metrics', 'Applied to every experiment of every business on this platform', true, true, '{}'
FROM platforms p
WHERE NOT EXISTS (SELECT 1 FROM metric_groups g WHERE g.platform_id = p.id AND g.name = 'Default metrics');
UPDATE metric_groups g SET builtin = true, is_default = true
WHERE g.platform_id IS NOT NULL AND g.name = 'Default metrics';
