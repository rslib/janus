-- SQLite doesn't support DROP COLUMN before 3.35.0, so we recreate
CREATE TABLE users_backup AS SELECT id, username, password_hash, is_admin, totp_secret, totp_enabled, created_at, updated_at FROM users;
DROP TABLE users;
ALTER TABLE users_backup RENAME TO users;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username);
