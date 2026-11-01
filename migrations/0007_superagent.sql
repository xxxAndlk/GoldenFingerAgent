-- Super-agent 地基：Thread、Trigger、订阅、审批发件箱与持久事件日志。
-- 全部为加法迁移；旧表、旧接口保持不变。

CREATE TABLE agent_thread (
    id                 TEXT PRIMARY KEY,
    owner_user_id      TEXT NOT NULL REFERENCES app_user(id),
    channel            TEXT NOT NULL,
    external_id        TEXT NOT NULL DEFAULT '',
    title              TEXT NOT NULL DEFAULT '',
    participants_json  TEXT NOT NULL DEFAULT '[]',
    summary            TEXT NOT NULL DEFAULT '',
    summary_event_id   INTEGER NOT NULL DEFAULT 0,
    summary_updated_at DATETIME,
    status             TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    retention_class    TEXT NOT NULL DEFAULT 'default',
    last_event_at      DATETIME,
    created_at         DATETIME NOT NULL,
    updated_at         DATETIME NOT NULL
);
CREATE UNIQUE INDEX agent_thread_external_uq
    ON agent_thread (owner_user_id, channel, external_id) WHERE external_id <> '';
CREATE INDEX agent_thread_owner_idx ON agent_thread (owner_user_id, updated_at DESC);

CREATE TABLE agent_thread_link (
    id               TEXT PRIMARY KEY,
    owner_user_id    TEXT NOT NULL REFERENCES app_user(id),
    thread_id        TEXT NOT NULL REFERENCES agent_thread(id),
    linked_thread_id TEXT NOT NULL REFERENCES agent_thread(id),
    relation         TEXT NOT NULL,
    confidence       REAL NOT NULL DEFAULT 0,
    reason           TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL,
    UNIQUE (owner_user_id, thread_id, linked_thread_id, relation)
);
CREATE INDEX agent_thread_link_thread_idx ON agent_thread_link (owner_user_id, thread_id);

CREATE TABLE agent_trigger (
    id                TEXT PRIMARY KEY,
    owner_user_id     TEXT NOT NULL REFERENCES app_user(id),
    channel           TEXT NOT NULL,
    external_id       TEXT NOT NULL DEFAULT '',
    thread_id         TEXT REFERENCES agent_thread(id),
    actor_json        TEXT NOT NULL DEFAULT '[]',
    kind              TEXT NOT NULL,
    title             TEXT NOT NULL DEFAULT '',
    body              TEXT NOT NULL DEFAULT '',
    payload_json      TEXT NOT NULL DEFAULT '{}',
    received_at       DATETIME NOT NULL,
    dedupe_key        TEXT NOT NULL,
    privacy_level     TEXT NOT NULL DEFAULT 'normal',
    trusted           INTEGER NOT NULL DEFAULT 0 CHECK (trusted IN (0, 1)),
    status            TEXT NOT NULL DEFAULT 'new' CHECK (status IN
        ('new', 'filtered', 'queued', 'ignored', 'routed', 'done', 'failed')),
    route_result_json TEXT NOT NULL DEFAULT '{}',
    error             TEXT NOT NULL DEFAULT '',
    created_at        DATETIME NOT NULL,
    updated_at        DATETIME NOT NULL,
    UNIQUE (owner_user_id, dedupe_key)
);
CREATE INDEX agent_trigger_owner_idx ON agent_trigger (owner_user_id, received_at DESC);
CREATE INDEX agent_trigger_status_idx ON agent_trigger (status, received_at);
CREATE INDEX agent_trigger_thread_idx ON agent_trigger (thread_id, received_at);

CREATE TABLE agent_thread_subscription (
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    thread_id      TEXT NOT NULL REFERENCES agent_thread(id),
    worker_task_id TEXT NOT NULL REFERENCES agent_job(id),
    created_at     DATETIME NOT NULL,
    PRIMARY KEY (thread_id, worker_task_id)
);
CREATE INDEX agent_thread_subscription_worker_idx ON agent_thread_subscription (worker_task_id);

CREATE TABLE agent_action_outbox (
    id                  TEXT PRIMARY KEY,
    owner_user_id       TEXT NOT NULL REFERENCES app_user(id),
    channel             TEXT NOT NULL,
    thread_id           TEXT NOT NULL DEFAULT '',
    worker_task_id      TEXT NOT NULL DEFAULT '',
    kind                TEXT NOT NULL,
    target_json         TEXT NOT NULL DEFAULT '{}',
    body                TEXT NOT NULL DEFAULT '',
    payload_json        TEXT NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL DEFAULT 'draft' CHECK (status IN
        ('draft', 'pending_approval', 'approved', 'sending', 'sent', 'failed',
         'send_unknown', 'rejected', 'cancelled')),
    requires_approval   INTEGER NOT NULL DEFAULT 1 CHECK (requires_approval IN (0, 1)),
    idempotency_key     TEXT NOT NULL,
    version             INTEGER NOT NULL DEFAULT 0,
    approved_by         TEXT NOT NULL DEFAULT '',
    approved_at         DATETIME,
    provider_message_id TEXT NOT NULL DEFAULT '',
    last_error          TEXT NOT NULL DEFAULT '',
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL,
    sent_at             DATETIME,
    UNIQUE (owner_user_id, idempotency_key)
);
CREATE INDEX agent_action_outbox_status_idx ON agent_action_outbox (owner_user_id, status, updated_at);

CREATE TABLE agent_event (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    kind           TEXT NOT NULL,
    entity_type    TEXT NOT NULL,
    entity_id      TEXT NOT NULL,
    payload_json   TEXT NOT NULL DEFAULT '{}',
    created_at     DATETIME NOT NULL
);
CREATE INDEX agent_event_owner_idx ON agent_event (owner_user_id, id);

-- WorkerTask 复用 agent_job；waiting_kind != 'none' 时 UI 展示 waiting，释放并发槽。
ALTER TABLE agent_job ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_job ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_job ADD COLUMN parent_trigger_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_job ADD COLUMN profile TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_job ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_job ADD COLUMN result_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_job ADD COLUMN waiting_kind TEXT NOT NULL DEFAULT 'none';
ALTER TABLE agent_job ADD COLUMN waiting_payload_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agent_job ADD COLUMN version INTEGER NOT NULL DEFAULT 0;
CREATE INDEX agent_job_thread_idx ON agent_job (thread_id, created_at DESC);
CREATE INDEX agent_job_parent_trigger_idx ON agent_job (parent_trigger_id);

-- 会话区分普通聊天、Worker 与内部旁白，并保留所属 Thread。
ALTER TABLE chat_session ADD COLUMN kind TEXT NOT NULL DEFAULT 'chat';
ALTER TABLE chat_session ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_session ADD COLUMN summary TEXT NOT NULL DEFAULT '';
CREATE INDEX chat_session_thread_idx ON chat_session (thread_id, created_at DESC);
