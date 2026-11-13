CREATE TABLE revoked_tokens (hash varchar(64) PRIMARY KEY, expires_at timestamptz NOT NULL);
CREATE INDEX idx_revoked_tokens_expires_at ON revoked_tokens (expires_at);
