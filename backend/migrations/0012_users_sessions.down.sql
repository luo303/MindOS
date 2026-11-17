-- 回滚 0012：删会话与用户。
--
-- 顺序必须先会话后用户（外键）。回滚会丢掉所有人的登录态，
-- 业务数据不受影响，但 owner_id 的外键仍然指向 users，所以 0013 必须先回滚。
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
