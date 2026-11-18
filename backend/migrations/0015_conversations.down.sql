-- 0015_conversations.down.sql
DROP INDEX IF EXISTS qa_runs_conversation_idx;
ALTER TABLE qa_runs DROP COLUMN IF EXISTS conversation_id;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS conversations;
