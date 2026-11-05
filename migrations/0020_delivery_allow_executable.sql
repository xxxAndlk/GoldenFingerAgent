-- 0020_delivery_allow_executable.sql — 交付 Outbox 必须保留用户的显式可执行文件许可。
-- 否则 Worker 上传时丢失该开关，云端会再次拒绝已明确允许的文件。

ALTER TABLE artifact_delivery_outbox ADD COLUMN allow_executable INTEGER NOT NULL DEFAULT 0;
