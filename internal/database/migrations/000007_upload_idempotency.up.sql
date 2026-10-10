CREATE TABLE upload_idempotency (
    user_id bigint NOT NULL,
    key varchar(128) NOT NULL,
    request_hash varchar(64) NOT NULL,
    status_code integer NOT NULL DEFAULT 0,
    response text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, key)
);
CREATE INDEX upload_idempotency_expires_at_idx ON upload_idempotency (expires_at);
