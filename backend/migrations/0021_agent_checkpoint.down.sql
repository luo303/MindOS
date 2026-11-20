-- 0021_agent_checkpoint.down.sql
DROP INDEX IF EXISTS agent_approvals_checkpoint_idx;
ALTER TABLE agent_approvals
    DROP COLUMN IF EXISTS partial_answer,
    DROP COLUMN IF EXISTS interrupt_id,
    DROP COLUMN IF EXISTS checkpoint_id;
DROP TABLE IF EXISTS agent_checkpoints;
