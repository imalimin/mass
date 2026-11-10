-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS base_url VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER name;

-- Preserve an existing credential's unambiguous endpoint. Credentials shared by
-- models with different endpoints require an explicit edit before further use.
UPDATE credentials c
LEFT JOIN (
 SELECT space_id,credential_id,MIN(BINARY NULLIF(endpoint,'')) AS endpoint,COUNT(DISTINCT BINARY NULLIF(endpoint,'')) AS endpoints
 FROM model_configs GROUP BY space_id,credential_id
) m ON m.space_id=c.space_id AND m.credential_id=c.id
SET c.base_url=CASE
 WHEN m.endpoints=1 THEN m.endpoint
 WHEN m.endpoints IS NULL THEN 'https://ark.cn-beijing.volces.com/api/v3'
 ELSE '' END;
