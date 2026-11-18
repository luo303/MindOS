-- 0015_conversations.up.sql
--
-- 多轮会话的最小模型：一个会话多条消息，消息按 seq 排序。
-- 摘要/向量/关键词列在后续迁移里加，本迁移只做"能聊起来"的部分。

CREATE TABLE conversations (
    id            bigserial PRIMARY KEY,
    owner_id      bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title         text   NOT NULL DEFAULT '',
    message_count integer NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversations_owner_idx ON conversations (owner_id, updated_at DESC);

CREATE TABLE messages (
    id              bigserial PRIMARY KEY,
    conversation_id bigint NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    owner_id        bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role            text   NOT NULL CHECK (role IN ('user', 'assistant')),
    content         text   NOT NULL,
    citations       jsonb  NOT NULL DEFAULT '[]',
    seq             integer NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq)
);
CREATE INDEX messages_conv_seq_idx ON messages (conversation_id, seq);
CREATE INDEX messages_owner_idx ON messages (owner_id, conversation_id, seq);

-- qa_runs 关联会话，便于复盘；一次性 /ask 的 conversation_id 为 NULL。
ALTER TABLE qa_runs
    ADD COLUMN conversation_id bigint REFERENCES conversations(id) ON DELETE SET NULL;
CREATE INDEX qa_runs_conversation_idx ON qa_runs (conversation_id);
