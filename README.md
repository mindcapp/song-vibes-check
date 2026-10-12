# song-vibes-check

Сервис сравнивает две песни по жанровым тегам из [MusicBrainz](https://musicbrainz.org) и отвечает, насколько они похожи и почему.

В репозитории три независимых сервиса:

| Сервис | Каталог | Язык | Порт | Что делает |
|---|---|---|---|---|
| **api** (song-vibes-check) | `cmd/`, `internal/` | Go 1.26 | 8080 | `POST /compare`: схожесть двух песен по тегам |
| **mood-service** | `mood-service/` | Python 3.11, FastAPI | 8001 | `POST /analyze`: настроение песни по тексту |
| **user-service** | `user-service/` | Go 1.26 | 8002 | регистрация, вход по JWT, история сравнений |

Сервисы друг друга не вызывают: каждый отвечает на свои запросы, связывает их клиент.

## Быстрый старт

Нужны Docker и Docker Compose.

```bash
cp .env.example .env
# впишите в .env ключ для user-service:
openssl rand -hex 32          # → USER_SERVICE_SECRET_KEY=<результат>

docker compose up --build
```

Поднимутся `api`, `redis`, `user-service` с PostgreSQL, Prometheus и Grafana. Без `USER_SERVICE_SECRET_KEY` контейнер `user-service` завершится с ошибкой в логе — остальные сервисы при этом работают.

```bash
curl -s -X POST localhost:8080/compare \
  -H 'Content-Type: application/json' \
  -d '{
    "song_a": {"artist": "Queen", "title": "Bohemian Rhapsody"},
    "song_b": {"artist": "Guns N Roses", "title": "Sweet Child O Mine"}
  }' | jq
```

Первый запрос занимает несколько секунд (см. [Ограничения](#ограничения)), повторный такой же отвечает за миллисекунды из кэша.

| Адрес | Что там |
|---|---|
| http://localhost:8080 | api |
| http://localhost:8002 | user-service |
| http://localhost:8001 | mood-service (только с профилем `mood`) |
| http://localhost:9090 | Prometheus |
| http://localhost:3000 | Grafana |

Все порты привязаны к `127.0.0.1` и доступны только с этой машины. Чтобы открыть сервис наружу, поставьте перед ним reverse proxy (пример для api — [deploy/nginx.conf](deploy/nginx.conf)).

- **Grafana**: логин `admin`. Пароль берётся из `GF_ADMIN_PASSWORD`; если переменная не задана, используется `admin` — только для локальной разработки. Регистрация пользователей отключена. Datasource и дашборд `song-vibes-check` подключаются автоматически.
- **Redis и PostgreSQL** наружу не публикуются, к ним ходят только контейнеры.
- **mood-service** в обычный запуск не входит: образ тянет PyTorch и нужен ключ Genius. Запуск: `GENIUS_API_KEY=... docker compose --profile mood up --build`.

Остановить: `docker compose down`. Вместе с данными (БД, метрики, настройки Grafana): `docker compose down -v`.

Ручная проверка на нескольких парах песен, включая малоизвестного исполнителя:

```bash
scripts/smoke.sh                 # или scripts/smoke.sh http://host:8080
```

Скрипт ходит в живой MusicBrainz, поэтому в CI не входит.

## API: `POST /compare`

| Метод | Путь | Назначение |
|---|---|---|
| POST | `/compare` | сравнение двух песен |
| GET | `/health` | проверка живости, `{"status":"ok"}` |
| GET | `/metrics` | метрики Prometheus |

### Запрос

```json
{
  "song_a": {"artist": "Queen", "title": "Bohemian Rhapsody"},
  "song_b": {"artist": "Guns N Roses", "title": "Sweet Child O Mine"}
}
```

Все четыре поля обязательны. Имя исполнителя сверяется без учёта регистра, пунктуации, диакритики и ведущего «the»: `guns n roses`, `Guns N' Roses` и `Guns N’ Roses` — один исполнитель.

### Ответ

Реальный ответ на запрос выше (длинные списки сокращены):

```json
{
  "genre_score": 0.16981132075471697,
  "weighted_score": 0.29887471060141074,
  "tags_a": ["rock", "hard rock", "album rock", "arena rock", "art rock", "classic rock", "..."],
  "tags_b": ["hard rock", "rock", "classic rock", "album rock", "pop rock", "pop/rock", "..."],
  "shared_tags": ["rock", "hard rock", "classic rock", "album rock", "pop rock", "pop/rock", "heavy metal", "pop", "glam"],
  "only_a": ["arena rock", "art rock", "progressive rock", "british metal", "..."],
  "only_b": ["blues", "glam metal", "hardnheavy", "metal", "..."],
  "tag_source_a": "recording",
  "tag_source_b": "recording",
  "low_data": false
}
```

| Поле | Смысл |
|---|---|
| `genre_score` | Jaccard по множествам тегов: доля общих тегов среди всех, от 0 до 1 |
| `weighted_score` | взвешенный Jaccard: учитывает, сколько голосов у каждого тега, от 0 до 1 |
| `shared_tags` | теги, которые есть у обеих песен, от самых весомых |
| `only_a`, `only_b` | теги только одной из песен, от самых весомых |
| `tags_a`, `tags_b` | теги в том виде, в каком их отдаёт MusicBrainz, до нормализации |
| `tag_source_a`, `tag_source_b` | откуда взяты теги песни: `recording`, `mixed` или `artist` |
| `low_data` | `true`, если у любой из песен после нормализации меньше трёх тегов — оценкам тогда доверять не стоит |

Оценки и списки `shared_tags`, `only_a`, `only_b` считаются по нормализованным тегам, поэтому написание в них может отличаться от `tags_a` и `tags_b`.

### Ошибки

Тело ошибки всегда `{"error": "..."}`.

| Код | Когда | Что делать |
|---|---|---|
| 400 | невалидный JSON, пустое поле, тело больше 64 КБ | исправить запрос |
| 404 | песня не найдена в MusicBrainz | проверить написание исполнителя и названия |
| 422 | песня найдена, но пригодных тегов нет ни у неё, ни у исполнителя | сравнить не получится |
| 502 | MusicBrainz недоступен, ответил ошибкой или не уложился в `REQUEST_TIMEOUT` | повторить позже |

У ответа 502 есть заголовок `Retry-After` в секундах: значение, которое назвал MusicBrainz (не больше 60), иначе 1. Подробности сбоя апстрима клиенту не передаются, они остаются в логе.

## Как считается схожесть

1. **Поиск записей.** У песни в MusicBrainz обычно много записей (студийная, концертные, ремастеры), и теги проставлены у немногих. Сервис ищет только записи с тегами, оставляет те, где исполнитель совпадает с запрошенным, а оценка поиска не ниже 90, и берёт до пяти записей с наибольшим числом голосов. Голоса их тегов суммируются.
2. **Теги исполнителя.** Если пригодных тегов у записей меньше трёх, по идентификатору исполнителя (MBID) запрашиваются его теги. Они дополняют теги записей с весом 0,5 (`mixed`) или заменяют их, если у записей тегов нет совсем (`artist`). Если тегов записей хватает, исполнитель не запрашивается (`recording`).
3. **Нормализация** (`internal/tags`):
   - регистр и пробелы приводятся к одному виду, дефисы и подчёркивания становятся пробелами;
   - варианты написания сводятся к одному тегу по таблице синонимов (`hip hop`, `hip-hop`, `hiphop`);
   - теги не про жанр отбрасываются по стоп-листу: `seen live`, страны и национальности, служебные пометки;
   - годы и десятилетия (`1975`, `80s`) отбрасываются всегда.
4. **Оценки.** `genre_score` — обычный Jaccard по множествам тегов. `weighted_score` — взвешенный Jaccard, Σmin / Σmax, по долям голосов внутри песни. Доли, а не сырые счётчики, взяты для того, чтобы хит с сотней голосов был сравним с малоизвестной песней.

Стоп-лист по умолчанию лежит в [internal/tags/stoplist.txt](internal/tags/stoplist.txt), синонимы — в [internal/tags/synonyms.txt](internal/tags/synonyms.txt). Свой стоп-лист подключается переменной `TAG_STOPLIST_FILE` и полностью заменяет встроенный.

## Кэш

Ответы MusicBrainz кэшируются в Redis, чтобы повторные запросы не тратили лимит апстрима.

| Что | Ключ | Время жизни |
|---|---|---|
| песня с тремя и более тегами | `track:v3:<исполнитель>\|<название>` | `CACHE_TTL` |
| песня, у которой тегов меньше трёх | тот же | `CACHE_LOW_DATA_TTL` |
| «песня не найдена» | тот же | `CACHE_NEGATIVE_TTL` |
| теги исполнителя | `artist:v1:<mbid>` | `CACHE_ARTIST_TTL` |

- Исполнитель и название в ключе приводятся к нижнему регистру, лишние пробелы убираются.
- Ошибки MusicBrainz не кэшируются.
- Несколько одновременных запросов одной и той же песни дают один запрос в MusicBrainz.
- **Сбой Redis не роняет запросы.** Операция с кэшем ограничена 150 мс; после ошибки кэш обходится 30 секунд, в логе появляется одно предупреждение, сервис продолжает отвечать напрямую из MusicBrainz.
- Без `REDIS_URL` сервис работает без кэша.

В строке access-лога для `/compare` поля `cache_a` и `cache_b` показывают `hit` или `miss` по каждой песне.

## Переменные окружения

### api

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `MB_USER_AGENT` | `song-vibes-check/0.2.0 ( https://github.com/mindcapp/song-vibes-check )` | User-Agent для MusicBrainz. Их правила требуют название, версию и контакт (URL или e-mail); запросы без внятного User-Agent ограничиваются |
| `REDIS_URL` | не задана | адрес Redis, например `redis://redis:6379/0`. Без неё кэш выключен |
| `CACHE_TTL` | `24h` | время жизни песни с тегами |
| `CACHE_LOW_DATA_TTL` | `1h` | время жизни песни или исполнителя, у которых меньше трёх тегов |
| `CACHE_NEGATIVE_TTL` | `10m` | время жизни ответа «не найдено» |
| `CACHE_ARTIST_TTL` | `72h` | время жизни тегов исполнителя |
| `REQUEST_TIMEOUT` | `20s` | сколько `/compare` может искать обе песни, прежде чем ответит 502 |
| `TAG_STOPLIST_FILE` | не задана | путь к своему стоп-листу тегов; заменяет встроенный |

Длительности задаются в формате Go: `90s`, `15m`, `24h`. Неверное значение игнорируется с предупреждением в логе, берётся значение по умолчанию.

### docker compose

Читаются из окружения или из файла `.env` в корне (шаблон — [.env.example](.env.example)).

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `USER_SERVICE_SECRET_KEY` | нет, обязательна | ключ подписи JWT в user-service, не короче 32 символов: `openssl rand -hex 32` |
| `POSTGRES_PASSWORD` | `user_service` | пароль БД user-service. Значение по умолчанию — только для локальной разработки |
| `GF_ADMIN_PASSWORD` | `admin` | пароль администратора Grafana. Значение по умолчанию — только для локальной разработки |
| `GENIUS_API_KEY` | не задана | ключ Genius для mood-service (профиль `mood`) |
| `PROMETHEUS_CONFIG` | `./prometheus.yml` | файл конфигурации Prometheus |

Пароли PostgreSQL и Grafana применяются при первом создании тома. Чтобы сменить их позже, пересоздайте том или смените пароль средствами самой БД или Grafana.

## Мониторинг

`/metrics` отдаёт:

| Метрика | Метки | Смысл |
|---|---|---|
| `http_requests_total` | `method`, `path`, `status` | запросы к сервису |
| `http_request_duration_seconds` | `method`, `path` | время ответа |
| `musicbrainz_request_duration_seconds` | `endpoint` | время одного запроса в MusicBrainz, без ожидания лимита |
| `musicbrainz_errors_total` | `endpoint`, `reason` | неудачные запросы в MusicBrainz (`status_503`, `network`, `timeout`, ...) |
| `musicbrainz_retries_total` | `endpoint` | повторы после 503 или сетевой ошибки |
| `cache_hits_total`, `cache_misses_total` | `cache` | попадания и промахи; `cache` — `track` или `artist` |
| `cache_coalesced_total` | `cache` | запросы, присоединившиеся к уже идущему запросу той же песни |
| `cache_errors_total` | `cache` | ошибки Redis |

У метрик кэша есть метка `cache`, поэтому общие числа получаются суммой: `sum(cache_hits_total)`. Метка `path` принимает только `/compare`, `/health`, `/metrics` и `other`.

Дашборд Grafana `song-vibes-check` ([JSON](grafana/provisioning/dashboards/song-vibes-check.json)) показывает состояние цели `api:8080`, RPS, p95 времени ответа, долю ошибок, долю попаданий в кэш, время ответа и ошибки MusicBrainz. Панели с долями и перцентилями пусты, пока для них нет данных: например, доля ошибок 5xx не рисуется, пока не было ни одной такой ошибки.

Prometheus в compose опрашивает `api:8080` ([prometheus.yml](prometheus.yml)). Чтобы смотреть на сервис вне compose, например на VPS, скопируйте [prometheus.vps.yml.example](prometheus.vps.yml.example) в `prometheus.vps.yml`, впишите адрес и запустите с `PROMETHEUS_CONFIG=./prometheus.vps.yml`. Этот файл в `.gitignore`, чтобы адрес сервера не попадал в репозиторий.

Логи пишутся в stdout в JSON (`log/slog`), по строке на запрос:

```json
{"time":"2026-10-12T09:38:59Z","level":"INFO","msg":"request","method":"POST","path":"/compare","status":200,"duration_ms":2216,"remote_addr":"172.18.0.1:51469","artist_a":"Queen","title_a":"Bohemian Rhapsody","artist_b":"Guns N Roses","title_b":"Sweet Child O Mine","cache_a":"miss","cache_b":"miss","tags_from_a":"recording","tags_from_b":"recording","genre_score":0.17,"weighted_score":0.3,"low_data":false}
```

При ошибке в строке есть `error` (с подробностями апстрима) и `failed_song` (`song_a` или `song_b`). Если клиент отключился, не дождавшись ответа, записывается статус 499.

## mood-service

Определяет настроение песни по её тексту. Каталог [mood-service/](mood-service/), один файл `main.py`.

`POST /analyze`:

```json
{"artist": "Queen", "title": "Bohemian Rhapsody"}
```

```json
{"mood": "sad", "confidence": 0.987, "energy": 0.312}
```

Как это работает:

1. Текст песни берётся из Genius через библиотеку `lyricsgenius`.
2. Тональность первых 2000 символов оценивает модель `distilbert-base-uncased-finetuned-sst-2-english`; `confidence` — её уверенность.
3. `energy` (0–1) — эвристика по тексту: восклицательные знаки, слова заглавными буквами, доля уникальных слов.
4. `mood` выводится из пары «тональность + энергия»: позитивный текст — `energetic` (энергия от 0,5) или `happy`, негативный — `angry` или `sad`.

Модель обучена на английском, для текстов на других языках результат ненадёжен.

| Код | Когда |
|---|---|
| 404 | текст не найден в Genius |
| 422 | текст найден, но пуст после очистки |
| 500 | сбой модели |
| 502 | Genius недоступен |
| 503 | модель ещё не загрузилась |

Ошибки приходят в формате FastAPI: `{"detail": "..."}`. Есть `GET /health`.

Единственная переменная — `GENIUS_API_KEY` (шаблон в [mood-service/.env.example](mood-service/.env.example)). При первом запуске модель скачивается из Hugging Face.

## user-service

Учётные записи и история сравнений. Каталог [user-service/](user-service/), отдельный Go-модуль, данные в PostgreSQL через GORM.

| Метод | Путь | Авторизация | Назначение |
|---|---|---|---|
| POST | `/register` | нет | регистрация: `{"email", "password"}` → `201 {"token"}` |
| POST | `/login` | нет | вход: `{"email", "password"}` → `200 {"token"}` |
| GET | `/me` | Bearer | профиль: `{"id", "email", "created_at"}` |
| POST | `/history` | Bearer | сохранить сравнение: `{"song_a", "song_b", "genre_score", "mood_score"}` |
| GET | `/history` | Bearer | свои сравнения, сначала новые |
| GET | `/health` | нет | проверка живости |

- **Пароли** хранятся как хеши bcrypt.
- **Токен** — JWT (HS256) на 24 часа, передаётся в заголовке `Authorization: Bearer <token>`.
- **Защита от перебора:** `/register` и `/login` ограничены пятью запросами в секунду с одного IP (всплеск до 10), сверх этого — 429.
- **История** хранит то, что прислал клиент: `song_a` и `song_b` — строки, оценки — числа. Сам сервис api не вызывает.
- Ошибки приходят как `{"error": "..."}`.

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `SECRET_KEY` | нет, обязательна | ключ подписи JWT |
| `DATABASE_URL` | не задана | строка подключения к PostgreSQL; если задана, имеет приоритет |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_SSLMODE` | `localhost`, `5432`, `postgres`, пусто, `user_service`, `disable` | параметры подключения, если `DATABASE_URL` не задана |

**`SECRET_KEY` проверяется при старте.** Сервис откажется запускаться, если ключ пуст, оставлен как в `.env.example` или короче 32 символов: токен, подписанный таким ключом, можно подделать. Сгенерировать ключ:

```bash
openssl rand -hex 32
```

В compose ключ передаётся через `USER_SERVICE_SECRET_KEY`. Таблицы создаются автоматически при старте.

## Разработка

```bash
go test -race ./...                        # api
(cd user-service && go test ./...)         # user-service
gofmt -l cmd internal user-service         # должно быть пусто
go vet ./...
```

- Тесты api не ходят в сеть: MusicBrainz заменён на `httptest`, Redis — на хранилище в памяти. Один тест воспроизводит записанный настоящий ответ MusicBrainz ([testdata](internal/provider/testdata/)) и упадёт, если формат выдачи перестанет совпадать с ожидаемым.
- Тесты хранилища user-service идут на SQLite в памяти, а в compose и в проде используется PostgreSQL. Поведение, специфичное для Postgres (типы, ограничения, диалект SQL), ими не проверяется.

Локальный запуск api без Docker: `go run ./cmd/server` (кэш выключен, пока не задана `REDIS_URL`).

### Структура

```
cmd/server/                  точка входа api, HTTP-сервер на :8080
internal/api/                хендлеры, роутинг, логирование, метрики HTTP
internal/provider/           клиент MusicBrainz, выбор источника тегов
internal/cache/              кэш в Redis с деградацией и склейкой запросов
internal/tags/               нормализация тегов, синонимы, стоп-лист
internal/similarity/         Jaccard и взвешенный Jaccard
mood-service/                сервис настроения (Python)
user-service/                сервис пользователей (Go, отдельный модуль)
scripts/smoke.sh             ручная проверка /compare
grafana/provisioning/        datasource и дашборд Grafana
prometheus.yml               конфигурация Prometheus для compose
deploy/                      systemd-юнит и конфиг nginx для VPS
.github/workflows/deploy.yml CI и деплой
```

## Деплой на VPS

В прод уходит только `api`: статический бинарь под systemd. Каталог `/opt/song-similarity`, имя юнита и имя бинаря остались от прежнего названия проекта и намеренно не переименованы — на них завязан уже настроенный сервер.

### Первоначальная настройка (один раз, руками)

1. Создать системного пользователя, от которого будет работать сервис:

   ```bash
   sudo useradd --system --no-create-home --shell /usr/sbin/nologin app
   ```

2. Создать рабочую директорию и передать её пользователю деплоя (тому, под которым GitHub Actions логинится по SSH — секрет `SSH_USER`), чтобы он мог класть туда бинарь по scp:

   ```bash
   sudo mkdir -p /opt/song-similarity
   sudo chown "$SSH_USER":"$SSH_USER" /opt/song-similarity
   ```

3. Создать файл окружения, который читает systemd-юнит. Он может быть пустым — у всех переменных есть значения по умолчанию (см. [Переменные окружения](#переменные-окружения)):

   ```bash
   printf 'MB_USER_AGENT=song-vibes-check/0.2.0 ( you@example.com )\n' | sudo tee /opt/song-similarity/.env
   sudo chown app:app /opt/song-similarity/.env
   ```

   Кэш на VPS необязателен. Чтобы включить его, поставьте Redis (`sudo apt install redis-server`) и допишите в этот файл `REDIS_URL=redis://127.0.0.1:6379/0`. Без `REDIS_URL` сервис работает без кэша.

4. Установить systemd-юнит (файл лежит в репозитории — [deploy/song-similarity.service](deploy/song-similarity.service)):

   ```bash
   sudo cp deploy/song-similarity.service /etc/systemd/system/song-similarity.service
   sudo systemctl daemon-reload
   sudo systemctl enable song-similarity
   ```

   Содержимое юнита:

   ```ini
   [Unit]
   Description=Song Similarity API
   After=network.target

   [Service]
   Type=simple
   User=app
   WorkingDirectory=/opt/song-similarity
   EnvironmentFile=/opt/song-similarity/.env
   ExecStart=/opt/song-similarity/app
   Restart=on-failure
   RestartSec=5s
   StandardOutput=journal
   StandardError=journal

   [Install]
   WantedBy=multi-user.target
   ```

5. Первый деплой бинаря (дальше это делает CI):

   ```bash
   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o song-similarity ./cmd/server
   scp song-similarity "$SSH_USER@$SSH_HOST:/opt/song-similarity/app"
   ssh "$SSH_USER@$SSH_HOST" 'chmod +x /opt/song-similarity/app && sudo systemctl start song-similarity'
   ```

6. Разрешить пользователю деплоя перезапускать сервис без пароля (нужно для шага health-check в CI) — добавить в `sudo visudo`:

   ```
   SSH_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl stop song-similarity, /usr/bin/systemctl start song-similarity
   ```

   Замените `SSH_USER` на реальное имя пользователя. Без этого шаг `sudo systemctl ...` в workflow будет требовать пароль и упадёт.

7. Проверить:

   ```bash
   curl -sf http://127.0.0.1:8080/health
   ```

### Nginx + SSL (опционально, если сервис смотрит наружу под доменом)

Конфиг лежит в [deploy/nginx.conf](deploy/nginx.conf):

```nginx
server {
    listen 80;
    server_name your-domain.example.com;

    location /.well-known/acme-challenge/ {
        root /var/www/certbot;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    server_name your-domain.example.com;

    ssl_certificate     /etc/letsencrypt/live/your-domain.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/your-domain.example.com/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Установка:

```bash
sudo apt install nginx certbot python3-certbot-nginx
sudo cp deploy/nginx.conf /etc/nginx/sites-available/song-similarity
sudo ln -s /etc/nginx/sites-available/song-similarity /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
sudo certbot --nginx -d your-domain.example.com
```

`certbot --nginx` сам допишет секцию SSL и настроит редирект — вручную прописывать пути к сертификатам, как в примере выше, обычно не требуется, certbot сделает это за вас при первом запуске.

### CI/CD (GitHub Actions)

Workflow [.github/workflows/deploy.yml](.github/workflows/deploy.yml) состоит из двух джоб.

**`test`** — на каждый pull request и на push в `main`: `gofmt`, `go vet`, `go test -race ./...` для основного модуля и `go vet` + `go test` для `user-service`.

> Тесты хранилища `user-service` идут на in-memory SQLite, а в docker-compose и в проде используется PostgreSQL. Поведение, специфичное для Postgres (типы, ограничения, диалект SQL), этими тестами не проверяется.

**`deploy`** — только при push в `main` и только после успешной `test`:

1. checkout кода;
2. `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o song-similarity ./cmd/server`;
3. scp бинаря на VPS в `/opt/song-similarity/app`;
4. по ssh: `systemctl stop` → `chmod +x` → `systemctl start` → `sleep 2` → `curl -sf /health`; если health-check не прошёл — сервис останавливается и job падает (`exit 1`), что видно в логах GitHub Actions.

Добавить в **Settings → Secrets and variables → Actions** репозитория:

| Secret     | Значение                                              |
|------------|--------------------------------------------------------|
| `SSH_HOST` | IP или домен сервера                                   |
| `SSH_USER` | пользователь для SSH-деплоя (не `app` — нужен shell и sudo-права на `systemctl`, см. п.6 выше) |
| `VPS_KEY`  | приватный SSH-ключ (без пароля) для этого пользователя  |

Публичную часть ключа нужно заранее добавить в `~/.ssh/authorized_keys` пользователя `SSH_USER` на сервере.

## Ограничения

- **Данные MusicBrainz краудсорсные и меняются.** Теги проставляют пользователи: у популярных песен их десятки, у малоизвестных может не быть ни одного, и набор со временем меняется. Оценка для одной и той же пары может измениться, когда истечёт кэш. При `low_data: true` оценке доверять не стоит.
- **Повторяемость держится на фильтре `tag:*`.** Обычный поиск MusicBrainz на один и тот же запрос возвращает разные записи: у популярной песни сотни записей с одинаковой оценкой, порядок их нестабилен. Сервис ищет только записи с тегами — их немного, и они приходят все сразу. Если у песни больше 100 записей с тегами, в выдачу попадёт только часть, и результат снова может меняться от запроса к запросу.
- **Холодный запрос занимает несколько секунд.** MusicBrainz разрешает в среднем один запрос в секунду с адреса. Сервис отправляет запросы строго по одному с паузой в секунду после каждого ответа, а на пару песен нужно от двух до шести запросов. Одновременные запросы разных песен ждут в общей очереди; те, что не уложились в `REQUEST_TIMEOUT`, получают 502.
- **Лимит общий на все экземпляры за одним IP.** Очередь живёт в памяти процесса: два экземпляра сервиса за одним адресом превысят лимит, и MusicBrainz начнёт отвечать 503. На 503 и сетевую ошибку сервис делает одну повторную попытку.
- **Имя исполнителя должно совпасть.** Сравнение прощает регистр, пунктуацию и диакритику, но не опечатки и не другой язык: «Битлз» не найдёт The Beatles.
- **Жанровость тегов не проверяется.** Отбрасывается только то, что есть в стоп-листе; теги вроде `party` или `road` участвуют в сравнении наравне с жанрами.
- **Прод — один бинарь на одном VPS.** Docker используется только локально; Redis на сервере ставится отдельно и необязателен.
