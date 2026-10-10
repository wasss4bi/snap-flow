ALTER TABLE upload_operations ADD COLUMN first_failed_at timestamptz;
UPDATE upload_operations SET first_failed_at = failed_at WHERE state = 'failed';
CREATE INDEX upload_operations_first_failed_at_idx ON upload_operations (first_failed_at) WHERE state = 'failed';
