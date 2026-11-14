-- +goose NO TRANSACTION
-- +goose Up
UPDATE definition_drafts SET document=JSON_REMOVE(document,'$.assets')
WHERE JSON_CONTAINS_PATH(document,'one','$.assets');
UPDATE definition_versions SET bundle=JSON_REMOVE(bundle,'$.document.assets')
WHERE JSON_CONTAINS_PATH(bundle,'one','$.document.assets');
DROP TABLE IF EXISTS definition_draft_objects;
DROP TABLE IF EXISTS definition_version_objects;
