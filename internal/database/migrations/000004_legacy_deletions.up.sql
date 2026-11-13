-- Previous releases marked manual deletions before physical removal.
-- Recheck those objects independently of any exhausted RabbitMQ task.
INSERT INTO deletions (filename, attempts, available_at)
SELECT filename, 0, now() FROM upload_operations WHERE state = 'deleted'
ON CONFLICT (filename) DO NOTHING;
