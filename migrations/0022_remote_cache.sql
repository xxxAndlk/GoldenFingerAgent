-- 0022_remote_cache.sql — 手机端的远程控制面缓存与离线指令队列。
--
-- 电脑是任务与简报的唯一事实源。手机只保存：
--   - remote_cache：最近一次成功读到的结果（展示用），带同步时间；
--   - remote_pending_command：电脑离线时排队的变更指令，带持久化幂等键。
--
-- 恢复后最多执行一次：重放时复用同一个 idempotency_key。

CREATE TABLE IF NOT EXISTS remote_cache (
    owner_user_id  TEXT NOT NULL,
    cache_key      TEXT NOT NULL,
    payload_json   TEXT NOT NULL DEFAULT '{}',
    synced_at      DATETIME NOT NULL,
    PRIMARY KEY (owner_user_id, cache_key)
);

CREATE TABLE IF NOT EXISTS remote_pending_command (
    idempotency_key TEXT PRIMARY KEY,
    owner_user_id   TEXT NOT NULL,
    kind            TEXT NOT NULL,
    payload_json    TEXT NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'done', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    result_json     TEXT NOT NULL DEFAULT '{}',
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS remote_pending_owner_idx
    ON remote_pending_command (owner_user_id, status, created_at);
