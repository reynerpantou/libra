-- ===== Diversion types =====
--
-- Which ids traffic can be split by. user_id and device_id are built in;
-- adding a row (say shop_id) makes it available to layers, requests,
-- events and every page at once.
CREATE TABLE diversions (
    key         TEXT PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,39}$'),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    builtin     BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO diversions (key, name, description, builtin) VALUES
    ('user_id', 'User id', 'Keeps a signed-in person in one variant across devices.', true),
    ('device_id', 'Device id', 'Keeps a device in one variant, before or after sign-in.', true);

ALTER TABLE layers DROP CONSTRAINT layers_diversion_check,
    ADD CONSTRAINT layers_diversion_fkey FOREIGN KEY (diversion) REFERENCES diversions(key);

-- An auto layer belongs to a single experiment: all 1,000 buckets are its
-- own, so it can take up to 100% of traffic. It's hidden from the layer list.
ALTER TABLE layers ADD COLUMN auto BOOLEAN NOT NULL DEFAULT false;

-- Ids beyond user and device (keyed by diversion) that events carry.
ALTER TABLE events ADD COLUMN ids JSONB NOT NULL DEFAULT '{}';

-- ===== Platforms =====
--
-- A platform (a company or product line, e.g. "TikTok Shop") groups
-- businesses (Search, Feed, …). Platforms can define their own measures,
-- metrics and metric groups; a platform's default groups are included in
-- every experiment of its businesses.
CREATE TABLE platforms (
    id          BIGSERIAL PRIMARY KEY,
    key         TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO platforms (key, name, description)
    SELECT 'default', 'Default platform', 'Businesses created before platforms existed'
    WHERE EXISTS (SELECT 1 FROM businesses);
ALTER TABLE businesses ADD COLUMN platform_id BIGINT REFERENCES platforms(id);
UPDATE businesses SET platform_id = (SELECT id FROM platforms WHERE key = 'default');
ALTER TABLE businesses ALTER COLUMN platform_id SET NOT NULL;

-- Measures, metrics and metric groups belong to a business or a platform.
ALTER TABLE measures ALTER COLUMN business_id DROP NOT NULL,
    ADD COLUMN platform_id BIGINT REFERENCES platforms(id) ON DELETE CASCADE,
    ADD CONSTRAINT measures_one_scope CHECK ((business_id IS NULL) <> (platform_id IS NULL));
CREATE UNIQUE INDEX measures_platform_key ON measures (platform_id, key) WHERE platform_id IS NOT NULL;

ALTER TABLE metrics ALTER COLUMN business_id DROP NOT NULL,
    ADD COLUMN platform_id BIGINT REFERENCES platforms(id) ON DELETE CASCADE,
    ADD CONSTRAINT metrics_one_scope CHECK ((business_id IS NULL) <> (platform_id IS NULL));
CREATE UNIQUE INDEX metrics_platform_key ON metrics (platform_id, key) WHERE platform_id IS NOT NULL;

ALTER TABLE metric_groups ALTER COLUMN business_id DROP NOT NULL,
    ADD COLUMN platform_id BIGINT REFERENCES platforms(id) ON DELETE CASCADE,
    ADD COLUMN is_default BOOLEAN NOT NULL DEFAULT false,
    ADD CONSTRAINT metric_groups_one_scope CHECK ((business_id IS NULL) <> (platform_id IS NULL));
CREATE UNIQUE INDEX metric_groups_platform_name ON metric_groups (platform_id, name) WHERE platform_id IS NOT NULL;

-- An experiment reads any number of metric groups, from any business.
ALTER TABLE experiments ADD COLUMN metric_group_ids BIGINT[] NOT NULL DEFAULT '{}';
UPDATE experiments SET metric_group_ids = ARRAY[metric_group_id] WHERE metric_group_id IS NOT NULL;
ALTER TABLE experiments DROP COLUMN metric_group_id;
