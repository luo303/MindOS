-- 0014_ai_reader_rls.down.sql
--
-- 回滚顺序：先拆策略，再收回授权，最后删角色——直接 DROP ROLE 会因为有
-- 依赖（策略、授权）而失败。

DROP POLICY IF EXISTS document_chunks_ai_read ON document_chunks;
ALTER TABLE document_chunks DISABLE ROW LEVEL SECURITY;
REVOKE SELECT (id, document_id, seq, heading, start_offset, end_offset,
               token_count, embed_model, embedded_at, created_at)
  ON document_chunks FROM mindos_ai_reader;

DROP POLICY IF EXISTS documents_ai_read ON documents;
ALTER TABLE documents DISABLE ROW LEVEL SECURITY;
REVOKE SELECT (id, title, source_type, mime, collection, tags, status,
               chunk_count, size_bytes, embed_status, embed_model,
               embed_dimensions, embedded_chunks, embedded_at,
               created_at, updated_at, owner_id)
  ON documents FROM mindos_ai_reader;

GRANT EXECUTE ON FUNCTION pg_catalog.set_config(text, text, boolean) TO PUBLIC;

-- 不在这里 DROP ROLE：角色是集群级的，别的库可能还引用它（策略/授权），
-- 从一个库的迁移里删会直接失败。全部库都回滚后手工清理：
--   DROP ROLE IF EXISTS mindos_ai_reader;
