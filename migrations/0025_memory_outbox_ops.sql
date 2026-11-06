-- 0025_memory_outbox_ops.sql — 共享记忆的待发队列支持删除操作。
--
-- 写入和删除都要能断网续传：只在联网时直接删云端，
-- 断网时删除会丢失，云端旧内容永久留着、另一端仍能召回。
ALTER TABLE memory_sync_outbox ADD COLUMN op TEXT NOT NULL DEFAULT 'upsert';
