-- 回到只允许三个值。执行前请先处理 verdict = 'failed' 的行，
-- 否则约束会拒绝建立并让整个回滚失败。
ALTER TABLE qa_runs DROP CONSTRAINT IF EXISTS qa_runs_verdict_check;

ALTER TABLE qa_runs ADD CONSTRAINT qa_runs_verdict_check
    CHECK (verdict IN ('answered', 'refused', 'ungrounded'));
