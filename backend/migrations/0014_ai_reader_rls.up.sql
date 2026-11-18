-- 0014_ai_reader_rls.up.sql
--
-- 给 SQL 查询工具建一个独立的只读账号：它只能查 documents / document_chunks
-- 的授权列，且只能看到 app.tenant_id 指定的那一行。租户 ID 由工具代码用
-- SET LOCAL 设置，模型既拿不到也改不了（set_config 已从该角色收回）。

-- 角色是集群级对象：ALTER ROLE ... SET 更新的是同一条 pg_db_role_setting
-- 记录。多个库并行迁移时，并发更新会报 "tuple concurrently updated"（XX000）。
-- 用事务级 advisory lock 把这段角色 DDL 串行化；锁随迁移事务提交释放。
SELECT pg_advisory_xact_lock(732014);

DO $$
BEGIN
    -- 用异常捕获而不是 IF NOT EXISTS：多个测试库会并行跑迁移，
    -- 两个会话可能同时看到"角色不存在"，然后一个成功、一个撞 42710。
    BEGIN
        CREATE ROLE mindos_ai_reader
            LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS
            CONNECTION LIMIT 4;
    EXCEPTION
        WHEN duplicate_object THEN NULL;
    END;
END
$$;

-- 不在迁移里 ALTER ROLE ... SET：那是集群级共享行，多个库并行迁移会撞
-- "tuple concurrently updated"。会话级限制由执行器在每个事务里 SET LOCAL，
-- 只读由 BeginTx(ReadOnly) 保证。

-- 模型能改租户的唯一入口。收回后它只能读到工具设置的值；工具改用 SET LOCAL。
REVOKE EXECUTE ON FUNCTION pg_catalog.set_config(text, text, boolean) FROM PUBLIC;

-- 只授统计需要的列：content / storage_path / content_hash / 错误详情都不给。
-- owner_id 必须授：document_chunks 的策略要在子查询里读它；它是调用者自己的 ID。
GRANT SELECT (id, title, source_type, mime, collection, tags, status,
              chunk_count, size_bytes, embed_status, embed_model,
              embed_dimensions, embedded_chunks, embedded_at,
              created_at, updated_at, owner_id)
  ON documents TO mindos_ai_reader;

-- 分块表没有 owner_id，行归属由父表 EXISTS 判断；content / embedding / tsv 都不给。
GRANT SELECT (id, document_id, seq, heading, start_offset, end_offset,
              token_count, embed_model, embedded_at, created_at)
  ON document_chunks TO mindos_ai_reader;

ALTER TABLE documents ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS documents_ai_read ON documents;
CREATE POLICY documents_ai_read ON documents
    FOR SELECT TO mindos_ai_reader
    USING (owner_id = current_setting('app.tenant_id', true)::bigint);

ALTER TABLE document_chunks ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS document_chunks_ai_read ON document_chunks;
CREATE POLICY document_chunks_ai_read ON document_chunks
    FOR SELECT TO mindos_ai_reader
    USING (EXISTS (
        SELECT 1 FROM documents d
        WHERE d.id = document_chunks.document_id
          AND d.owner_id = current_setting('app.tenant_id', true)::bigint
    ));
