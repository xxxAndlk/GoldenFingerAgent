-- 0001_init.sql — 按需求文档第 7 节设计的 schema，外加聊天表（SQLite 方言）。
-- 状态列使用 TEXT + CHECK（比 PG ENUM 更便于前向迁移）。
-- id/时间列由 Go 显式传入（uuid.NewString() / time.Now()），不依赖 DB 默认值。

CREATE TABLE app_user (
    id            TEXT PRIMARY KEY,
    user_type     TEXT NOT NULL CHECK (user_type IN ('elder', 'general', 'child')),
    name          TEXT NOT NULL,
    tz            TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    guardian_id   TEXT,
    notif_prefs_json TEXT NOT NULL DEFAULT '{}',
    created_at    DATETIME NOT NULL,
    deleted_at    DATETIME
);

CREATE TABLE person (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    canonical_name TEXT NOT NULL,
    notes          TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL,
    deleted_at     DATETIME
);

CREATE TABLE alias (
    id        TEXT PRIMARY KEY,
    person_id TEXT NOT NULL REFERENCES person(id),
    alias     TEXT NOT NULL
);
CREATE UNIQUE INDEX alias_person_alias_uq ON alias (person_id, alias);

CREATE TABLE fact (
    id          TEXT PRIMARY KEY,
    person_id   TEXT NOT NULL REFERENCES person(id),
    fact_type   TEXT NOT NULL,
    value_text  TEXT NOT NULL,
    confidence  REAL NOT NULL DEFAULT 0,
    status      TEXT NOT NULL CHECK (status IN ('confirmed', 'inferred')),
    source_msg_id TEXT,
    valid_from  DATETIME NOT NULL,
    valid_to    DATETIME,
    embedding   BLOB,
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL,
    deleted_at  DATETIME
);

CREATE TABLE task (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    kind           TEXT NOT NULL CHECK (kind IN ('intent', 'fact', 'alarm', 'note')),
    schema_json   TEXT NOT NULL DEFAULT '{}',
    time_expr_raw  TEXT NOT NULL DEFAULT '',
    abs_time       DATETIME,
    deadline       DATETIME,
    status         TEXT NOT NULL CHECK (status IN
        ('draft', 'pending_confirm', 'scheduled', 'notified', 'done', 'snoozed', 'cancelled', 'expired')),
    confidence     REAL NOT NULL DEFAULT 0,
    source_msg_id  TEXT,
    linked_person_id TEXT REFERENCES person(id),
    event_template TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL,
    updated_at     DATETIME NOT NULL,
    deleted_at     DATETIME
);

CREATE TABLE reminder (
    id         TEXT PRIMARY KEY,
    task_id    TEXT NOT NULL REFERENCES task(id),
    fire_at    DATETIME NOT NULL,
    channel    TEXT NOT NULL DEFAULT 'app',
    level      INT NOT NULL DEFAULT 1,
    state      TEXT NOT NULL CHECK (state IN ('pending', 'sent', 'acked', 'failed')),
    dedupe_key TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL
);

CREATE TABLE episode (
    id            TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES app_user(id),
    summary       TEXT NOT NULL,
    raw_ref       TEXT NOT NULL DEFAULT '',
    time_start    DATETIME,
    time_end      DATETIME,
    embedding     BLOB,
    expires_at    DATETIME,
    created_at    DATETIME NOT NULL,
    deleted_at    DATETIME
);

CREATE TABLE audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_user_id TEXT,
    actor         TEXT NOT NULL,
    action        TEXT NOT NULL,
    target        TEXT NOT NULL,
    detail_json  TEXT NOT NULL DEFAULT '{}',
    created_at    DATETIME NOT NULL
);

CREATE TABLE consent (
    id                      TEXT PRIMARY KEY,
    subject_user_id         TEXT NOT NULL REFERENCES app_user(id),
    scope                   TEXT NOT NULL,
    granted_by_guardian_id  TEXT,
    granted_at              DATETIME NOT NULL,
    revoked_at              DATETIME
);

-- 聊天持久化（Web 演示与多轮澄清状态所需）。
CREATE TABLE chat_session (
    id            TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES app_user(id),
    state_json   TEXT NOT NULL DEFAULT '{}',
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL
);

CREATE TABLE chat_message (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES chat_session(id),
    role            TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
    content         TEXT NOT NULL DEFAULT '',
    tool_calls_json TEXT,
    tool_call_id    TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL
);
