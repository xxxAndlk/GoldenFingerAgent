-- 0004_inbox.sql — 通知中心：收件箱事件 / 稍后处理 / 关注规则（SQLite 方言）。
-- id 与时间列由 Go 显式传入，不依赖 DB 默认值；状态列 TEXT 便于前向迁移。

CREATE TABLE inbox_event (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT NOT NULL,
    source_pkg     TEXT,
    app_name       TEXT,
    title          TEXT,
    text           TEXT,
    kind           TEXT NOT NULL DEFAULT 'normal',
    clue_type      TEXT,
    clue_value     TEXT,
    posted_at      DATETIME,
    ingested_at    DATETIME NOT NULL,
    dedupe_hash    TEXT NOT NULL,
    embedding      BLOB,
    UNIQUE(owner_user_id, dedupe_hash)
);

CREATE INDEX idx_inbox_event_owner_ingested ON inbox_event(owner_user_id, ingested_at);
CREATE INDEX idx_inbox_event_owner_kind ON inbox_event(owner_user_id, kind);

CREATE TABLE inbox_snooze (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT,
    event_id       TEXT NOT NULL,
    fire_at        DATETIME NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    created_at     DATETIME NOT NULL
);

CREATE INDEX idx_inbox_snooze_owner_status_fire ON inbox_snooze(owner_user_id, status, fire_at);

CREATE TABLE inbox_watch (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT,
    title          TEXT,
    keyword        TEXT NOT NULL,
    source_pkg     TEXT,
    status         TEXT NOT NULL DEFAULT 'armed',
    created_at     DATETIME NOT NULL
);

CREATE INDEX idx_inbox_watch_owner_status ON inbox_watch(owner_user_id, status);
