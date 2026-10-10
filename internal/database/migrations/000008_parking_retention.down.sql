DROP INDEX IF EXISTS upload_operations_first_failed_at_idx;
ALTER TABLE upload_operations DROP COLUMN IF EXISTS first_failed_at;
