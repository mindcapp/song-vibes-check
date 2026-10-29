# vibe-lab: Redpanda + ClickHouse + Go-генератор

Отдельная лаборатория: поток событий прослушивания в Redpanda и аналитика в ClickHouse.
Запускается своим compose, в корневой `docker-compose.yml` не входит. Go API на хосте может
писать в неё события через `POST /api/v1/events` (см. корневой README, раздел
«События и аналитика»).

> **Данные синтетические.** Треки настоящие (8 000 треков FMA small из
> `pipeline/output/points.json`), но слушатели, сессии, скипы и лайки выдуманы генератором.
> Матрица переходов отражает заложенную в генератор модель, а не поведение людей.

```
Go producer / API ──JSON──▶ Redpanda (топик vibe.events.stream, 3 партиции)
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
- Go 1.26+ (этого требует franz-go v1.22)
- `pipeline/output/points.json` — его пишет `pipeline/prepare_data.py` (см. `pipeline/README.md`)

## Запуск

```bash
cd lab-events
docker compose up -d
docker compose ps          # redpanda-init должен быть Exited (0), остальные Up
```

Все порты опубликованы только на `127.0.0.1`: Redpanda `9092` (без аутентификации),
ClickHouse `8123` (HTTP) и `9000` (native). Пароль ClickHouse по умолчанию `vibe`,
переопределяется переменной `CLICKHOUSE_PASSWORD` при первом `up` (пустой том).

Генератор:

```bash
cd producer
go run . -rate 1000 -duration 60s
```

За минуту это ~62 тыс. событий и ~70 пар треков с 20+ проигрываниями — хватает для
запросов ниже. Флаги: `-rate` прослушиваний/с, `-duration` (0 = до Ctrl+C), `-users`
число одновременных сессий, `-points` путь к `points.json`
(по умолчанию `../../pipeline/output/points.json`), `-zipf` показатель популярности,
`-seed`, `-brokers`, `-topic`.

### Модель генератора

- Сессия — 15–44 трека. Следующий трек в 70% случаев из жанра предыдущего (`genre`
  из `points.json` играет роль «кластера»), иначе любой. Тот же трек подряд не повторяется.
- Скип: 15% внутри жанра, 60% между жанрами. Иначе `complete`, и после него в 6% случаев
  отдельным событием `like` (так же его пришлёт плеер через API).
- Популярность треков — распределение Ципфа (`s = 1.2`, сдвиг `v = 4`): самый популярный
  трек набирает ~2.7% прослушиваний. Сдвиг подобран симуляцией: при `v = 1` первый трек
  жанра забирал ~25% выборов и матрицу заполняли его повторы, при `v = 10` за минуту
  ни одна пара не набирала 20 проигрываний.
- Ключ записи — `session_id`: события сессии попадают в одну партицию по порядку.

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

Лучшие переходы (из какого трека в какой слушатель идёт и не скипает). Алиасы нарочно
не совпадают с именами колонок: `sum(plays) AS plays` ClickHouse подставил бы внутрь
`sum(...)` и упал с `ILLEGAL_AGGREGATION`:
```sql
SELECT from_track, to_track,
       sum(plays) AS total_plays,
       sum(skips) AS total_skips,
       round(1 - total_skips / total_plays, 3) AS keep_rate
FROM transitions
GROUP BY from_track, to_track
HAVING total_plays >= 20
ORDER BY keep_rate DESC, total_plays DESC
LIMIT 20;
```
Если логика генератора работает, у топовых пар `from_track` и `to_track` совпадает жанр.
Проверка в среднем (после прогона выше было 0.851 внутри жанра и 0.399 между жанрами —
ровно заложенные 1 − 0.15 и 1 − 0.6); жанры подгружаются из `points.json` во временную таблицу:

```bash
python3 -c "import json; print('INSERT INTO vibe.genres VALUES ' + ','.join(\"('%s','%s')\" % (p['track_id'], p['genre']) for p in json.load(open('../pipeline/output/points.json'))))" > /tmp/genres.sql
docker compose exec -T clickhouse clickhouse-client -u vibe --password vibe -d vibe \
  -q "CREATE TABLE genres (track_id String, genre String) ENGINE = Memory"
docker compose exec -T clickhouse clickhouse-client -u vibe --password vibe -d vibe < /tmp/genres.sql
```
```sql
SELECT gf.genre = gt.genre AS same_genre, sum(plays) AS total_plays,
       round(1 - sum(skips) / sum(plays), 3) AS keep_rate
FROM transitions t
JOIN genres gf ON gf.track_id = t.from_track
JOIN genres gt ON gt.track_id = t.to_track
GROUP BY same_genre;
```

Состояние консьюмера в Kafka:
```bash
docker compose exec redpanda rpk group describe clickhouse-vibe
```

## Что стоит попробовать руками
1. Остановить ClickHouse (`docker compose stop clickhouse`), погонять генератор, запустить обратно. События должны догнаться из топика.
2. Выставить `-rate 5000` и посмотреть, где упрётся (CPU, лимит памяти, лаг группы).
3. Сравнить `SELECT count() FROM transitions` и `SELECT count() FROM transitions FINAL`: SummingMergeTree схлопывает строки в фоне, поэтому читать нужно через `sum()` + `GROUP BY`.
4. Добавить `kafka_num_consumers = 3` (по числу партиций) и сравнить пропускную способность.

## Очистка

Все данные лежат в томах `redpanda_data` и `clickhouse_data`. Удалить контейнеры вместе
с ними (топик, события, таблицы; схема пересоздастся при следующем `up`):

```bash
docker compose down -v
```

## Что было исправлено после первого запуска

Лаборатория писалась без запуска; при проверке на Docker нашлось:

1. У `producer/go.mod` не было `require` и `go.sum` — `go run .` не собирался.
2. Запрос лучших переходов падал с `ILLEGAL_AGGREGATION` (алиас `plays` совпадал с колонкой).
3. `like` не считался проигрыванием, но его `listen_ms` суммировался в `transitions`;
   теперь у лайков своя колонка `likes`, а `plays`/`listen_ms` — только `complete` и `skip`.
4. Порты Redpanda и ClickHouse были открыты на `0.0.0.0`; теперь только `127.0.0.1`.
5. `redpanda-init` с `|| true` завершался успешно даже без брокера; теперь падает,
   если топика нет.
6. При 2 000 (а тем более 8 000) равновероятных треков пары почти не повторялись и
   `HAVING plays >= 20` всегда был пуст — добавлена популярность по Ципфу.

Healthcheck Redpanda, Kafka engine и materialized views заработали без правок; предупреждения
ClickHouse `Listen [::]:... failed` безвредны — в Docker без IPv6 сервер слушает только IPv4.

## Ограничения
- Одна нода Redpanda, replication factor 1. Это учебный стенд, не про отказоустойчивость.
- Схему при изменении придётся пересоздавать: `docker compose down -v && docker compose up -d` (скрипты в `clickhouse-init` выполняются только на пустом томе).
- Данные генератора синтетические (см. начало файла).
