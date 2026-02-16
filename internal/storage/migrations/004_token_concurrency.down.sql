-- SQLite doesn't support DROP COLUMN in older versions, so we recreate the table
CREATE TABLE api_tokens_backup AS SELECT id, name, key_hash, key_prefix, user_id, rate_limit_rpm, daily_budget_usd, created_at, last_used_at FROM api_tokens;
DROP TABLE api_tokens;
ALTER TABLE api_tokens_backup RENAME TO api_tokens;
