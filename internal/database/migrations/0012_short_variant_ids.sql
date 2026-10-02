-- Variant ids become 8 digits (10000000–99999999): short enough to read out
-- and type, and visibly different from 15-digit experiment ids. They stay
-- random and unique (checked against existing ids, and the primary key).
CREATE OR REPLACE FUNCTION libra_variant_id() RETURNS BIGINT AS $$
DECLARE
    candidate BIGINT;
BEGIN
    LOOP
        candidate := 10000000 + (('x' || substr(replace(gen_random_uuid()::text, '-', ''), 1, 15))::bit(60)::bigint % 90000000);
        EXIT WHEN NOT EXISTS (SELECT 1 FROM variants WHERE id = candidate);
    END LOOP;
    RETURN candidate;
END;
$$ LANGUAGE plpgsql VOLATILE;

-- Renumber existing (long) ids one by one, so each new id is checked
-- against those already given out. Whitelist rows follow by cascade.
CREATE TEMP TABLE short_variant_ids (old_id BIGINT PRIMARY KEY, new_id BIGINT NOT NULL) ON COMMIT DROP;
DO $$
DECLARE
    r RECORD;
    n BIGINT;
BEGIN
    FOR r IN SELECT id FROM variants WHERE id >= 100000000 ORDER BY id LOOP
        n := libra_variant_id();
        UPDATE variants SET id = n WHERE id = r.id;
        INSERT INTO short_variant_ids VALUES (r.id, n);
    END LOOP;
END $$;

UPDATE experiments e SET launched_variant_id = m.new_id FROM short_variant_ids m WHERE e.launched_variant_id = m.old_id;
UPDATE exposures x SET variant_id = m.new_id FROM short_variant_ids m WHERE x.variant_id = m.old_id;
UPDATE assignments a SET variant_id = m.new_id FROM short_variant_ids m WHERE a.variant_id = m.old_id;

-- Tuning rounds keep variant ids inside their arms and results.
CREATE FUNCTION pg_temp.renumber_arms(arms JSONB) RETURNS JSONB AS $$
    SELECT COALESCE(jsonb_agg(CASE WHEN m.new_id IS NULL THEN a ELSE jsonb_set(a, '{variant_id}', to_jsonb(m.new_id)) END ORDER BY o), '[]'::jsonb)
    FROM jsonb_array_elements(arms) WITH ORDINALITY AS x(a, o)
    LEFT JOIN short_variant_ids m ON m.old_id = (a->>'variant_id')::bigint
$$ LANGUAGE sql;
UPDATE tuning_rounds SET arms = pg_temp.renumber_arms(arms) WHERE jsonb_typeof(arms) = 'array' AND jsonb_array_length(arms) > 0;
UPDATE tuning_rounds SET results = jsonb_set(results, '{arms}', pg_temp.renumber_arms(results->'arms'))
    WHERE results IS NOT NULL AND jsonb_typeof(results->'arms') = 'array';

UPDATE config_version SET version = version + 1;
