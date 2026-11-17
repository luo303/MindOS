-- 回滚 0013：去掉归属，恢复全局唯一约束。
--
-- 注意：清空的数据不会回来。回滚后所有用户的文档混在同一张表里，
-- 旧代码（单用户）才能继续工作；如果多用户期间产生了内容相同的文档，
-- 最后那条全局唯一索引会建不起来——那时只能先人工去重。
ALTER TABLE chat_models DROP COLUMN IF EXISTS owner_id;
DROP INDEX IF EXISTS chat_models_owner_name_uniq;
DROP INDEX IF EXISTS chat_models_owner_default_uniq;
CREATE UNIQUE INDEX chat_models_name_uniq ON chat_models (name);
CREATE UNIQUE INDEX chat_models_default_uniq ON chat_models (is_default) WHERE is_default;

ALTER TABLE qa_runs DROP COLUMN IF EXISTS owner_id;
DROP INDEX IF EXISTS qa_runs_owner_created_idx;
DROP INDEX IF EXISTS qa_runs_owner_verdict_idx;
CREATE INDEX qa_runs_created_at_idx ON qa_runs (created_at DESC);
CREATE INDEX qa_runs_verdict_idx ON qa_runs (verdict, created_at DESC);

ALTER TABLE documents DROP COLUMN IF EXISTS owner_id;
DROP INDEX IF EXISTS documents_owner_hash_uniq;
DROP INDEX IF EXISTS documents_owner_created_idx;
DROP INDEX IF EXISTS documents_embed_pending_idx;
CREATE UNIQUE INDEX documents_content_hash_key ON documents (content_hash);
CREATE INDEX documents_created_at_idx ON documents (created_at DESC, id DESC);
CREATE INDEX documents_embed_pending_idx
    ON documents (id) WHERE status = 'ready' AND embed_status <> 'done';
