CREATE TABLE debug_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    token_name TEXT DEFAULT '',
    model TEXT DEFAULT '',
    provider TEXT DEFAULT '',
    request_body TEXT DEFAULT '',
    response_body TEXT DEFAULT '',
    request_headers TEXT DEFAULT '',
    response_status INTEGER DEFAULT 0
);
CREATE INDEX idx_debug_request_id ON debug_log(request_id);
CREATE INDEX idx_debug_timestamp ON debug_log(timestamp);
