-- 0013_memory_policy_devices.sql — 记忆确认治理、个人策略与设备能力。

ALTER TABLE mem_edge ADD COLUMN status TEXT NOT NULL DEFAULT 'inferred';
ALTER TABLE mem_edge ADD COLUMN scope_type TEXT NOT NULL DEFAULT 'global';
ALTER TABLE mem_edge ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE mem_edge ADD COLUMN condition_text TEXT NOT NULL DEFAULT '';
ALTER TABLE mem_edge ADD COLUMN confidence REAL NOT NULL DEFAULT 0.5;
ALTER TABLE mem_edge ADD COLUMN version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mem_edge ADD COLUMN confirmed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE mem_edge ADD COLUMN confirmed_at DATETIME;
CREATE INDEX mem_edge_scope_idx ON mem_edge (owner_user_id, status, scope_type, scope_id);

CREATE TABLE personal_policy (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    policy_type    TEXT NOT NULL CHECK (policy_type IN
        ('focus', 'preference', 'boundary', 'sop', 'notification')),
    name           TEXT NOT NULL,
    content_json   TEXT NOT NULL DEFAULT '{}',
    scope_type     TEXT NOT NULL DEFAULT 'global' CHECK (scope_type IN
        ('global', 'task', 'channel', 'device')),
    scope_id       TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'disabled')),
    version        INTEGER NOT NULL DEFAULT 0,
    valid_from     DATETIME,
    valid_to       DATETIME,
    created_at     DATETIME NOT NULL,
    updated_at     DATETIME NOT NULL
);
CREATE INDEX personal_policy_owner_idx ON personal_policy (owner_user_id, status, policy_type);

CREATE TABLE device_environment (
    id              TEXT PRIMARY KEY,
    owner_user_id   TEXT NOT NULL REFERENCES app_user(id),
    name            TEXT NOT NULL DEFAULT '',
    device_type     TEXT NOT NULL DEFAULT '',
    online          INTEGER NOT NULL DEFAULT 0 CHECK (online IN (0, 1)),
    last_seen_at    DATETIME,
    revoked_at      DATETIME,
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL
);
CREATE INDEX device_environment_owner_idx ON device_environment (owner_user_id, revoked_at);

CREATE TABLE device_capability (
    id                 TEXT PRIMARY KEY,
    owner_user_id      TEXT NOT NULL REFERENCES app_user(id),
    device_id          TEXT NOT NULL REFERENCES device_environment(id) ON DELETE CASCADE,
    tool_name          TEXT NOT NULL,
    resource_scope_json TEXT NOT NULL DEFAULT '{}',
    work_dir           TEXT NOT NULL DEFAULT '',
    limits_json        TEXT NOT NULL DEFAULT '{}',
    created_at         DATETIME NOT NULL,
    UNIQUE (device_id, tool_name)
);

CREATE TABLE work_task_device_binding (
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    task_id        TEXT NOT NULL REFERENCES work_task(id) ON DELETE CASCADE,
    device_id      TEXT NOT NULL REFERENCES device_environment(id) ON DELETE CASCADE,
    capability_id  TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL,
    PRIMARY KEY (task_id, device_id)
);
