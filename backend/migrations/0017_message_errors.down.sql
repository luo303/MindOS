-- 0017_message_errors.down.sql

ALTER TABLE messages
    DROP COLUMN IF EXISTS error_message,
    DROP COLUMN IF EXISTS error_code;
