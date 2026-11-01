-- 渠道采集策略、跳过计数与跨渠道每日简报。
-- 原始消息仍统一落在 agent_trigger；这里只保存策略、统计和派生简报。

CREATE TABLE agent_channel_policy (
    owner_user_id TEXT NOT NULL REFERENCES app_user(id),
    channel       TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT '',
    allowed       INTEGER NOT NULL CHECK (allowed IN (0, 1)),
    updated_at    DATETIME NOT NULL,
    PRIMARY KEY (owner_user_id, channel, source)
);

CREATE TABLE agent_channel_skip (
    owner_user_id TEXT NOT NULL REFERENCES app_user(id),
    channel       TEXT NOT NULL,
    day           TEXT NOT NULL,
    count         INTEGER NOT NULL DEFAULT 0,
    updated_at    DATETIME NOT NULL,
    PRIMARY KEY (owner_user_id, channel, day)
);

CREATE TABLE agent_daily_brief (
    id              TEXT PRIMARY KEY,
    owner_user_id   TEXT NOT NULL REFERENCES app_user(id),
    brief_date      TEXT NOT NULL,
    version         INTEGER NOT NULL DEFAULT 1,
    content_md      TEXT NOT NULL DEFAULT '',
    sections_json   TEXT NOT NULL DEFAULT '{}',
    source_refs_json TEXT NOT NULL DEFAULT '[]',
    model           TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'ready' CHECK (status IN ('ready', 'delivered', 'failed')),
    delivered_at    DATETIME,
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL,
    UNIQUE (owner_user_id, brief_date, version)
);
CREATE INDEX agent_daily_brief_latest_idx
    ON agent_daily_brief (owner_user_id, brief_date, version DESC);

CREATE INDEX agent_trigger_channel_idx
    ON agent_trigger (owner_user_id, channel, received_at DESC);

-- IMAP 等外部采集器的单调游标；只保存来源位置，不保存凭据或正文。
CREATE TABLE agent_channel_cursor (
    owner_user_id TEXT NOT NULL REFERENCES app_user(id),
    channel       TEXT NOT NULL,
    source        TEXT NOT NULL,
    cursor        TEXT NOT NULL DEFAULT '',
    updated_at    DATETIME NOT NULL,
    PRIMARY KEY (owner_user_id, channel, source)
);
