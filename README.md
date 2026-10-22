<div align="center">

# 🎧 song-vibes-check

**Микросервис на Go, который сравнивает две песни по жанровым тегам MusicBrainz и объясняет, почему они похожи (или нет).**

[![CI](https://github.com/mindcapp/song-vibes-check/actions/workflows/deploy.yml/badge.svg)](https://github.com/mindcapp/song-vibes-check/actions)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-cache-DC382D?style=flat-square&logo=redis&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-metrics-E6522C?style=flat-square&logo=prometheus&logoColor=white)
![Grafana](https://img.shields.io/badge/Grafana-dashboard-F46800?style=flat-square&logo=grafana&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-compose-2496ED?style=flat-square&logo=docker&logoColor=white)

[Как это работает](#-как-это-работает) •
[Быстрый старт](#-быстрый-старт) •
[API](#-api) •
[Конфигурация](#%EF%B8%8F-конфигурация) •
[Мониторинг](#-мониторинг) •
[Деплой](#-деплой) •
[Ограничения](#%EF%B8%8F-ограничения)

</div>

---

## ✨ Возможности

- **Два счёта схожести:** классический Jaccard по множествам тегов и взвешенный — по долям голосов MusicBrainz.
- **Объяснимый ответ:** общие теги, теги только одной из песен, источник тегов и флаг `low_data`, если данных мало.
- **Нормализация тегов:** регистр, синонимы (`hip-hop` = `hip hop` = `hiphop`), стоп-лист мусорных тегов (`seen live`, годы, национальности).
- **Стабильный результат:** теги собираются из всех тегированных записей, а не из случайной страницы выдачи; счёт детерминирован.
- **Redis-кэш с деградацией:** без Redis сервис продолжает работать, просто медленнее; одинаковые параллельные запросы склеиваются (singleflight).
- **Бережное отношение к MusicBrainz:** лимит 1 запрос/с, один повтор при `503` с учётом `Retry-After`, корректный `User-Agent`.
- **Наблюдаемость:** структурные JSON-логи (`log/slog`), метрики Prometheus, готовый дашборд Grafana.

## 🧠 Как это работает

```mermaid
flowchart LR
    C[Клиент] -->|POST /compare| API[song-vibes-check]
    API --> CACHE{Redis}
    CACHE -->|hit| API
    CACHE -->|miss| SF[singleflight]
    SF --> RL[rate limiter<br/>1 req/s]
    RL --> MB[(MusicBrainz API)]
    API --> N[нормализация тегов]
    N --> S[Jaccard + weighted]
    API -->|/metrics| P[Prometheus]
    P --> G[Grafana]
```

1. Для каждой песни ищется запись в MusicBrainz и собираются теги всех её тегированных версий.
2. Если пригодных тегов меньше трёх, добавляются теги исполнителя (lookup по MBID) с весом 0,5.
3. Теги нормализуются, мусор отбрасывается.
4. Считаются `genre_score` (Jaccard) и `weighted_score` (weighted Jaccard по долям голосов внутри трека).

## 🚀 Быстрый старт

**Требования:** Docker и Docker Compose.

```bash
git clone https://github.com/mindcapp/song-vibes-check.git
cd song-vibes-check

cp .env.example .env
echo "USER_SERVICE_SECRET_KEY=$(openssl rand -hex 32)" >> .env

docker compose up --build -d
```

| Сервис     | Адрес                          |
| ---------- | ------------------------------ |
| API        | http://127.0.0.1:8080          |
| Prometheus | http://127.0.0.1:9090          |
| Grafana    | http://127.0.0.1:3000          |

Все порты привязаны к `127.0.0.1` и наружу не публикуются.

> mood-service тяжёлый (PyTorch) и требует ключ Genius API, поэтому запускается только по запросу:
> `GENIUS_API_KEY=... docker compose --profile mood up --build`

> Поиск похожих треков нужен Qdrant, он тоже запускается по запросу — см. [Поиск похожих треков](#-поиск-похожих-треков).

**Без Docker:**

```bash
go run ./cmd/server
```

Без `REDIS_URL` сервис работает без кэша.

## 📡 API

| Метод | Путь       | Назначение                         |
| ----- | ---------- | ---------------------------------- |
| POST  | `/compare` | сравнение двух песен               |
| GET   | `/health`  | health check, `{"status":"ok"}`    |
| GET   | `/metrics` | метрики в формате Prometheus       |
| GET   | `/api/v1/similar` | похожие по звучанию треки (Qdrant) |
| GET   | `/api/v1/tracks/search` | поиск трека по названию или исполнителю |

### Пример запроса

```bash
curl -s -X POST localhost:8080/compare \
  -H 'Content-Type: application/json' \
  -d '{
    "song_a": {"artist": "Queen", "title": "Bohemian Rhapsody"},
    "song_b": {"artist": "Guns N Roses", "title": "Sweet Child O Mine"}
  }' | jq
```

### Пример ответа

```json
{
  "genre_score": 0.1698,
  "weighted_score": 0.2989,
  "shared_tags": ["rock", "hard rock", "classic rock"],
  "only_a": ["progressive rock", "art rock", "opera"],
  "only_b": ["heavy metal", "glam metal"],
  "tags_a": ["..."],
  "tags_b": ["..."],
  "tag_source_a": "recording",
  "tag_source_b": "recording",
  "low_data": false
}
```

<sub>Списки тегов сокращены для примера.</sub>

| Поле | Что значит |
| --- | --- |
| `genre_score` | Jaccard по нормализованным тегам, от 0 до 1 |
| `weighted_score` | взвешенная схожесть по долям голосов за теги |
| `shared_tags` / `only_a` / `only_b` | что совпало и что есть только у одной песни |
| `tags_a` / `tags_b` | сырые теги из MusicBrainz, без нормализации |
| `tag_source_a` / `tag_source_b` | `recording`, `mixed` (записи + исполнитель) или `artist` |
| `low_data` | `true`, если у одной из песен меньше трёх тегов — счёт ненадёжен |

### Коды ответов

| Код | Когда |
| --- | --- |
| `200` | всё хорошо |
| `400` | невалидный JSON или пустые поля |
| `404` | песня не найдена в MusicBrainz |
| `422` | песня найдена, но у неё нет тегов |
| `502` | MusicBrainz недоступен; приходит заголовок `Retry-After` |
| `500` | внутренняя ошибка (паника перехватывается и пишется в лог со стеком) |

## 🔎 Поиск похожих треков

Треки из [FMA](https://github.com/mdeff/fma) (подмножество `small`, 8 000 треков) лежат в
Qdrant: 64-мерный вектор аудиопризнаков librosa (после нормализации и PCA), косинусная
близость. Коллекцию готовит офлайн-пайплайн `pipeline/prepare_data.py` — описание
в `pipeline/README.md`. Эндпоинты включаются переменной `QDRANT_URL`; без неё они
отвечают `503`, остальной сервис работает как обычно.

### Запуск

```bash
# Qdrant из профиля vectors (порт только на 127.0.0.1) и API, знающий его адрес
QDRANT_URL=http://qdrant:6333 docker compose --profile vectors up --build -d

# один раз залить данные (с хоста, порт 6333 проброшен на loopback)
cd pipeline && .venv/bin/python prepare_data.py --qdrant-url http://127.0.0.1:6333
```

Без Docker: `QDRANT_URL=http://127.0.0.1:6333 go run ./cmd/server`.

Qdrant добавлен в корневой `docker-compose.yml` профилем `vectors`, а не вынесен
ссылкой на `pipeline/docker-compose.yml`: контейнеру `api` нужен Qdrant в той же
docker-сети (`http://qdrant:6333`), а отдельный compose создаёт свою сеть. Профиль
держит его выключенным по умолчанию, как `mood-service`. `pipeline/docker-compose.yml`
остаётся для работы с пайплайном без остального стека; одновременно оба не поднимать —
они займут один порт 6333, и данные у них в разных томах.

### `GET /api/v1/similar`

| Параметр | По умолчанию | Описание |
| --- | --- | --- |
| `track_id` | — (обязателен) | id трека FMA, положительное целое |
| `limit` | `5` | сколько соседей вернуть, от 1 до 50 |
| `exclude_same_artist` | `false` | `true` — пропустить треки того же исполнителя |

С `exclude_same_artist=true` у Qdrant берутся кандидаты с запасом (`max(6×limit, 30)`),
треки того же `artist` отбрасываются; если осталось меньше `limit`, поиск один раз
расширяется до 1000 кандидатов — так же, как метрика в `pipeline/evaluate.py`.

```bash
curl -s 'localhost:8080/api/v1/similar?track_id=38910&limit=3&exclude_same_artist=true' | jq
```

```json
{
  "track": {"track_id": 38910, "title": "greyish", "artist": "File Under Toner",
            "genre": "Experimental", "x": 11.6233, "y": -3.5402},
  "neighbors": [
    {"track_id": 9846, "title": "Computer Music/Post-Fordism", "artist": "Mattin",
     "genre": "Experimental", "x": 11.5895, "y": -3.447, "score": 0.91519415},
    {"track_id": 126516, "title": "Ancient Greece Noise", "artist": "Seiei Jack",
     "genre": "Experimental", "x": 11.6035, "y": -3.5729, "score": 0.86939424},
    {"track_id": 127350, "title": "恐怖", "artist": "Lately Kind of Yeah",
     "genre": "Electronic", "x": 11.7392, "y": -3.6218, "score": 0.865085}
  ]
}
```

Без `exclude_same_artist` первые три места заняли бы другие треки File Under Toner.
`x`, `y` — 2D-проекция UMAP для карты на фронте.

`score` — косинусная близость (1 — одинаковое направление векторов). `genre` — жанр
FMA (`genre_top`), в поиске он не участвует.

### `GET /api/v1/tracks/search`

Для выпадающего списка на фронте: треки, у которых название или исполнитель содержит
`q` (без учёта регистра). Сначала совпадения в начале названия, потом в начале имени
исполнителя, потом в середине.

| Параметр | По умолчанию | Описание |
| --- | --- | --- |
| `q` | — (обязателен) | от 2 до 100 символов |
| `limit` | `10` | от 1 до 50 |

```bash
curl -s 'localhost:8080/api/v1/tracks/search?q=awol&limit=2' | jq
# {"tracks": [{"track_id": 2, "title": "Food", "artist": "AWOL", "genre": "Hip-Hop", "x": 7.6261, "y": -3.3019},
#             {"track_id": 80293, "title": "HipHop Junkfood", "artist": "AWOL", "genre": "Hip-Hop", "x": 7.9355, "y": -1.4202}]}
```

Поиск идёт по копии payload в памяти API: при первом запросе все точки читаются из
Qdrant (`scroll`) и перечитываются раз в 10 минут. Текстовый фильтр Qdrant без
полнотекстового индекса чувствителен к регистру, а 8 000 названий дешевле держать в памяти,
чем менять коллекцию.

### Коды ответов

| Код | Когда |
| --- | --- |
| `200` | всё хорошо (пустой результат — пустой массив) |
| `400` | нет `track_id`, он не положительное целое, `limit` вне диапазона, `exclude_same_artist` не булево, `q` короче 2 символов |
| `404` | трека с таким `track_id` нет в коллекции |
| `502` | Qdrant недоступен или не ответил вовремя; приходит `Retry-After` |
| `503` | поиск не настроен: не задан `QDRANT_URL` |

## ⚙️ Конфигурация

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `MB_USER_AGENT` | `song-vibes-check/0.2.0 ( https://github.com/mindcapp/song-vibes-check )` | User-Agent для MusicBrainz, по правилам API нужен контакт |
| `REDIS_URL` | — | адрес Redis; без него кэш выключен |
| `CACHE_TTL` | `24h` | время жизни трека в кэше |
| `CACHE_LOW_DATA_TTL` | `1h` | для треков с малым числом тегов |
| `CACHE_NEGATIVE_TTL` | `10m` | для «не найдено» и «нет тегов» |
| `CACHE_ARTIST_TTL` | `72h` | для тегов исполнителя |
| `REQUEST_TIMEOUT` | `30s` | общий таймаут обработки запроса |
| `TAG_STOPLIST_FILE` | встроенный | свой стоп-лист тегов |
| `QDRANT_URL` | — | адрес Qdrant (`http://host:6333`); без него поиск похожих выключен |
| `QDRANT_COLLECTION` | `tracks` | имя коллекции |
| `QDRANT_API_KEY` | — | ключ, если в Qdrant включена аутентификация |

Ошибки MusicBrainz в кэш не попадают.

## 📈 Мониторинг

Дашборд Grafana подключается автоматически через provisioning.

| Метрика | Описание |
| --- | --- |
| `http_requests_total{method,path,status}` | запросы к сервису |
| `http_request_duration_seconds{method,path}` | время обработки |
| `musicbrainz_request_duration_seconds` | время запросов к MusicBrainz |
| `musicbrainz_errors_total{reason}` | ошибки MusicBrainz по причинам |
| `cache_hits_total{cache}` | попадания в кэш (`track` / `artist`) |
| `cache_misses_total{cache}` | промахи, только лидер singleflight |
| `cache_coalesced_total{cache}` | запросы, склеенные с уже идущим |
| `qdrant_request_duration_seconds{operation}` | время запросов к Qdrant (`retrieve` / `query` / `scroll`) |
| `qdrant_errors_total{operation,reason}` | ошибки Qdrant по причинам (`network`, `timeout`, `status_5xx`, …) |

Неизвестные пути сводятся к `other`, чтобы сканеры не раздували число рядов.

## 🧪 Тесты

```bash
go test -race ./...
```

Тесты не ходят в живой MusicBrainz: клиент проверяется на `httptest`-сервере и записанной фикстуре ответа. Для ручной проверки на живых данных есть `scripts/smoke.sh`.

## 📦 Деплой

Прод — бинарь под systemd на VPS, за nginx с TLS. GitHub Actions:

1. на каждый pull request — `gofmt`, `go vet`, `go test -race`;
2. на push в `main` — сборка, копирование бинаря на VPS, перезапуск сервиса и health-check. Если health-check не прошёл, джоба падает.

Пошаговая первичная настройка сервера (пользователь, systemd-юнит, sudoers, nginx и certbot) — в [`deploy/`](deploy/).

## ⚠️ Ограничения

- **Данные MusicBrainz краудсорсные.** Теги и голоса меняются, у малоизвестных треков их может не быть совсем.
- **Холодный запрос занимает несколько секунд** из-за лимита 1 запрос/с и возможного повтора при `503`. Повторный — миллисекунды из кэша.
- **Стабильность опирается на фильтр `tag:*`** в поиске MusicBrainz — это поведение поисковика, а не гарантия API.
- **Составные теги не пересекаются с простыми:** `british rock` и `rock` считаются разными.

## 🗺 Roadmap

- [x] Нормализация тегов и взвешенная схожесть
- [x] Redis-кэш, singleflight, деградация без Redis
- [x] Метрики MusicBrainz и дашборд Grafana
- [ ] Проверка веток `mixed` и `artist` на живых данных
- [ ] Redis на VPS
- [ ] Связка с mood-service для сравнения по настроению текста

## 🛠 Стек

**Go 1.26** · `net/http` · `log/slog` · **Redis** (`go-redis/v9`) · **Prometheus** · **Grafana** · **Docker Compose** · **GitHub Actions** · systemd · nginx

---

<div align="center">
<sub>Сделано <a href="https://github.com/mindcapp">mindcapp</a> · данные: <a href="https://musicbrainz.org">MusicBrainz</a> (CC0)</sub>
</div>
