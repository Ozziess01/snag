-- События. Порядок сортировки (проект, проблема, время) делает быстрыми
-- главные запросы: «события этой проблемы» и «проблемы проекта за период».
CREATE TABLE IF NOT EXISTS events
(
    project_id  UInt64,
    issue_id    UInt64,
    event_id    UUID,
    timestamp   DateTime64(3, 'UTC'),
    received_at DateTime64(3, 'UTC'),
    level       LowCardinality(String),
    platform    LowCardinality(String),
    environment LowCardinality(String),
    release     LowCardinality(String),
    sdk         LowCardinality(String),
    title       String,
    culprit     String,
    -- Кто пострадал: user.id, email, логин или IP. Для «сколько пользователей».
    user_key    String,
    tags        Map(LowCardinality(String), String),
    -- Исходный JSON события целиком: стек, breadcrumbs, контексты.
    data        String CODEC(ZSTD(3))
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(timestamp)
ORDER BY (project_id, issue_id, timestamp)
TTL toDateTime(timestamp) + INTERVAL 90 DAY;

-- Счётчики по часам для списка проблем и графиков. Заполняются сами при
-- вставке в events, поэтому список не сканирует сырые события.
CREATE TABLE IF NOT EXISTS issue_hourly
(
    project_id UInt64,
    issue_id   UInt64,
    hour       DateTime('UTC'),
    events     SimpleAggregateFunction(sum, UInt64),
    users      AggregateFunction(uniq, String)
)
ENGINE = AggregatingMergeTree
ORDER BY (project_id, issue_id, hour)
TTL hour + INTERVAL 90 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS issue_hourly_mv TO issue_hourly AS
SELECT
    project_id,
    issue_id,
    toStartOfHour(timestamp) AS hour,
    count() AS events,
    -- Пустой user_key не считаем пользователем.
    uniqStateIf(user_key, user_key != '') AS users
FROM events
GROUP BY project_id, issue_id, hour;
