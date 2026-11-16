-- 回滚 0008：去掉向量化状态与记忆，列维度退回 0002 的 1536。
-- 顺序与 up 相反：先删索引与约束，再删列，最后改列类型。
DROP INDEX IF EXISTS documents_embed_pending_idx;
DROP INDEX IF EXISTS document_chunks_embedding_idx;

ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_embed_status_check;

ALTER TABLE documents
    DROP COLUMN IF EXISTS embed_status,
    DROP COLUMN IF EXISTS embed_model,
    DROP COLUMN IF EXISTS embed_dimensions,
    DROP COLUMN IF EXISTS embedded_chunks,
    DROP COLUMN IF EXISTS embed_error_code,
    DROP COLUMN IF EXISTS embed_error_message,
    DROP COLUMN IF EXISTS embedded_at;

ALTER TABLE document_chunks
    DROP COLUMN IF EXISTS embed_model,
    DROP COLUMN IF EXISTS embedded_at;

-- 回滚会丢掉全部向量（维度不同无法保留），这是有意的：
-- 保留一份维度不符的向量比清空更难排查。
ALTER TABLE document_chunks
    ALTER COLUMN embedding TYPE vector(1536);
