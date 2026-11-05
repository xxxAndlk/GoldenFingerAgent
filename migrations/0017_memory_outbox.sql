-- 0017_memory_outbox.sql — 端侧共享记忆的持久上传队列。
-- Append 先落此表再异步上传；网络恢复/重启后按 next_retry_at 续传。

CREATE TABLE IF NOT EXISTS memory_sync_outbox (
    owner_user_id  TEXT NOT NULL,
    source_id      TEXT NOT NULL,
    payload_json   TEXT NOT NULL,
    content_hash   TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending', 'sent', 'failed', 'paused')),
    attempts       INTEGER NOT NULL DEFAULT 0,
    next_retry_at  DATETIME,
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL,
    sent_at        DATETIME,
    PRIMARY KEY (owner_user_id, source_id)
);
CREATE INDEX IF NOT EXISTS memory_sync_outbox_due_idx
    ON memory_sync_outbox (status, next_retry_at);
