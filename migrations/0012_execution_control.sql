-- 0012_execution_control.sql — 安全步骤检查点、动作授权与工具调用对账。

CREATE TABLE work_task_checkpoint (
    id                 TEXT PRIMARY KEY,
    owner_user_id      TEXT NOT NULL REFERENCES app_user(id),
    task_id            TEXT NOT NULL REFERENCES work_task(id) ON DELETE CASCADE,
    job_id             TEXT NOT NULL,
    session_id         TEXT NOT NULL DEFAULT '',
    seq                INTEGER NOT NULL,
    task_version       INTEGER NOT NULL DEFAULT 0,
    kind               TEXT NOT NULL CHECK (kind IN
        ('turn_start', 'tool_done', 'waiting', 'turn_done', 'recovery')),
    summary            TEXT NOT NULL DEFAULT '',
    next_action        TEXT NOT NULL DEFAULT '',
    payload_json       TEXT NOT NULL DEFAULT '{}',
    created_at         DATETIME NOT NULL,
    UNIQUE (job_id, seq)
);
CREATE INDEX work_task_checkpoint_task_idx
    ON work_task_checkpoint (owner_user_id, task_id, created_at DESC);

CREATE TABLE action_grant (
    id                   TEXT PRIMARY KEY,
    owner_user_id        TEXT NOT NULL REFERENCES app_user(id),
    task_id              TEXT NOT NULL DEFAULT '',
    action_type          TEXT NOT NULL,
    resource_scope_json  TEXT NOT NULL DEFAULT '{}',
    risk_level           TEXT NOT NULL DEFAULT 'read' CHECK (risk_level IN
        ('read', 'prepare', 'external_write', 'sensitive')),
    status               TEXT NOT NULL DEFAULT 'active' CHECK (status IN
        ('active', 'revoked', 'expired', 'exhausted')),
    remaining_uses       INTEGER NOT NULL DEFAULT -1,
    approved_by          TEXT NOT NULL DEFAULT '',
    approved_at          DATETIME,
    expires_at           DATETIME,
    revoked_at           DATETIME,
    version              INTEGER NOT NULL DEFAULT 0,
    created_at           DATETIME NOT NULL,
    updated_at           DATETIME NOT NULL
);
CREATE INDEX action_grant_task_idx ON action_grant (owner_user_id, task_id, status);
CREATE INDEX action_grant_expiry_idx ON action_grant (status, expires_at);

CREATE TABLE tool_invocation (
    id                   TEXT PRIMARY KEY,
    owner_user_id        TEXT NOT NULL REFERENCES app_user(id),
    task_id              TEXT NOT NULL DEFAULT '',
    job_id               TEXT NOT NULL DEFAULT '',
    session_id           TEXT NOT NULL DEFAULT '',
    tool_call_id         TEXT NOT NULL DEFAULT '',
    tool_name            TEXT NOT NULL,
    risk_level           TEXT NOT NULL DEFAULT 'read',
    args_redacted_json   TEXT NOT NULL DEFAULT '{}',
    args_hash            TEXT NOT NULL DEFAULT '',
    grant_id             TEXT NOT NULL DEFAULT '',
    idempotency_key      TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN
        ('proposed', 'approved', 'running', 'succeeded', 'failed', 'unknown', 'denied', 'cancelled')),
    result_summary       TEXT NOT NULL DEFAULT '',
    effect_receipt_json  TEXT NOT NULL DEFAULT '{}',
    started_at           DATETIME,
    finished_at          DATETIME,
    created_at           DATETIME NOT NULL,
    UNIQUE (owner_user_id, idempotency_key)
);
CREATE INDEX tool_invocation_task_idx ON tool_invocation (owner_user_id, task_id, created_at DESC);
CREATE INDEX tool_invocation_job_idx ON tool_invocation (job_id, created_at DESC);

ALTER TABLE agent_action_outbox ADD COLUMN tool_invocation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_action_outbox ADD COLUMN action_grant_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_action_outbox ADD COLUMN expected_effect_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agent_action_outbox ADD COLUMN reconciled_at DATETIME;
