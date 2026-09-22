CREATE TABLE notification_channels (
    id                   BIGSERIAL PRIMARY KEY,
    project_id           BIGINT      NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind                 TEXT        NOT NULL CHECK (kind IN ('telegram')),
    target               TEXT        NOT NULL,
    on_new               BOOLEAN     NOT NULL DEFAULT true,
    on_regression        BOOLEAN     NOT NULL DEFAULT true,
    spike_threshold      INT         NOT NULL DEFAULT 0 CHECK (spike_threshold >= 0),
    spike_window_minutes INT         NOT NULL DEFAULT 5 CHECK (spike_window_minutes BETWEEN 1 AND 60),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, kind, target)
);
