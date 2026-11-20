-- 0023_one_pending_approval.up.sql
--
-- 同一会话同一时间最多一条待审批，避免前端/并发创建出两条。
CREATE UNIQUE INDEX agent_approvals_one_pending_per_conversation
    ON agent_approvals (conversation_id)
    WHERE status = 'pending';
