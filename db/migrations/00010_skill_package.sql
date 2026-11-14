-- +goose Up
-- Published Skills already have a verified TOS bundle. Keep the immutable
-- object, its digest, and its resource references; discard the inline copy.
UPDATE definition_versions v
JOIN definitions d ON d.space_id=v.space_id AND d.id=v.definition_id
JOIN content_objects o ON o.space_id=v.space_id AND o.id=v.content_id
SET v.bundle='null'
WHERE d.kind='skill' AND o.state='ready' AND o.kind='definition.bundle';
