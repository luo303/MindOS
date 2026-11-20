-- 0020_agent_approvals.down.sql

ALTER TABLE todos DROP CONSTRAINT IF EXISTS todos_approval_fk;
DROP TABLE IF EXISTS agent_approvals;
