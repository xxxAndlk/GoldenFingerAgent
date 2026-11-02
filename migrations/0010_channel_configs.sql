-- 渠道级独立开关与全局开关。
-- 渠道正文仍统一落在 agent_trigger；这里只控制采集、摘要、索引、简报和自动 Worker。
-- 未写入记录的渠道按默认值处理：渠道开关全部关闭，全局开关保持开启。

CREATE TABLE agent_channel_config (
    owner_user_id      TEXT NOT NULL REFERENCES app_user(id),
    channel            TEXT NOT NULL,
    enabled            INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    summary_enabled    INTEGER NOT NULL DEFAULT 0 CHECK (summary_enabled IN (0, 1)),
    index_enabled      INTEGER NOT NULL DEFAULT 0 CHECK (index_enabled IN (0, 1)),
    brief_enabled      INTEGER NOT NULL DEFAULT 0 CHECK (brief_enabled IN (0, 1)),
    auto_worker_enabled INTEGER NOT NULL DEFAULT 0 CHECK (auto_worker_enabled IN (0, 1)),
    updated_at         DATETIME NOT NULL,
    PRIMARY KEY (owner_user_id, channel)
);

CREATE TABLE agent_global_config (
    owner_user_id           TEXT PRIMARY KEY REFERENCES app_user(id),
    router_enabled          INTEGER NOT NULL DEFAULT 1 CHECK (router_enabled IN (0, 1)),
    worker_enabled          INTEGER NOT NULL DEFAULT 1 CHECK (worker_enabled IN (0, 1)),
    daily_brief_enabled     INTEGER NOT NULL DEFAULT 1 CHECK (daily_brief_enabled IN (0, 1)),
    proactive_notify_enabled INTEGER NOT NULL DEFAULT 1 CHECK (proactive_notify_enabled IN (0, 1)),
    updated_at              DATETIME NOT NULL
);
