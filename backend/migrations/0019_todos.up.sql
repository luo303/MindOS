-- 0019_todos.up.sql
--
-- 用户待办：与文档异步任务的 tasks 表无关。
-- source=ai 时 approval_id 关联后续的审批记录；手动创建为 NULL。

CREATE TABLE todos (
    id           bigserial PRIMARY KEY,
    owner_id     bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title        text   NOT NULL,
    description  text   NOT NULL DEFAULT '',
    status       text   NOT NULL DEFAULT 'open'
                 CHECK (status IN ('open', 'in_progress', 'done', 'cancelled')),
    priority     smallint NOT NULL DEFAULT 2
                 CHECK (priority BETWEEN 1 AND 3),
    due_at       timestamptz,
    source       text   NOT NULL DEFAULT 'manual'
                 CHECK (source IN ('manual', 'ai')),
    source_ref   jsonb  NOT NULL DEFAULT '{}',
    approval_id  bigint UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    deleted_at   timestamptz
);
CREATE INDEX todos_owner_status_due_idx
    ON todos (owner_id, status, due_at NULLS LAST)
    WHERE deleted_at IS NULL;
