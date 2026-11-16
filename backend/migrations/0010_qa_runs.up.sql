-- 0010_qa_runs.up.sql
--
-- 一行 = 一次问答的完整运行记录。它是这套系统的质量底账：
-- 没有它，"拒答率""为什么这次判成 ungrounded"只能靠人试问感觉。
--
-- 刻意不存的两样东西：
--   1. 检索到的分块正文。retrieved 只存 chunk_id 与两路分数——
--      存正文等于把知识库复制一份，体积翻倍且多一处泄漏面。
--   2. 模型 API Key 或任何凭据。这里只记"用了哪条配置"。

CREATE TABLE IF NOT EXISTS qa_runs (
    id                bigserial PRIMARY KEY,
    question          text        NOT NULL,
    -- answer 允许空串：请求 cancelled 时也留一行，此时没有答案。
    answer            text        NOT NULL DEFAULT '',
    -- 模型原始输出。判 ungrounded 时 answer 已被换成拒答文案，
    -- 只有这里还能看到模型到底写了什么。
    raw_answer        text,
    verdict           text        NOT NULL,
    verdict_reason    text,
    citations         jsonb       NOT NULL DEFAULT '[]',
    retrieved         jsonb       NOT NULL DEFAULT '[]',
    -- 不设外键：配置会被删，而历史记录不该因此消失或被禁止删除。
    -- 同时冻结一份模型名，配置改名后仍能看出当时用的是哪个模型。
    chat_model_id     bigint,
    model             text,
    prompt_tokens     bigint      NOT NULL DEFAULT 0,
    completion_tokens bigint      NOT NULL DEFAULT 0,
    latency_ms        integer     NOT NULL DEFAULT 0,
    error_code        text,
    created_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT qa_runs_question_check CHECK (length(btrim(question)) > 0),
    -- 三个值是服务端裁决的全部输出。写死在这里，是为了让"多了一种状态"
    -- 必须先改表，而不是在代码里悄悄多一个没人处理的分支。
    CONSTRAINT qa_runs_verdict_check CHECK (verdict IN ('answered', 'refused', 'ungrounded')),
    CONSTRAINT qa_runs_latency_check CHECK (latency_ms >= 0),
    CONSTRAINT qa_runs_tokens_check CHECK (prompt_tokens >= 0 AND completion_tokens >= 0)
);

-- 复盘与统计都是"最近发生了什么"，所以按时间倒序建索引。
CREATE INDEX IF NOT EXISTS qa_runs_created_at_idx ON qa_runs (created_at DESC);
-- 算拒答率时按 verdict 分组，单独一个索引让这类查询不必扫全表。
CREATE INDEX IF NOT EXISTS qa_runs_verdict_idx ON qa_runs (verdict, created_at DESC);
