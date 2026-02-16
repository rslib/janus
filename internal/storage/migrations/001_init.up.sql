CREATE TABLE IF NOT EXISTS request_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp INTEGER NOT NULL,
    token_name TEXT NOT NULL,
    model TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_model TEXT NOT NULL,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    cost_usd REAL DEFAULT 0,
    latency_ms INTEGER DEFAULT 0,
    status TEXT NOT NULL,
    error_message TEXT DEFAULT '',
    streaming INTEGER DEFAULT 0,
    cached INTEGER DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_timestamp ON request_logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_token ON request_logs(token_name);
CREATE INDEX IF NOT EXISTS idx_model ON request_logs(model);
