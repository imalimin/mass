-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE credentials (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name VARCHAR(100) NOT NULL,
 key_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 ciphertext BLOB NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 revision BIGINT NOT NULL DEFAULT 1,
 PRIMARY KEY(space_id,id),
 FOREIGN KEY(space_id) REFERENCES spaces(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE model_configs (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 space_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name VARCHAR(100) NOT NULL,
 provider VARCHAR(32) CHARACTER SET ascii NOT NULL,
 model_id VARCHAR(128) CHARACTER SET ascii NOT NULL,
 endpoint VARCHAR(255) CHARACTER SET ascii NOT NULL,
 credential_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 context_window INT NOT NULL,
 max_output_tokens INT NOT NULL,
 temperature DOUBLE NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 revision BIGINT NOT NULL DEFAULT 1,
 PRIMARY KEY(space_id,id),
 FOREIGN KEY(space_id) REFERENCES spaces(id),
 FOREIGN KEY(space_id,credential_id) REFERENCES credentials(space_id,id),
 CHECK(context_window > max_output_tokens AND max_output_tokens > 0),
 CHECK(temperature >= 0 AND temperature <= 2)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
