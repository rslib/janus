ALTER TABLE request_logs ADD COLUMN request_id TEXT DEFAULT '';
CREATE INDEX idx_request_logs_request_id ON request_logs(request_id);
