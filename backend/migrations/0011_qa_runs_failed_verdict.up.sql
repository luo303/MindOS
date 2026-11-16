-- 0011_qa_runs_failed_verdict.up.sql
--
-- 给 verdict 增加第四个值 failed。
--
-- 起因：一次问答可能既没有答案、也不是"拒答"——模型 429、上游超时、
-- 客户端中途断开都会走到这里。把它们硬塞进 refused 会污染拒答率
-- （那是最核心的质量指标），塞进 ungrounded 又会让"系统缺陷"这个
-- 信号失真。诚实的做法是多一个状态。
--
-- 注意 error_code 与 verdict=failed 是两件事：verdict 说"这次没有结果"，
-- error_code 说"为什么"。cancelled 也是 failed 的一种。

ALTER TABLE qa_runs DROP CONSTRAINT IF EXISTS qa_runs_verdict_check;

ALTER TABLE qa_runs ADD CONSTRAINT qa_runs_verdict_check
    CHECK (verdict IN ('answered', 'refused', 'ungrounded', 'failed'));
