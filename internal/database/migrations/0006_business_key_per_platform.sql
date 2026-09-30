-- Business keys are unique within their platform (TikTok Shop and
-- Tokopedia can both have "search"). Services name the platform when a
-- key alone is ambiguous.
ALTER TABLE businesses DROP CONSTRAINT IF EXISTS businesses_key_key;
CREATE UNIQUE INDEX businesses_platform_key ON businesses (platform_id, key);
