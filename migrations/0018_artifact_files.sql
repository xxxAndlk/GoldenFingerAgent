-- 0018_artifact_files.sql — Artifact 版本从“文本内容”扩展为通用不可变内容。
-- 历史文本版本默认 storage_kind='text'，不强制迁移。

ALTER TABLE artifact_version ADD COLUMN storage_kind TEXT NOT NULL DEFAULT 'text';
ALTER TABLE artifact_version ADD COLUMN original_name TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_version ADD COLUMN source_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_version ADD COLUMN source_ref TEXT NOT NULL DEFAULT '';
