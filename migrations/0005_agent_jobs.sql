-- 可靠的异步 Agent 任务、事件与通知。任务结果和通知分别持久化，通知未确认前可补拉。
CREATE TABLE agent_job (
    id               TEXT PRIMARY KEY,
    owner_user_id    TEXT NOT NULL REFERENCES app_user(id),
    agent_id         TEXT NOT NULL,
    task             TEXT NOT NULL,
    work_dir         TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN
        ('queued', 'running', 'completed', 'failed', 'timeout', 'cancelled', 'interrupted')),
    idempotency_key  TEXT NOT NULL,
    external_task_id TEXT NOT NULL DEFAULT '',
    output           TEXT NOT NULL DEFAULT '',
    error            TEXT NOT NULL DEFAULT '',
    attempt          INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    started_at       DATETIME,
    finished_at      DATETIME,
    heartbeat_at     DATETIME
);
CREATE UNIQUE INDEX agent_job_idempotency_uq ON agent_job (owner_user_id, idempotency_key);
CREATE INDEX agent_job_queue_idx ON agent_job (status, created_at);
CREATE INDEX agent_job_owner_idx ON agent_job (owner_user_id, created_at DESC);

CREATE TABLE agent_job_event (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT NOT NULL REFERENCES agent_job(id),
    type        TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL
);
CREATE INDEX agent_job_event_job_idx ON agent_job_event (job_id, id);

CREATE TABLE agent_notification (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    job_id         TEXT NOT NULL REFERENCES agent_job(id),
    body           TEXT NOT NULL,
    created_at     DATETIME NOT NULL,
    read_at        DATETIME
);
CREATE INDEX agent_notification_unread_idx ON agent_notification (owner_user_id, read_at, id);
