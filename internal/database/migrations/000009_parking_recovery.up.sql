CREATE TABLE parking_recoveries (
    task_id varchar(64) PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);
