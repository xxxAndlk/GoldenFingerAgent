-- 0016_cloud_memory.sql — 云端共享长期记忆：设备凭据与来源命名空间映射。
-- cloudserver 使用独立的 DB 文件运行同一套迁移；本机库也会建表但不使用。

CREATE TABLE IF NOT EXISTS cloud_device (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT NOT NULL,
    device_name    TEXT NOT NULL,
    token_hash     TEXT NOT NULL UNIQUE,          -- sha256(device token)，只存哈希
    scopes_json    TEXT NOT NULL DEFAULT '[]',
    created_at     DATETIME NOT NULL,
    last_seen_at   DATETIME,
    revoked_at     DATETIME
);
CREATE INDEX IF NOT EXISTS cloud_device_owner_idx ON cloud_device (owner_user_id, created_at);

-- mem_episode.source_id 是全局主键，云端保存
-- cloud:<owner>:<original_source_id>，这里维护双向映射与遗忘墓碑。
CREATE TABLE IF NOT EXISTS cloud_episode_meta (
    owner_user_id      TEXT NOT NULL,
    original_source_id TEXT NOT NULL,
    stored_source_id   TEXT NOT NULL UNIQUE,
    device_id          TEXT NOT NULL DEFAULT '',
    content_hash       TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL,
    forgotten_at       DATETIME,
    PRIMARY KEY (owner_user_id, original_source_id)
);
CREATE INDEX IF NOT EXISTS cloud_episode_meta_stored_idx ON cloud_episode_meta (stored_source_id);
