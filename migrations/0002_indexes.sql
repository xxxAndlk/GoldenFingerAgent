-- 0002_indexes.sql — 查询与向量相似度索引。

CREATE INDEX person_owner_idx ON person (owner_user_id) WHERE deleted_at IS NULL;
CREATE INDEX alias_person_idx ON alias (person_id);
CREATE INDEX alias_alias_idx ON alias (alias);

CREATE INDEX fact_person_idx ON fact (person_id) WHERE deleted_at IS NULL;
CREATE INDEX fact_embedding_idx ON fact USING hnsw (embedding vector_cosine_ops);

CREATE INDEX task_owner_idx ON task (owner_user_id, abs_time) WHERE deleted_at IS NULL;
CREATE INDEX task_status_idx ON task (status) WHERE deleted_at IS NULL;

CREATE INDEX reminder_fire_idx ON reminder (fire_at) WHERE state = 'pending';
CREATE INDEX reminder_task_idx ON reminder (task_id);

CREATE INDEX episode_owner_idx ON episode (owner_user_id) WHERE deleted_at IS NULL;
CREATE INDEX episode_embedding_idx ON episode USING hnsw (embedding vector_cosine_ops);

CREATE INDEX audit_owner_idx ON audit_log (owner_user_id, created_at);
CREATE INDEX consent_subject_idx ON consent (subject_user_id, scope) WHERE revoked_at IS NULL;

CREATE INDEX chat_message_session_idx ON chat_message (session_id, created_at);
