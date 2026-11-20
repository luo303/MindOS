-- 0018_qa_runs_cancelled.down.sql

UPDATE qa_runs SET verdict = 'failed' WHERE verdict = 'cancelled';

ALTER TABLE qa_runs DROP CONSTRAINT IF EXISTS qa_runs_verdict_check;
ALTER TABLE qa_runs ADD CONSTRAINT qa_runs_verdict_check
    CHECK (verdict IN ('answered', 'refused', 'ungrounded', 'failed'));
