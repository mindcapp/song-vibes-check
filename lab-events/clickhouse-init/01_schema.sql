-- Слой 1. Kafka engine: сама ничего не хранит, только читает топик.
CREATE TABLE IF NOT EXISTS vibe.events_queue
(
    event_id      String,
    ts_ms         UInt64,
    user_id       String,
    session_id    String,
    track_id      String,
    prev_track_id String,
    event_type    String,   -- complete | skip | like
    listen_ms     UInt32
)
ENGINE = Kafka
SETTINGS
    kafka_broker_list = 'redpanda:29092',
    kafka_topic_list = 'vibe.events.stream',
    kafka_group_name = 'clickhouse-vibe',
    kafka_format = 'JSONEachRow',
    kafka_num_consumers = 1,
    kafka_skip_broken_messages = 10;

-- Слой 2. Сырые события в MergeTree.
CREATE TABLE IF NOT EXISTS vibe.events
(
    event_id      String,
    ts            DateTime64(3, 'UTC'),
    user_id       LowCardinality(String),
    session_id    String,
    track_id      LowCardinality(String),
    prev_track_id LowCardinality(String),
    event_type    LowCardinality(String),
    listen_ms     UInt32
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (user_id, ts);

CREATE MATERIALIZED VIEW IF NOT EXISTS vibe.events_mv TO vibe.events AS
SELECT
    event_id,
    fromUnixTimestamp64Milli(ts_ms, 'UTC') AS ts,
    user_id,
    session_id,
    track_id,
    prev_track_id,
    event_type,
    listen_ms
FROM vibe.events_queue;

-- Слой 3. Матрица переходов track -> track.
-- SummingMergeTree схлопывает строки с одинаковым ключом, суммируя числовые колонки.
-- Читать всегда через sum()/GROUP BY: слияния идут в фоне и не мгновенно.
-- like — отдельное событие поверх прослушивания (плеер шлёт complete/skip,
-- а like дополнительно), поэтому оно не проигрывание и не время прослушивания.
CREATE TABLE IF NOT EXISTS vibe.transitions
(
    from_track LowCardinality(String),
    to_track   LowCardinality(String),
    plays      UInt64,   -- complete + skip
    skips      UInt64,
    likes      UInt64,
    listen_ms  UInt64    -- только по complete и skip
)
ENGINE = SummingMergeTree
ORDER BY (from_track, to_track);

CREATE MATERIALIZED VIEW IF NOT EXISTS vibe.transitions_mv TO vibe.transitions AS
SELECT
    prev_track_id                      AS from_track,
    track_id                           AS to_track,
    toUInt64(event_type IN ('complete', 'skip'))                    AS plays,
    toUInt64(event_type = 'skip')                                   AS skips,
    toUInt64(event_type = 'like')                                   AS likes,
    if(event_type IN ('complete', 'skip'), toUInt64(listen_ms), 0)  AS listen_ms
FROM vibe.events_queue
WHERE prev_track_id != '';

-- Слой 4. Телеметрия по минутам.
CREATE TABLE IF NOT EXISTS vibe.events_per_minute
(
    minute     DateTime('UTC'),
    event_type LowCardinality(String),
    events     UInt64,
    listen_ms  UInt64
)
ENGINE = SummingMergeTree
ORDER BY (minute, event_type);

CREATE MATERIALIZED VIEW IF NOT EXISTS vibe.events_per_minute_mv TO vibe.events_per_minute AS
SELECT
    toStartOfMinute(fromUnixTimestamp64Milli(ts_ms, 'UTC')) AS minute,
    event_type,
    count()                                                  AS events,
    sum(toUInt64(listen_ms))                                 AS listen_ms
FROM vibe.events_queue
GROUP BY minute, event_type;
