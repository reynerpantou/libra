-- ===== Unguessable variant ids =====
-- Variant ids follow the experiment id convention: random 15-digit numbers,
-- so a service can log and report the variant ids it got without them
-- revealing counts or colliding across environments.
CREATE FUNCTION libra_variant_id() RETURNS BIGINT AS $$
DECLARE
    candidate BIGINT;
BEGIN
    LOOP
        candidate := libra_random_id();
        EXIT WHEN NOT EXISTS (SELECT 1 FROM variants WHERE id = candidate);
    END LOOP;
    RETURN candidate;
END;
$$ LANGUAGE plpgsql VOLATILE;

ALTER TABLE variants ALTER COLUMN id SET DEFAULT libra_variant_id();
DROP SEQUENCE IF EXISTS variants_id_seq CASCADE;
ALTER TABLE variants ALTER COLUMN id SET DEFAULT libra_variant_id();

-- Renumber existing variants: whitelist follows by cascade; the rest by hand.
ALTER TABLE whitelist DROP CONSTRAINT whitelist_variant_id_fkey,
    ADD CONSTRAINT whitelist_variant_id_fkey FOREIGN KEY (variant_id) REFERENCES variants(id) ON DELETE CASCADE ON UPDATE CASCADE;
CREATE TEMP TABLE variant_renumber ON COMMIT DROP AS
    SELECT id AS old_id, NULL::bigint AS new_id FROM variants WHERE id < 100000000000000;
UPDATE variant_renumber SET new_id = libra_random_id();
UPDATE variants v SET id = r.new_id FROM variant_renumber r WHERE v.id = r.old_id;
UPDATE experiments e SET launched_variant_id = r.new_id FROM variant_renumber r WHERE e.launched_variant_id = r.old_id;
UPDATE exposures x SET variant_id = r.new_id FROM variant_renumber r WHERE x.variant_id = r.old_id;
UPDATE assignments a SET variant_id = r.new_id FROM variant_renumber r WHERE a.variant_id = r.old_id;

-- ===== An experiment can span several businesses of its platform =====
-- business_id stays the primary one (its metrics lead the report);
-- business_ids lists all of them, primary first.
ALTER TABLE experiments ADD COLUMN business_ids BIGINT[] NOT NULL DEFAULT '{}';
UPDATE experiments SET business_ids = ARRAY[business_id];

UPDATE config_version SET version = version + 1;
