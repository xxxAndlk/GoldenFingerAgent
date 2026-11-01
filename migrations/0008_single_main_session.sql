-- 单用户单大脑：每个用户最多一个用户可见主会话。
-- Router / Worker / Thread 会话属于实现细节，不进入主会话选择。
CREATE UNIQUE INDEX chat_session_main_uq
    ON chat_session (owner_user_id) WHERE kind = 'main';
CREATE INDEX chat_session_visible_idx
    ON chat_session (owner_user_id, updated_at DESC)
    WHERE kind NOT IN ('worker', 'router');
