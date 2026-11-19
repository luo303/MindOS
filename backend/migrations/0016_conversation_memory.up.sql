-- 0016_conversation_memory.up.sql
--
-- 阶段 2/3：消息向量、滚动摘要、会话摘要水位。
-- 关键词检索走 Elasticsearch 的 mindos-messages，不在 PG 建 tsvector。

ALTER TABLE conversations
    ADD COLUMN summarized_until_seq   integer NOT NULL DEFAULT 0,
    ADD COLUMN summary_version        integer NOT NULL DEFAULT 0,
    ADD COLUMN summary_claimed_until  integer NOT NULL DEFAULT 0,
    ADD COLUMN summary_claimed_at     timestamptz;

ALTER TABLE messages
    ADD COLUMN embedding       vector(1024),
    ADD COLUMN embedding_model text,
    ADD COLUMN embedded_at     timestamptz,
    ADD COLUMN token_count     integer NOT NULL DEFAULT 0;

CREATE TABLE conversation_summaries (
    id                bigserial PRIMARY KEY,
    conversation_id   bigint NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    owner_id          bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    version           integer NOT NULL,
    covered_from_seq  integer NOT NULL DEFAULT 1,
    covered_to_seq    integer NOT NULL,
    batch_from_seq    integer NOT NULL,
    batch_to_seq      integer NOT NULL,
    content           text   NOT NULL,
    token_count       integer NOT NULL,
    model             text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, version)
);
CREATE INDEX conversation_summaries_latest_idx
    ON conversation_summaries (conversation_id, version DESC);
