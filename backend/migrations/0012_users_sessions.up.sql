-- 0012：用户与会话。
--
-- 这两张表是多租户的地基：users 是隔离域，sessions 是登录态。
--
-- 为什么会话落库而不是发 JWT：撤销必须立刻生效（删一行），而 JWT 要维护黑名单；
-- 本服务每条请求本来就要查库，多一次带索引的查找换"能撤销"是划算的。
-- token 只存 sha256：库被 dump 时拿到哈希换不出 token（token 是 32 字节随机值）。
CREATE TABLE users (
    id            bigserial   PRIMARY KEY,
    email         text        NOT NULL,
    password_hash text        NOT NULL,
    display_name  text        NOT NULL DEFAULT '',
    status        text        NOT NULL DEFAULT 'active',
    failed_logins integer     NOT NULL DEFAULT 0,
    locked_until  timestamptz,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    -- 存小写：查询与唯一索引只认一种写法，避免 Alice@x.com 与 alice@x.com 变成两个账号。
    CONSTRAINT users_email_check CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
    CONSTRAINT users_status_check CHECK (status IN ('active', 'disabled')),
    CONSTRAINT users_failed_logins_check CHECK (failed_logins >= 0)
);
CREATE UNIQUE INDEX users_email_uniq ON users (email);

CREATE TABLE sessions (
    id           bigserial   PRIMARY KEY,
    user_id      bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   bytea       NOT NULL,
    user_agent   text        NOT NULL DEFAULT '',
    client_ip    text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,

    CONSTRAINT sessions_token_hash_check CHECK (length(token_hash) = 32)
);

-- 认证路径上唯一的查询：按 token 哈希找会话。
CREATE UNIQUE INDEX sessions_token_hash_uniq ON sessions (token_hash);
-- "踢掉某人的其他设备"要按用户列会话；清理过期会话按 expires_at。
CREATE INDEX sessions_user_idx ON sessions (user_id, created_at DESC);
CREATE INDEX sessions_expire_idx ON sessions (expires_at);
