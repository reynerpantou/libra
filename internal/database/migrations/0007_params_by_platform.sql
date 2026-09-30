-- Each variant's parameters live under its platform's key, e.g.
-- {"tiktokshop": {"search": {...}}}, so platforms never write the same
-- field. Existing parameters are wrapped.
UPDATE variants v
SET params = jsonb_build_object(p.key, CASE WHEN jsonb_typeof(v.params) = 'object' THEN v.params ELSE '{}'::jsonb END)
FROM experiments e
JOIN businesses b ON b.id = e.business_id
JOIN platforms p ON p.id = b.platform_id
WHERE v.experiment_id = e.id
  AND NOT (jsonb_typeof(v.params) = 'object'
           AND v.params ? p.key
           AND jsonb_typeof(v.params -> p.key) = 'object'
           AND (SELECT count(*) FROM jsonb_object_keys(v.params)) = 1);

UPDATE config_version SET version = version + 1;
