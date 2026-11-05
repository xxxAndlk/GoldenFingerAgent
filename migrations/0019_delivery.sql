-- 0019_delivery.sql — 云端临时交付区与电脑端上传 Outbox。
-- cloud_delivery 只在 cloudserver 使用；artifact_delivery_outbox 只在端侧使用。

CREATE TABLE IF NOT EXISTS cloud_delivery (
    delivery_id       TEXT PRIMARY KEY,
    owner_user_id     TEXT NOT NULL,
    uploaded_by_device TEXT NOT NULL,
    target_device_id  TEXT NOT NULL,
    artifact_id       TEXT NOT NULL,
    artifact_version  INTEGER NOT NULL,
    name              TEXT NOT NULL,
    mime_type         TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes        INTEGER NOT NULL,
    sha256            TEXT NOT NULL,
    storage_path      TEXT NOT NULL,
    idempotency_key   TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'revoked', 'expired')),
    created_at        DATETIME NOT NULL,
    expires_at        DATETIME NOT NULL,
    acked_at          DATETIME,
    deleted_at        DATETIME,
    UNIQUE (owner_user_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS cloud_delivery_inbox_idx
    ON cloud_delivery (target_device_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS cloud_delivery_expiry_idx
    ON cloud_delivery (status, expires_at);

CREATE TABLE IF NOT EXISTS artifact_delivery_outbox (
    idempotency_key   TEXT PRIMARY KEY,
    owner_user_id     TEXT NOT NULL,
    artifact_id       TEXT NOT NULL,
    artifact_version  INTEGER NOT NULL,
    target_device_id  TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'uploading', 'done', 'failed')),
    attempts          INTEGER NOT NULL DEFAULT 0,
    next_retry_at     DATETIME,
    last_error        TEXT NOT NULL DEFAULT '',
    delivery_id       TEXT NOT NULL DEFAULT '',
    created_at        DATETIME NOT NULL,
    completed_at      DATETIME
);
CREATE INDEX IF NOT EXISTS artifact_delivery_outbox_due_idx
    ON artifact_delivery_outbox (status, next_retry_at);
