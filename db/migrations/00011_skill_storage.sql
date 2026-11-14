-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE IF NOT EXISTS skill_drafts (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 definition_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1,
 content_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(space_id,definition_id),
 FOREIGN KEY(space_id,definition_id) REFERENCES definitions(space_id,id),
 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS skill_versions (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 definition_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 label VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 archived BOOLEAN NOT NULL DEFAULT FALSE,
 content_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(space_id,id), UNIQUE(space_id,definition_id,label), UNIQUE(space_id,definition_id,id),
 FOREIGN KEY(space_id,definition_id) REFERENCES definitions(space_id,id),
 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;

-- All prior dependency rows target discarded Skill versions.
DROP TABLE IF EXISTS definition_dependencies;

-- The old catalog tables also hold Agent data. Remove only Skill rows.
DELETE o FROM definition_draft_objects o
JOIN definitions d ON d.space_id=o.space_id AND d.id=o.definition_id WHERE d.kind='skill';
DELETE o FROM definition_version_objects o
JOIN definition_versions v ON v.space_id=o.space_id AND v.id=o.version_id
JOIN definitions d ON d.space_id=v.space_id AND d.id=v.definition_id WHERE d.kind='skill';
DELETE dr FROM definition_drafts dr
JOIN definitions d ON d.space_id=dr.space_id AND d.id=dr.definition_id WHERE d.kind='skill';
DELETE v FROM definition_versions v
JOIN definitions d ON d.space_id=v.space_id AND d.id=v.definition_id WHERE d.kind='skill';
DELETE FROM definitions WHERE kind='skill';

CREATE TABLE IF NOT EXISTS definition_dependencies (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, version_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, ordinal INT NOT NULL,
 skill_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, skill_version_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(space_id,version_id,ordinal), UNIQUE(space_id,version_id,skill_id),
 FOREIGN KEY(space_id,version_id) REFERENCES definition_versions(space_id,id),
 FOREIGN KEY(space_id,skill_id,skill_version_id) REFERENCES skill_versions(space_id,definition_id,id)
) ENGINE=InnoDB;
