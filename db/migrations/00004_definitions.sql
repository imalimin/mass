-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE definitions (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, kind ENUM('agent','skill') NOT NULL,
 name VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, archived BOOLEAN NOT NULL DEFAULT FALSE,
 default_version_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
 PRIMARY KEY(space_id,id), UNIQUE(space_id,kind,name),
 FOREIGN KEY(space_id) REFERENCES spaces(id)
) ENGINE=InnoDB;
CREATE TABLE definition_drafts (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, definition_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1, document JSON NOT NULL,
 PRIMARY KEY(space_id,definition_id), FOREIGN KEY(space_id,definition_id) REFERENCES definitions(space_id,id)
) ENGINE=InnoDB;
CREATE TABLE definition_versions (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, definition_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 label VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL, debug BOOLEAN NOT NULL, archived BOOLEAN NOT NULL DEFAULT FALSE,
 content_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, sha256 CHAR(64) CHARACTER SET ascii NOT NULL,
 bundle JSON NOT NULL, created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(space_id,id), UNIQUE(space_id,definition_id,label), UNIQUE(space_id,definition_id,id),
 CHECK ((debug=TRUE AND label IS NULL) OR (debug=FALSE AND label IS NOT NULL)),
 FOREIGN KEY(space_id,definition_id) REFERENCES definitions(space_id,id),
 FOREIGN KEY(space_id,content_id) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;
ALTER TABLE definitions ADD FOREIGN KEY(space_id,id,default_version_id) REFERENCES definition_versions(space_id,definition_id,id);
CREATE TABLE definition_dependencies (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, version_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, ordinal INT NOT NULL,
 skill_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, skill_version_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(space_id,version_id,ordinal), UNIQUE(space_id,version_id,skill_id),
 FOREIGN KEY(space_id,version_id) REFERENCES definition_versions(space_id,id),
 FOREIGN KEY(space_id,skill_id,skill_version_id) REFERENCES definition_versions(space_id,definition_id,id)
) ENGINE=InnoDB;
CREATE TABLE definition_draft_objects (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, definition_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, path VARCHAR(512) NOT NULL, object_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(space_id,definition_id,path),
 FOREIGN KEY(space_id,definition_id) REFERENCES definition_drafts(space_id,definition_id),
 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;
CREATE TABLE definition_version_objects (
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, version_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, path VARCHAR(512) NOT NULL, object_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(space_id,version_id,path),
 FOREIGN KEY(space_id,version_id) REFERENCES definition_versions(space_id,id),
 FOREIGN KEY(space_id,object_id) REFERENCES content_objects(space_id,id)
) ENGINE=InnoDB;
