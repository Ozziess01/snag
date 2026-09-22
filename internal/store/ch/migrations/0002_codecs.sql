-- Плотнее сжатие самых объёмных колонок (замер: теги занимали 70 %
-- места). Время почти монотонно внутри куска — DoubleDelta сводит его
-- к нескольким битам на строку. Новые настройки применяются к новым
-- кускам и к старым при слиянии.
ALTER TABLE events MODIFY COLUMN tags Map(LowCardinality(String), String) CODEC(ZSTD(3));
ALTER TABLE events MODIFY COLUMN timestamp DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1));
ALTER TABLE events MODIFY COLUMN received_at DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1));
ALTER TABLE events MODIFY COLUMN title String CODEC(ZSTD(3));
ALTER TABLE events MODIFY COLUMN culprit String CODEC(ZSTD(3));
ALTER TABLE events MODIFY COLUMN user_key String CODEC(ZSTD(3));
