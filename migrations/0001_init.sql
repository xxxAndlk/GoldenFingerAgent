-- 0001_init.sql — schema per requirements doc section 7, plus chat tables.
-- Status columns use TEXT + CHECK (simpler forward migrations than PG ENUM).

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE app_user (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_type     TEXT NOT NULL CHECK (user_type IN ('elder', 'general', 'child')),
    name          TEXT NOT NULL,
    tz            TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    guardian_id   UUID,
    notif_prefs_jsonb JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

CREATE TABLE person (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id  UUID NOT NULL REFERENCES app_user(id),
    canonical_name TEXT NOT NULL,
    notes          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ
);

CREATE TABLE alias (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    person_id UUID NOT NULL REFERENCES person(id),
    alias     TEXT NOT NULL
);
CREATE UNIQUE INDEX alias_person_alias_uq ON alias (person_id, alias);

CREATE TABLE fact (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    person_id   UUID NOT NULL REFERENCES person(id),
    fact_type   TEXT NOT NULL,
    value_text  TEXT NOT NULL,
    confidence  DOUBLE PRECISION NOT NULL DEFAULT 0,
    status      TEXT NOT NULL CHECK (status IN ('confirmed', 'inferred')),
    source_msg_id UUID,
    valid_from  TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to    TIMESTAMPTZ,
    embedding   VECTOR(1024),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE TABLE task (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id  UUID NOT NULL REFERENCES app_user(id),
    kind           TEXT NOT NULL CHECK (kind IN ('intent', 'fact', 'alarm', 'note')),
    schema_jsonb   JSONB NOT NULL DEFAULT '{}'::jsonb,
    time_expr_raw  TEXT NOT NULL DEFAULT '',
    abs_time       TIMESTAMPTZ,
    deadline       TIMESTAMPTZ,
    status         TEXT NOT NULL CHECK (status IN
        ('draft', 'pending_confirm', 'scheduled', 'notified', 'done', 'snoozed', 'cancelled', 'expired')),
    confidence     DOUBLE PRECISION NOT NULL DEFAULT 0,
    source_msg_id  UUID,
    linked_person_id UUID REFERENCES person(id),
    event_template TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ
);

CREATE TABLE reminder (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id    UUID NOT NULL REFERENCES task(id),
    fire_at    TIMESTAMPTZ NOT NULL,
    channel    TEXT NOT NULL DEFAULT 'app',
    level      INT NOT NULL DEFAULT 1,
    state      TEXT NOT NULL CHECK (state IN ('pending', 'sent', 'acked', 'failed')),
    dedupe_key TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE episode (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id UUID NOT NULL REFERENCES app_user(id),
    summary       TEXT NOT NULL,
    raw_ref       TEXT NOT NULL DEFAULT '',
    time_range    TSTZRANGE,
    embedding     VECTOR(1024),
    expires_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

CREATE TABLE audit_log (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_user_id UUID,
    actor         TEXT NOT NULL,
    action        TEXT NOT NULL,
    target        TEXT NOT NULL,
    detail_jsonb  JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE consent (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_user_id         UUID NOT NULL REFERENCES app_user(id),
    scope                   TEXT NOT NULL,
    granted_by_guardian_id  UUID,
    granted_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at              TIMESTAMPTZ
);

-- Chat persistence (required for the web demo and multi-turn clarify state).
CREATE TABLE chat_session (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id UUID NOT NULL REFERENCES app_user(id),
    state_jsonb   JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE chat_message (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES chat_session(id),
    role            TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
    content         TEXT NOT NULL DEFAULT '',
    tool_calls_jsonb JSONB,
    tool_call_id    TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
