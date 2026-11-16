-- 0009_chat_models.up.sql
--
-- 对话模型由用户配置并落库：哪家便宜、哪个模型中文好，一年一变；
-- 写进代码等于每次换模型都要发版，写进 .env 又要求改文件还得重启。
--
-- 这张表只存对话模型。向量模型仍在 .env：它的维度绑在
-- document_chunks.embedding 的列类型上（vector(1024)），改它等于改表结构，
-- 属于运维动作而不是设置项。
--
-- 为什么没有 kind 列：一张只存一种东西的表加一个恒为 'chat' 的字段，
-- 只会让人以为它还能存别的（见 docs/design/rag.md §7.1）。

CREATE TABLE IF NOT EXISTS chat_models (
    id             bigserial PRIMARY KEY,
    -- name 是用户自己起的名字，用来在下拉框里区分同家的不同模型。
    name           text        NOT NULL,
    base_url       text        NOT NULL,
    model          text        NOT NULL,
    -- 密文格式 nonce||ciphertext（AES-256-GCM）。NULL 表示该端点不需要 Key。
    -- 为什么不存明文：库会跟着 pg_dump、备份、导出一起走，
    -- 一份 dump 发给别人排查问题就等于把 Key 一起发了出去。
    api_key_cipher bytea,
    -- 末 4 位，只用于界面确认"是不是这把"，不参与构造请求。
    api_key_hint   text        NOT NULL DEFAULT '',
    temperature    real,
    max_tokens     integer,
    is_default     boolean     NOT NULL DEFAULT false,
    last_used_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT chat_models_name_check CHECK (length(btrim(name)) BETWEEN 1 AND 64),
    CONSTRAINT chat_models_base_url_check CHECK (base_url ~ '^https?://'),
    CONSTRAINT chat_models_model_check CHECK (length(btrim(model)) BETWEEN 1 AND 128),
    -- temperature 的合法区间随厂商而变（多数 0~2，个别只接受 0~1），
    -- 这里按宽松的上界校验：真正越界的值由厂商返回 400，我们再翻译成可读提示。
    CONSTRAINT chat_models_temperature_check CHECK (temperature IS NULL OR (temperature >= 0 AND temperature <= 2)),
    CONSTRAINT chat_models_max_tokens_check CHECK (max_tokens IS NULL OR max_tokens > 0)
);

-- 重名会让提问页的下拉框出现两个无法区分的选择，所以在库层面禁掉。
CREATE UNIQUE INDEX IF NOT EXISTS chat_models_name_uniq ON chat_models (name);

-- 部分唯一索引：只约束 is_default = true 的行，从而"最多一个默认"。
-- 用索引而不是应用层判断，是因为"设默认"要先把旧默认置 false，
-- 两个请求交错时应用层检查会双双通过；唯一索引让第二次直接失败。
CREATE UNIQUE INDEX IF NOT EXISTS chat_models_default_uniq ON chat_models (is_default) WHERE is_default;
