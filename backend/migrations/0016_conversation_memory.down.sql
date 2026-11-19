-- 0016_conversation_memory.down.sql

DROP TABLE IF EXISTS conversation_summaries;

ALTER TABLE messages
    DROP COLUMN IF EXISTS token_count,
    DROP COLUMN IF EXISTS embedded_at,
    DROP COLUMN IF EXISTS embedding_model,
    DROP COLUMN IF EXISTS embedding;

ALTER TABLE conversations
    DROP COLUMN IF EXISTS summary_claimed_at,
    DROP COLUMN IF EXISTS summary_claimed_until,
    DROP COLUMN IF EXISTS summary_version,
    DROP COLUMN IF EXISTS summarized_until_seq;
