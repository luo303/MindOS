-- 0013：给知识、问答与模型配置加归属，并把唯一约束收窄到"同一用户内"。
--
-- 本次升级**清空既有业务数据**。理由：owner_id 是 NOT NULL + 外键，
-- 存量行没有任何可靠归属；静默归给某个用户在多租户下就是数据泄漏，
-- 让第一个注册的人"继承"更是如此。开发阶段的数据是测试数据，清空比猜一个 owner 诚实。
--
-- tasks / outbox / consumed_messages 一并清空：它们指向即将被删的文档，
-- 留着重投只会得到一串 document_not_found。
DELETE FROM documents;   -- document_chunks 由 ON DELETE CASCADE 跟随删除
DELETE FROM qa_runs;
DELETE FROM chat_models;
DELETE FROM tasks;
DELETE FROM outbox;
DELETE FROM consumed_messages;

-- ---------- documents ----------
-- 表已清空，所以可以直接加 NOT NULL 列，不需要默认值过渡。
ALTER TABLE documents ADD COLUMN owner_id bigint NOT NULL REFERENCES users (id);

-- 幂等导入从"全站同内容唯一"变成"同一用户内唯一"：
-- 不同用户上传同一份文件，各自拥有一条记录（原文对象是共享的，见 service.Delete）。
DROP INDEX documents_content_hash_key;
CREATE UNIQUE INDEX documents_owner_hash_uniq ON documents (owner_id, content_hash);

-- 列表是"我的文档按时间倒序"，索引必须把 owner 放在最左列。
DROP INDEX documents_created_at_idx;
CREATE INDEX documents_owner_created_idx ON documents (owner_id, created_at DESC, id DESC);

-- 批量回填的候选筛选也要按用户收窄，否则会排到别人的文档。
DROP INDEX documents_embed_pending_idx;
CREATE INDEX documents_embed_pending_idx
    ON documents (owner_id, id) WHERE status = 'ready' AND embed_status <> 'done';

-- ---------- qa_runs ----------
ALTER TABLE qa_runs ADD COLUMN owner_id bigint NOT NULL REFERENCES users (id);
DROP INDEX qa_runs_created_at_idx;
DROP INDEX qa_runs_verdict_idx;
CREATE INDEX qa_runs_owner_created_idx ON qa_runs (owner_id, created_at DESC);
CREATE INDEX qa_runs_owner_verdict_idx ON qa_runs (owner_id, verdict, created_at DESC);

-- ---------- chat_models ----------
ALTER TABLE chat_models ADD COLUMN owner_id bigint NOT NULL REFERENCES users (id);
-- 重名只在同一个人的配置里不允许：两个用户可以各自有一条叫 "默认" 的配置。
DROP INDEX chat_models_name_uniq;
DROP INDEX chat_models_default_uniq;
CREATE UNIQUE INDEX chat_models_owner_name_uniq ON chat_models (owner_id, name);
-- 每人最多一条默认：部分唯一索引按 owner 分组。
CREATE UNIQUE INDEX chat_models_owner_default_uniq ON chat_models (owner_id) WHERE is_default;
