-- 0021_agent_checkpoint.up.sql
CREATE TABLE agent_checkpoints (
    checkpoint_id   text PRIMARY KEY,
    owner_id        bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    conversation_id bigint REFERENCES conversations(id) ON DELETE CASCADE,
    payload         bytea NOT NULL,
    expires_at      timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE agent_approvals
    ADD COLUMN checkpoint_id  text,
    ADD COLUMN interrupt_id   text,
    ADD COLUMN partial_answer text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX agent_approvals_checkpoint_idx
    ON agent_approvals (checkpoint_id)
    WHERE checkpoint_id IS NOT NULL;
