-- 0008：分块向量化。
--
-- 三件事：
--   1. embedding 列从 vector(1536) 改成 vector(1024)，对齐 bge-m3；
--      1024 也在 pgvector 的 HNSW 上限（2000 维）之内，索引才能建。
--   2. document_chunks 记录"这条向量是哪个模型产出的"。
--   3. documents 记录向量化状态，供页面直接读出，不必 join 任务表。
--
-- 为什么状态还放在 documents 上：tasks 回答"这次动作执行到哪"，
-- documents 回答"这份文档现在能不能语义检索"。后者要在详情页直接读到，
-- 且 reaper 会清理任务表，不能让历史被清掉。
ALTER TABLE document_chunks
    ALTER COLUMN embedding TYPE vector(1024);

ALTER TABLE document_chunks
    ADD COLUMN embed_model text,
    ADD COLUMN embedded_at timestamptz;

ALTER TABLE documents
    ADD COLUMN embed_status        text        NOT NULL DEFAULT 'pending',
    ADD COLUMN embed_model         text,
    ADD COLUMN embed_dimensions    integer,
    ADD COLUMN embedded_chunks     integer     NOT NULL DEFAULT 0,
    ADD COLUMN embed_error_code    text,
    ADD COLUMN embed_error_message text,
    ADD COLUMN embedded_at         timestamptz;

-- 默认 pending 对存量数据是准确的：老文档确实一条向量都没有。
-- skipped 与 pending 必须分开：前者是"没配模型所以没做"，后者是"该做还没做"。
ALTER TABLE documents
    ADD CONSTRAINT documents_embed_status_check
        CHECK (embed_status IN ('pending', 'running', 'done', 'failed', 'skipped'));

-- HNSW + 余弦距离，与 overview 6.2 的 <=> 一致。
-- 空表建索引没有成本；文档量上万后再调 maintenance_work_mem。
CREATE INDEX document_chunks_embedding_idx
    ON document_chunks USING hnsw (embedding vector_cosine_ops);

-- 批量回填的候选筛选只扫"已就绪但没向量化完"的行。
CREATE INDEX documents_embed_pending_idx
    ON documents (id)
    WHERE status = 'ready' AND embed_status <> 'done';
