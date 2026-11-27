-- v1 无法表示开放条目；自动回滚不得丢弃这些用户数据。
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM memories WHERE key NOT IN ('answer_language','answer_detail','code_example_language'))
       OR EXISTS (SELECT 1 FROM memory_candidates WHERE key NOT IN ('answer_language','answer_detail','code_example_language'))
       OR EXISTS (SELECT 1 FROM memory_candidates GROUP BY owner_id,message_id HAVING count(*) > 1)
       OR EXISTS (SELECT 1 FROM memory_settings WHERE mode <> 'auto')
       OR EXISTS (SELECT 1 FROM memory_conversation_settings)
       OR EXISTS (SELECT 1 FROM memory_evidence GROUP BY memory_id,version HAVING count(*) > 1)
    THEN RAISE EXCEPTION 'open memory data cannot be represented by v1'; END IF;
END $$;
DROP TABLE memory_conversation_settings;
ALTER TABLE conversations DROP CONSTRAINT conversations_owner_id_id_unique;
DROP TABLE memory_settings;
DROP TABLE memory_extractions;
DROP TABLE memory_evidence;
DROP INDEX memories_active_idx;
ALTER TABLE memory_candidates DROP CONSTRAINT memory_candidates_source_key_unique,
    DROP COLUMN target_id, DROP COLUMN action, DROP COLUMN category, DROP COLUMN scope, DROP COLUMN expires_at, DROP COLUMN pinned,
    ADD UNIQUE(owner_id,message_id), ADD CHECK (key IN ('answer_language','answer_detail','code_example_language'));
ALTER TABLE memory_versions DROP COLUMN category, DROP COLUMN scope, DROP COLUMN expires_at, DROP COLUMN pinned, DROP COLUMN automatic;
ALTER TABLE memories DROP COLUMN category, DROP COLUMN scope, DROP COLUMN expires_at, DROP COLUMN pinned,
    ADD CHECK (key IN ('answer_language','answer_detail','code_example_language'));
