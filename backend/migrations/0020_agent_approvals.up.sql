-- 0020_agent_approvals.up.sql
--
-- AI 写待办的持久化提案。v1 不暂停模型：工具创建 pending 记录并返回
-- “待审批”，用户批准后由服务端执行；审批记录可跨刷新/重启保留。

CREATE TABLE agent_approvals (
    id              bigserial PRIMARY KEY,
    owner_id        bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    conversation_id bigint REFERENCES conversations(id) ON DELETE CASCADE,
    user_message_id bigint REFERENCES messages(id) ON DELETE SET NULL,
    chat_model_id   bigint,
    tool_name       text   NOT NULL,
    arguments       jsonb  NOT NULL DEFAULT '{}',
    summary         text   NOT NULL,
    status          text   NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'approved', 'rejected', 'completed', 'expired', 'failed')),
    expires_at      timestamptz NOT NULL,
    decided_at      timestamptz,
    result          jsonb  NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_approvals_pending_idx
    ON agent_approvals (owner_id, status, created_at DESC);
CREATE INDEX agent_approvals_conversation_idx
    ON agent_approvals (owner_id, conversation_id, created_at DESC);

ALTER TABLE todos
    ADD CONSTRAINT todos_approval_fk
    FOREIGN KEY (approval_id) REFERENCES agent_approvals(id) ON DELETE SET NULL;
