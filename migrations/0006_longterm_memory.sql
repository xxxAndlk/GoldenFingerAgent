-- 0006_longterm_memory.sql — 内置（纯 Go）长期记忆：原始片段 + 抽取后的事实边。
-- 取代原先 memory_service（Graphiti + Kuzu）的外部进程；
-- 不引入图数据库，时间语义与来源引用用普通列表达。
-- id/时间列由 Go 显式传入（uuid.NewString() / time.Now()），不依赖 DB 默认值。

-- mem_episode 保留送入长期记忆的原始证据，是幂等与失败重试的唯一事实来源。
CREATE TABLE mem_episode (
    source_id        TEXT PRIMARY KEY,            -- 幂等键，如 chat:xxx / episode:xxx / inbox:xxx
    owner_user_id    TEXT NOT NULL,
    text             TEXT NOT NULL,
    source           TEXT NOT NULL DEFAULT '',
    reference_time   DATETIME NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'done', 'failed')),
    attempts         INTEGER NOT NULL DEFAULT 0,
    last_error       TEXT NOT NULL DEFAULT '',
    embedding        BLOB,
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL
);
CREATE INDEX mem_episode_status_idx ON mem_episode (status, created_at);
CREATE INDEX mem_episode_owner_idx ON mem_episode (owner_user_id, reference_time);

-- mem_edge 是按主体/关系组织的抽取事实；同一关系的新值会把旧值标记为失效，
-- 而不是覆盖，从而保留“什么时候开始、什么时候不再成立”的时间语义。
CREATE TABLE mem_edge (
    id             TEXT PRIMARY KEY,
    owner_user_id  TEXT NOT NULL,
    subject        TEXT NOT NULL,                -- 归一化主体（人物/事物）
    relation       TEXT NOT NULL,                -- 归一化关系（工作/住址/喜好…）
    object         TEXT NOT NULL,                -- 归一化客体
    fact           TEXT NOT NULL,                -- 面向用户的完整事实句
    valid_from     DATETIME,
    valid_to       DATETIME,
    source_refs    TEXT NOT NULL DEFAULT '[]',   -- JSON 数组，元素为 mem_episode.source_id
    embedding      BLOB,
    created_at     DATETIME NOT NULL,
    updated_at     DATETIME NOT NULL,
    deleted_at     DATETIME
);
CREATE INDEX mem_edge_owner_idx ON mem_edge (owner_user_id, deleted_at);
CREATE INDEX mem_edge_triple_idx ON mem_edge (owner_user_id, subject, relation, deleted_at);
