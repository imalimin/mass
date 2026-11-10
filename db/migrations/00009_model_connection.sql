-- +goose NO TRANSACTION
-- +goose Up
-- Retain the original encryption binding for existing keys. New keys bind to model ID.
ALTER TABLE model_configs
 ADD COLUMN IF NOT EXISTS base_url VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 ADD COLUMN IF NOT EXISTS secret_binding_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 ADD COLUMN IF NOT EXISTS key_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 ADD COLUMN IF NOT EXISTS ciphertext BLOB NULL;

-- Conditional SQL also permits retry after a partially applied nontransactional DDL.
SET @mass_copy = IF(EXISTS(SELECT 1 FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='credentials') AND EXISTS(SELECT 1 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='model_configs' AND COLUMN_NAME='credential_id'),
 'UPDATE model_configs m JOIN credentials c ON c.space_id=m.space_id AND c.id=m.credential_id SET m.base_url=COALESCE(NULLIF(c.base_url,''''),m.endpoint),m.secret_binding_id=c.id,m.key_id=c.key_id,m.ciphertext=c.ciphertext,m.enabled=(m.enabled AND c.enabled) WHERE m.key_id=''''',
 'SELECT 1');
PREPARE mass_copy_stmt FROM @mass_copy;
EXECUTE mass_copy_stmt;
DEALLOCATE PREPARE mass_copy_stmt;

SET @mass_fk = (SELECT CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='model_configs' AND REFERENCED_TABLE_NAME='credentials' LIMIT 1);
SET @mass_drop_fk = IF(@mass_fk IS NULL,'SELECT 1',CONCAT('ALTER TABLE model_configs DROP FOREIGN KEY `',REPLACE(@mass_fk,'`','``'),'`'));
PREPARE mass_fk_stmt FROM @mass_drop_fk;
EXECUTE mass_fk_stmt;
DEALLOCATE PREPARE mass_fk_stmt;

ALTER TABLE model_configs DROP COLUMN IF EXISTS credential_id, DROP COLUMN IF EXISTS endpoint;
DROP TABLE IF EXISTS credentials;
ALTER TABLE model_configs MODIFY ciphertext BLOB NOT NULL;
