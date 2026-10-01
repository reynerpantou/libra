-- AB Tuning: an experiment whose treatment arms get new candidate parameter
-- values every round, chosen by a search algorithm from earlier rounds.

ALTER TABLE experiments ADD COLUMN kind TEXT NOT NULL DEFAULT 'ab' CHECK (kind IN ('ab', 'tuning'));
CREATE INDEX experiments_kind ON experiments (kind);

-- Arms of finished rounds stay (with weight 0) so their exposures, reports
-- and a launch can still refer to them.
ALTER TABLE variants ADD COLUMN retired BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE variants ADD COLUMN round INT NOT NULL DEFAULT 0;

CREATE TABLE tunings (
    experiment_id   BIGINT PRIMARY KEY REFERENCES experiments(id) ON DELETE CASCADE ON UPDATE CASCADE,
    algorithm       TEXT NOT NULL CHECK (algorithm IN ('random', 'quasi_random', 'bayesian', 'constrained')),
    params          JSONB NOT NULL,             -- [{path, type, min, max, control, scale, step}]
    base_params     JSONB NOT NULL DEFAULT '{}', -- the rest of the config every arm serves (inside the platform key)
    objective_metric_id BIGINT NOT NULL REFERENCES metrics(id),
    objective_direction TEXT NOT NULL CHECK (objective_direction IN ('increase', 'decrease')),
    guardrails      JSONB NOT NULL DEFAULT '[]', -- [{metric_id, max_drop}] max_drop: allowed relative degradation, 0.01 = 1%
    arms            INT NOT NULL CHECK (arms BETWEEN 1 AND 20),
    round_days      INT NOT NULL CHECK (round_days BETWEEN 1 AND 30),
    max_rounds      INT NOT NULL CHECK (max_rounds BETWEEN 1 AND 100),
    min_units       INT NOT NULL DEFAULT 0,      -- per arm before a round may end
    keep_best       BOOLEAN NOT NULL DEFAULT true,
    seed            BIGINT NOT NULL,
    base_salt       TEXT NOT NULL,
    round           INT NOT NULL DEFAULT 1,      -- the current round
    state           JSONB NOT NULL DEFAULT '{}', -- the algorithm's state between rounds
    finished_at     TIMESTAMPTZ,
    note            TEXT NOT NULL DEFAULT ''
);

CREATE TABLE tuning_rounds (
    experiment_id BIGINT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE ON UPDATE CASCADE,
    round         INT NOT NULL,
    started_at    TIMESTAMPTZ,                  -- set when the round starts serving
    ends_at       TIMESTAMPTZ,                  -- when it's due to end
    ended_at      TIMESTAMPTZ,
    status        TEXT NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'running', 'analyzed')),
    arms          JSONB NOT NULL DEFAULT '[]',  -- [{variant_id, key, values, x, source, predicted}]
    results       JSONB,                        -- per arm: units, objective and guardrail estimates
    note          TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (experiment_id, round)
);

UPDATE config_version SET version = version + 1;
