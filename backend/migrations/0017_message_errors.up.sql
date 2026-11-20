-- 0017_message_errors.up.sql
--
-- 失败的 assistant 消息也要落库：否则刷新后只剩 user 消息，
-- 前端无法还原"模型失败 + 原因"的卡片，只能显示空回答。

ALTER TABLE messages
    ADD COLUMN error_code    text,
    ADD COLUMN error_message text;
