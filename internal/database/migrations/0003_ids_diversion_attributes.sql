-- ===== Unguessable experiment ids =====
--
-- Experiment ids are random 15-digit numbers (below 2^53, so they're exact in
-- JavaScript too) instead of 1, 2, 3…, so nobody can walk the ids to find
-- other teams' experiments. The randomness comes from gen_random_uuid().
CREATE FUNCTION libra_random_id() RETURNS BIGINT AS $$
    SELECT 100000000000000 + (('x' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 15))::bit(60)::bigint % 900000000000000)
$$ LANGUAGE sql VOLATILE;

CREATE FUNCTION libra_experiment_id() RETURNS BIGINT AS $$
DECLARE
    candidate BIGINT;
BEGIN
    LOOP
        candidate := libra_random_id();
        EXIT WHEN NOT EXISTS (SELECT 1 FROM experiments WHERE id = candidate);
    END LOOP;
    RETURN candidate;
END;
$$ LANGUAGE plpgsql VOLATILE;

ALTER TABLE experiments ALTER COLUMN id SET DEFAULT libra_experiment_id();
DROP SEQUENCE IF EXISTS experiments_id_seq CASCADE;
ALTER TABLE experiments ALTER COLUMN id SET DEFAULT libra_experiment_id();

-- Renumber existing experiments. Child rows follow through ON UPDATE CASCADE;
-- the raw and rolled-up data tables (no foreign keys) are updated by hand.
ALTER TABLE variants  DROP CONSTRAINT variants_experiment_id_fkey,
    ADD CONSTRAINT variants_experiment_id_fkey FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE whitelist DROP CONSTRAINT whitelist_experiment_id_fkey,
    ADD CONSTRAINT whitelist_experiment_id_fkey FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_experiment_id_fkey,
    ADD CONSTRAINT audit_log_experiment_id_fkey FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE ON UPDATE CASCADE;

CREATE TEMP TABLE experiment_renumber ON COMMIT DROP AS
    SELECT id AS old_id, NULL::bigint AS new_id FROM experiments WHERE id < 100000000000000;
UPDATE experiment_renumber SET new_id = libra_random_id();
UPDATE experiments e SET id = r.new_id FROM experiment_renumber r WHERE e.id = r.old_id;
UPDATE exposures x SET experiment_id = r.new_id FROM experiment_renumber r WHERE x.experiment_id = r.old_id;
UPDATE assignments a SET experiment_id = r.new_id FROM experiment_renumber r WHERE a.experiment_id = r.old_id;
UPDATE audit_log a SET detail = jsonb_set(a.detail, '{from}', to_jsonb(r.new_id))
    FROM experiment_renumber r WHERE a.action = 'clone' AND (a.detail->>'from')::bigint = r.old_id;

-- ===== Diversion: which id an experiment randomizes on =====
--
-- Set per layer, so experiments sharing a layer always split the same kind
-- of unit and stay mutually exclusive.
ALTER TABLE layers ADD COLUMN diversion TEXT NOT NULL DEFAULT 'user_id' CHECK (diversion IN ('user_id', 'device_id'));
ALTER TABLE exposures ADD COLUMN unit_type TEXT NOT NULL DEFAULT 'user_id';
ALTER TABLE assignments ADD COLUMN unit_type TEXT NOT NULL DEFAULT 'user_id';

-- Events can name the user, the device, or both; measures are computed per
-- user and per device so either kind of experiment can be measured.
ALTER TABLE events ADD COLUMN device_id TEXT;
ALTER TABLE unit_measure_daily ADD COLUMN unit_type TEXT NOT NULL DEFAULT 'user_id';
ALTER TABLE unit_measure_daily DROP CONSTRAINT unit_measure_daily_pkey,
    ADD PRIMARY KEY (measure_id, unit_type, unit_id, day);
UPDATE measures SET needs_backfill = true;

-- ===== Targeting attributes =====
--
-- The attributes targeting rules may use, managed in the app so rules are
-- picked from a list instead of typed free-hand.
CREATE TABLE attributes (
    id          BIGSERIAL PRIMARY KEY,
    key         TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    type        TEXT NOT NULL CHECK (type IN ('string', 'number', 'version', 'boolean')),
    options     TEXT[] NOT NULL DEFAULT '{}', -- known values, offered as choices
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO attributes (key, name, description, type, options) VALUES
    ('region', 'Region', 'Country or region code', 'string', '{ID,SG,MY,TH,VN,PH,US}'),
    ('os', 'Operating system', 'Client operating system', 'string', '{android,ios,windows,macos,linux}'),
    ('device', 'Device type', 'Kind of device', 'string', '{android,ios,desktop,mobile,tablet}'),
    ('app_version', 'App version', 'Client app version, compared numerically part by part (10.2 > 9.9)', 'version', '{}'),
    ('language', 'Language', 'Preferred language code', 'string', '{en,id,zh,th,vi,ms}'),
    ('is_new_user', 'New user', 'Registered in the last 30 days', 'boolean', '{true,false}');

-- Targeting used to be a flat list of rules (all must match); it's now OR of
-- AND-groups. Wrap old lists as a single group.
UPDATE experiments SET targeting = jsonb_build_object('groups', CASE WHEN jsonb_array_length(targeting) = 0 THEN '[]'::jsonb ELSE jsonb_build_array(targeting) END)
    WHERE jsonb_typeof(targeting) = 'array';
ALTER TABLE experiments ALTER COLUMN targeting SET DEFAULT '{"groups": []}';
