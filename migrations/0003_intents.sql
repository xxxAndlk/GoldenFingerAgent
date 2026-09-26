-- 0003_intents.sql：常驻意图——事件触发的前瞻记忆
-- （“当……时提醒我”），OpenClaw 风格：确定性关键词匹配，带
-- 冷却 / 触发预算 / 过期，仅显式取消。
CREATE TABLE IF NOT EXISTS standing_intent (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id    UUID NOT NULL REFERENCES app_user(id),
    description      TEXT NOT NULL,
    -- OR-of-ANDs：[[“张阿姨”,“来电话”],[“张妈”,“电话”]] — 消息中至少
    -- 一个分组的每个词都出现，才算命中。
    trigger_groups   JSONB NOT NULL DEFAULT '[]'::jsonb,
    status           TEXT NOT NULL DEFAULT 'armed'
                     CHECK (status IN ('pending','armed','fired','done','cancelled','expired')),
    fire_count       INT NOT NULL DEFAULT 0,
    max_fires        INT NOT NULL DEFAULT 3,
    cooldown_seconds INT NOT NULL DEFAULT 86400,
    last_fired_at    TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS standing_intent_owner_idx
    ON standing_intent (owner_user_id, status);
