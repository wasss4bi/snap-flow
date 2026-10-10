ALTER TABLE upload_operations ADD COLUMN IF NOT EXISTS failure_code varchar(64);
ALTER TABLE upload_operations ADD COLUMN IF NOT EXISTS failed_at timestamptz;
