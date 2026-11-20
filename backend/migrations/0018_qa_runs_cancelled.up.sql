-- 0018_qa_runs_cancelled.up.sql
--
-- 用户主动停止不是失败：消息按正常 assistant 内容保存，
-- 运行记录用 cancelled 与真正的 failed（超时/上游错误）区分。

ALTER TABLE qa_runs DROP CONSTRAINT IF EXISTS qa_runs_verdict_check;
ALTER TABLE qa_runs ADD CONSTRAINT qa_runs_verdict_check
    CHECK (verdict IN ('answered', 'refused', 'ungrounded', 'failed', 'cancelled'));
