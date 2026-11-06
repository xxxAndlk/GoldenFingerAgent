-- 0021_conversation_cursors.sql — 按设备的会话已读游标。
--
-- 未读数必须按设备分别记录：手机读过一条任务后，电脑的角标不能跟着消失。
-- 每个 (owner, device, conversation) 一行，记录该设备读到的最后一条消息 rowid。
-- 用 rowid 而不是时间戳：时间戳受时区与精度影响，rowid 严格递增。

CREATE TABLE IF NOT EXISTS conversation_read_cursor (
    owner_user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    device_id        TEXT NOT NULL,
    conversation_id  TEXT NOT NULL,
    read_rowid       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (owner_user_id, device_id, conversation_id)
);
CREATE INDEX IF NOT EXISTS conversation_cursor_owner_idx
    ON conversation_read_cursor (owner_user_id, device_id);
