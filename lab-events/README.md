# vibe-lab: Redpanda + ClickHouse + Go-генератор

Отдельная лаборатория. С основным репозиторием и VPS никак не связана.

```
Go producer ──JSON──▶ Redpanda (топик vibe.events.stream, 3 партиции)
                          │
                          ▼
              ClickHouse: events_queue (Kafka engine)
                          │ materialized views
          ┌───────────────┼────────────────────┐
          ▼               ▼                    ▼
     events          transitions        events_per_minute
  (сырые, MergeTree) (SummingMergeTree)  (SummingMergeTree)
```

## Что понадобится
- Docker с Compose v2 и ~3 ГБ свободной RAM под контейнеры (лимиты: Redpanda 900 МБ, ClickHouse 1.5 ГБ)
- Go 1.22+

## Запуск

```bash
docker compose up -d
docker compose ps          # redpanda-init должен быть Exited (0), остальные Up
```

Генератор (первый раз нужно подтянуть зависимости):

```bash
cd producer
go mod tidy
go run . -rate 500 -duration 60s
```

Флаги: `-rate` событий/с, `-duration` (0 = до Ctrl+C), `-users` число сессий, `-tracks`, `-clusters`, `-seed`, `-brokers`, `-topic`.

## Проверка

```bash
docker compose exec clickhouse clickhouse-client -u vibe --password vibe -d vibe
```

Сколько событий дошло:
```sql
SELECT event_type, count() FROM events GROUP BY event_type;
```

Скорость по минутам:
```sql
SELECT minute, event_type, sum(events) AS events
FROM events_per_minute GROUP BY minute, event_type ORDER BY minute, event_type;
```

Лучшие переходы (из какого трека в какой слушатель идёт и не скипает):
```sql
SELECT from_track, to_track,
       sum(plays) AS plays,
       sum(skips) AS skips,
       round(1 - sum(skips) / sum(plays), 3) AS keep_rate
FROM transitions
GROUP BY from_track, to_track
HAVING plays >= 20
ORDER BY keep_rate DESC, plays DESC
LIMIT 20;
```
Если логика генератора работает, у топовых пар `from_track` и `to_track` должны быть из одного кластера (номера треков совпадают по модулю 12).

Состояние консьюмера в Kafka:
```bash
docker compose exec redpanda rpk group describe clickhouse-vibe
```

## Что стоит попробовать руками
1. Остановить ClickHouse (`docker compose stop clickhouse`), погонять генератор, запустить обратно. События должны догнаться из топика.
2. Выставить `-rate 5000` и посмотреть, где упрётся (CPU, лимит памяти, лаг группы).
3. Сравнить `SELECT count() FROM transitions` и `SELECT count() FROM transitions FINAL`: SummingMergeTree схлопывает строки в фоне, поэтому читать нужно через `sum()` + `GROUP BY`.
4. Добавить `kafka_num_consumers = 3` (по числу партиций) и сравнить пропускную способность.

## Ограничения
- Одна нода Redpanda, replication factor 1. Это учебный стенд, не про отказоустойчивость.
- Схему при изменении придётся пересоздавать: `docker compose down -v && docker compose up -d` (скрипты в `clickhouse-init` выполняются только на пустом томе).
- SQL и Go-код писались без запуска Docker и без загрузки зависимостей, при первом старте возможны мелкие правки.
