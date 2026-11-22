-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE space_openapi_tokens (
 id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 space_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 issuer_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name VARCHAR(100) NOT NULL,
 token_hash BINARY(32) NOT NULL,
 key_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 ciphertext BLOB NOT NULL,
 created_at DATETIME(6) NOT NULL,
 expires_at DATETIME(6) NOT NULL,
 revoked_at DATETIME(6) NULL,
 PRIMARY KEY(space_id,id),
 UNIQUE KEY openapi_token_hash(token_hash),
 FOREIGN KEY(space_id) REFERENCES spaces(id),
 FOREIGN KEY(issuer_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
