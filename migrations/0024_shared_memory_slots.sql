-- 跨端共享记忆的「槽位」：记录某个位置（简报日期、联系人备注等）当前对应
-- 哪一条长期记忆来源 ID，以及它的版本号与写入时间。
--
-- 为什么需要它：
--   1. 云端不允许同一个来源 ID 改内容，内容更新必须换新来源 ID，
--      旧版本要靠这张表找回来删除，否则新旧两条会同时留在云端。
--   2. 同一槽位可能由两端先后修改，必须记住「哪个更新」，
--      否则一个离线很久的旧请求恢复后会把新内容覆盖掉。
CREATE TABLE shared_memory_slot (
    -- 不加外键：同一张表在端侧记本机 owner，在云端记云端 owner，
    -- 两边的 owner 命名空间不同（与 cloud_episode_meta 一致）。
    owner_user_id      TEXT NOT NULL,
    slot               TEXT NOT NULL,
    source_id          TEXT NOT NULL,
    revision           INTEGER NOT NULL DEFAULT 1,
    -- 来源写入时间（毫秒）：客户端提供，云端据此判断新旧。
    source_updated_at  INTEGER NOT NULL DEFAULT 0,
    created_at         DATETIME NOT NULL,
    modified_at        DATETIME NOT NULL,
    PRIMARY KEY (owner_user_id, slot)
);
CREATE INDEX shared_memory_slot_prefix_idx
    ON shared_memory_slot (owner_user_id, slot);
