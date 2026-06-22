-- Bootstrap channel tags for profit reporting.
-- Run on System 1 (54.178.16.161) newapi PostgreSQL.
--
-- Tags used by the profit report:
--   pipi          -> channel id 1489 (big-key proxy to System 2)
--   maas-official -> Anthropic direct API channels
--   maas-aws-z    -> AWS Bedrock zone Z
--   maas-aws-x    -> AWS Bedrock zone X
--
-- Adjust the WHERE clauses below to match your actual channel naming convention.

BEGIN;

-- 1. Pipi big-key channel (the only sure-fire mapping)
UPDATE channels SET tag = 'pipi' WHERE id = 1489;

-- 2. MAAS official keys — adjust pattern as needed
-- UPDATE channels SET tag = 'maas-official'
--   WHERE id <> 1489 AND name ILIKE '%maas-official%' AND (tag IS NULL OR tag = '');

-- 3. AWS Bedrock zone Z
-- UPDATE channels SET tag = 'maas-aws-z'
--   WHERE id <> 1489 AND name ILIKE '%aws-z%' AND (tag IS NULL OR tag = '');

-- 4. AWS Bedrock zone X
-- UPDATE channels SET tag = 'maas-aws-x'
--   WHERE id <> 1489 AND name ILIKE '%aws-x%' AND (tag IS NULL OR tag = '');

-- Inspect current state before committing
SELECT tag, COUNT(*) AS n
FROM channels
WHERE status = 1
GROUP BY tag
ORDER BY tag NULLS LAST;

-- COMMIT;     -- uncomment after verifying counts
ROLLBACK;
