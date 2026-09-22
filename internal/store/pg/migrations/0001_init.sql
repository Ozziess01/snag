CREATE TABLE projects (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT        NOT NULL,
    slug            TEXT        NOT NULL UNIQUE,
    -- Откуда браузерам можно слать события. Пусто — отовсюду.
    allowed_origins TEXT[]      NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Публичные ключи DSN. Ключей у проекта может быть несколько, чтобы
-- сменить ключ без простоя: выпустил новый, раскатал, старый отключил.
CREATE TABLE project_keys (
    public_key  TEXT PRIMARY KEY,
    project_id  BIGINT      NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    label       TEXT        NOT NULL DEFAULT 'default',
    active      BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX project_keys_project ON project_keys (project_id);

-- Проблема — группа одинаковых событий. Сами события лежат в ClickHouse,
-- здесь то, что меняется: статус, первое и последнее появление.
CREATE TABLE issues (
    id             BIGSERIAL PRIMARY KEY,
    project_id     BIGINT      NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    fingerprint    BIGINT      NOT NULL,
    grouping_kind  TEXT        NOT NULL,
    title          TEXT        NOT NULL,
    culprit        TEXT        NOT NULL DEFAULT '',
    level          TEXT        NOT NULL,
    platform       TEXT        NOT NULL,
    status         TEXT        NOT NULL DEFAULT 'unresolved'
                               CHECK (status IN ('unresolved', 'resolved', 'ignored')),
    first_seen     TIMESTAMPTZ NOT NULL,
    last_seen      TIMESTAMPTZ NOT NULL,
    times_seen     BIGINT      NOT NULL DEFAULT 0,
    resolved_at    TIMESTAMPTZ,
    regressed_at   TIMESTAMPTZ,
    UNIQUE (project_id, fingerprint)
);
CREATE INDEX issues_list ON issues (project_id, status, last_seen DESC);
