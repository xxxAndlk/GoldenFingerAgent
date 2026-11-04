-- 0011_work_tasks.sql — 长期 WorkTask、实时状态、来源关联、控制 Inbox 与交付物。
-- 只做加法迁移；提醒 task 的语义保持不变。所有查询按 owner_user_id 过滤。

CREATE TABLE work_task (
    id                  TEXT PRIMARY KEY,
    owner_user_id       TEXT NOT NULL REFERENCES app_user(id),
    title               TEXT NOT NULL,
    goal                TEXT NOT NULL,
    completion_criteria TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'draft' CHECK (status IN
        ('draft', 'queued', 'active', 'waiting_user', 'waiting_approval',
         'waiting_other', 'waiting_device', 'waiting_external', 'paused',
         'pending_acceptance', 'completed', 'failed', 'cancelled')),
    priority            INTEGER NOT NULL DEFAULT 0,
    current_job_id      TEXT NOT NULL DEFAULT '',
    current_session_id  TEXT NOT NULL DEFAULT '',
    profile             TEXT NOT NULL DEFAULT 'read' CHECK (profile IN ('read', 'internal', 'external')),
    source_trigger_id   TEXT NOT NULL DEFAULT '',
    idempotency_key     TEXT NOT NULL,
    version             INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL,
    completed_at        DATETIME,
    UNIQUE (owner_user_id, idempotency_key)
);
CREATE INDEX work_task_owner_status_idx
    ON work_task (owner_user_id, status, priority DESC, updated_at DESC);
CREATE INDEX work_task_source_trigger_idx
    ON work_task (owner_user_id, source_trigger_id);

CREATE TABLE work_task_live_state (
    task_id                TEXT PRIMARY KEY REFERENCES work_task(id) ON DELETE CASCADE,
    owner_user_id          TEXT NOT NULL REFERENCES app_user(id),
    stage                  TEXT NOT NULL DEFAULT '',
    summary                TEXT NOT NULL DEFAULT '',
    next_action            TEXT NOT NULL DEFAULT '',
    waiting_kind           TEXT NOT NULL DEFAULT 'none' CHECK (waiting_kind IN
        ('none', 'user', 'approval', 'other', 'device', 'external', 'timer')),
    waiting_payload_json   TEXT NOT NULL DEFAULT '{}',
    open_questions_json    TEXT NOT NULL DEFAULT '[]',
    confirmed_points_json  TEXT NOT NULL DEFAULT '[]',
    handoff_note           TEXT NOT NULL DEFAULT '',
    version                INTEGER NOT NULL DEFAULT 0,
    updated_at             DATETIME NOT NULL
);
CREATE INDEX work_task_live_owner_idx ON work_task_live_state (owner_user_id, updated_at DESC);

CREATE TABLE work_task_thread (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    task_id        TEXT NOT NULL REFERENCES work_task(id) ON DELETE CASCADE,
    thread_id      TEXT NOT NULL REFERENCES agent_thread(id),
    relation       TEXT NOT NULL DEFAULT 'related' CHECK (relation IN ('origin', 'related', 'output')),
    confidence     REAL NOT NULL DEFAULT 0,
    reason         TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL DEFAULT 'system',
    created_at     DATETIME NOT NULL,
    UNIQUE (task_id, thread_id, relation)
);
CREATE INDEX work_task_thread_task_idx ON work_task_thread (owner_user_id, task_id);
CREATE INDEX work_task_thread_thread_idx ON work_task_thread (owner_user_id, thread_id);

CREATE TABLE work_task_inbox (
    id                   TEXT PRIMARY KEY,
    owner_user_id        TEXT NOT NULL REFERENCES app_user(id),
    task_id              TEXT NOT NULL REFERENCES work_task(id) ON DELETE CASCADE,
    source_kind          TEXT NOT NULL DEFAULT 'user' CHECK (source_kind IN ('user', 'thread', 'control', 'system')),
    source_event_id      TEXT NOT NULL DEFAULT '',
    kind                 TEXT NOT NULL CHECK (kind IN
        ('steer', 'pause', 'resume', 'cancel', 'recover', 'system')),
    body                 TEXT NOT NULL DEFAULT '',
    payload_json         TEXT NOT NULL DEFAULT '{}',
    priority             INTEGER NOT NULL DEFAULT 0,
    status               TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'consumed', 'cancelled')),
    idempotency_key      TEXT NOT NULL,
    consumed_by_job_id   TEXT NOT NULL DEFAULT '',
    consumed_at          DATETIME,
    created_at           DATETIME NOT NULL,
    UNIQUE (owner_user_id, idempotency_key)
);
CREATE INDEX work_task_inbox_queue_idx
    ON work_task_inbox (task_id, status, priority DESC, created_at);
CREATE INDEX work_task_inbox_owner_idx ON work_task_inbox (owner_user_id, created_at DESC);

CREATE TABLE artifact (
    id                 TEXT PRIMARY KEY,
    owner_user_id      TEXT NOT NULL REFERENCES app_user(id),
    task_id            TEXT NOT NULL REFERENCES work_task(id) ON DELETE CASCADE,
    kind               TEXT NOT NULL DEFAULT 'text' CHECK (kind IN
        ('text', 'document', 'code', 'analysis', 'email_draft', 'file', 'external_ref')),
    title              TEXT NOT NULL DEFAULT '',
    storage_dir        TEXT NOT NULL DEFAULT '',
    current_version    INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'draft' CHECK (status IN
        ('draft', 'pending_acceptance', 'accepted', 'rejected', 'delivered', 'superseded')),
    created_by_job_id  TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL,
    updated_at         DATETIME NOT NULL
);
CREATE INDEX artifact_task_idx ON artifact (owner_user_id, task_id, updated_at DESC);

CREATE TABLE artifact_version (
    id                 TEXT PRIMARY KEY,
    owner_user_id      TEXT NOT NULL REFERENCES app_user(id),
    artifact_id        TEXT NOT NULL REFERENCES artifact(id) ON DELETE CASCADE,
    version            INTEGER NOT NULL,
    relative_path      TEXT NOT NULL DEFAULT '',
    content_text       TEXT NOT NULL DEFAULT '',
    content_hash       TEXT NOT NULL DEFAULT '',
    mime_type          TEXT NOT NULL DEFAULT 'text/plain',
    size_bytes         INTEGER NOT NULL DEFAULT 0,
    source_refs_json   TEXT NOT NULL DEFAULT '[]',
    change_summary     TEXT NOT NULL DEFAULT '',
    created_by_job_id  TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL,
    UNIQUE (artifact_id, version)
);
CREATE INDEX artifact_version_owner_idx ON artifact_version (owner_user_id, artifact_id, version DESC);

ALTER TABLE agent_job ADD COLUMN work_task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_job ADD COLUMN job_role TEXT NOT NULL DEFAULT 'primary';
ALTER TABLE chat_session ADD COLUMN work_task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_action_outbox ADD COLUMN work_task_id TEXT NOT NULL DEFAULT '';
CREATE INDEX agent_job_work_task_idx ON agent_job (work_task_id, created_at DESC);
CREATE INDEX chat_session_work_task_idx ON chat_session (work_task_id);
CREATE INDEX agent_action_outbox_work_task_idx ON agent_action_outbox (work_task_id, created_at);

-- 一个 WorkTask 同时最多一个 queued/running 的主执行；子 Agent 可并行。
CREATE UNIQUE INDEX agent_job_work_task_primary_uq
    ON agent_job (work_task_id)
    WHERE work_task_id <> '' AND job_role = 'primary' AND status IN ('queued', 'running');

-- 旧 Super-agent Job 回填为确定性 Task；ID 固定，重复迁移不会重复创建。
INSERT OR IGNORE INTO work_task
    (id, owner_user_id, title, goal, completion_criteria, status, priority,
     current_job_id, current_session_id, profile, source_trigger_id,
     idempotency_key, version, created_at, updated_at, completed_at)
SELECT
    'legacy-' || j.id,
    j.owner_user_id,
    substr(j.task, 1, 120),
    j.task,
    '',
    CASE
        WHEN j.status = 'completed' THEN 'completed'
        WHEN j.status = 'cancelled' THEN 'cancelled'
        WHEN j.status IN ('failed', 'timeout', 'interrupted') THEN 'failed'
        ELSE 'paused'
    END,
    j.priority,
    CASE WHEN j.status IN ('queued', 'running') THEN j.id ELSE '' END,
    j.session_id,
    CASE WHEN j.profile IN ('read', 'internal', 'external') THEN j.profile ELSE 'read' END,
    j.parent_trigger_id,
    'legacy:' || j.id,
    j.version,
    j.created_at,
    j.updated_at,
    CASE WHEN j.status = 'completed' THEN j.finished_at ELSE NULL END
FROM agent_job j
WHERE j.agent_id = 'superagent';

INSERT OR IGNORE INTO work_task_live_state
    (task_id, owner_user_id, stage, summary, next_action, waiting_kind,
     waiting_payload_json, open_questions_json, confirmed_points_json,
     handoff_note, version, updated_at)
SELECT
    t.id, t.owner_user_id,
    CASE t.status WHEN 'completed' THEN 'done' ELSE 'legacy' END,
    substr(t.goal, 1, 500),
    '',
    'none', '{}', '[]', '[]', '', 0, t.updated_at
FROM work_task t
WHERE t.id LIKE 'legacy-%';

INSERT OR IGNORE INTO work_task_thread
    (id, owner_user_id, task_id, thread_id, relation, confidence, reason, created_by, created_at)
SELECT
    'legacy-link-' || j.id || '-' || j.thread_id,
    j.owner_user_id, 'legacy-' || j.id, j.thread_id, 'origin',
    1.0, '旧 Super-agent Job 回填', 'migration', j.created_at
FROM agent_job j
WHERE j.agent_id = 'superagent' AND j.thread_id <> '';

UPDATE agent_job
SET work_task_id = 'legacy-' || id
WHERE agent_id = 'superagent' AND work_task_id = '';

UPDATE chat_session
SET work_task_id = COALESCE((
    SELECT j.work_task_id FROM agent_job j
    WHERE j.session_id = chat_session.id AND j.work_task_id <> ''
    ORDER BY j.created_at DESC LIMIT 1
), '')
WHERE work_task_id = '';

UPDATE agent_action_outbox
SET work_task_id = COALESCE((
    SELECT j.work_task_id FROM agent_job j
    WHERE j.id = agent_action_outbox.worker_task_id AND j.work_task_id <> ''
    LIMIT 1
), '')
WHERE work_task_id = '';
