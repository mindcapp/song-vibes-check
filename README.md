# song-similarity

MVP-микросервис на Go для сравнения двух песен по жанровым тегам из MusicBrainz.

## Структура

```
cmd/server/main.go              — точка входа, HTTP-сервер на :8080
internal/provider/musicbrainz.go — клиент MusicBrainz API (rate limit 1 req/sec)
internal/similarity/genre.go     — Jaccard similarity по тегам
internal/api/                    — хендлеры, роутинг, логирование, метрики
Dockerfile                       — multi-stage сборка (golang:1.22-alpine → alpine:latest)
docker-compose.yml               — api + prometheus + grafana
prometheus.yml                   — scrape-конфиг для локального docker-compose
grafana/provisioning/            — Prometheus datasource, подключается автоматически
deploy/song-similarity.service   — systemd юнит для VPS
deploy/nginx.conf                — reverse proxy + SSL (Let's Encrypt) для VPS
.github/workflows/deploy.yml     — CI/CD: сборка бинаря и деплой на VPS по push в main
```

## Эндпоинты

| Метод | Путь        | Назначение                                  |
|-------|-------------|----------------------------------------------|
| POST  | `/compare`  | сравнение жанров двух треков                 |
| GET   | `/health`   | health check (`{"status":"ok"}`)             |
| GET   | `/metrics`  | метрики в формате Prometheus                 |

Каждый запрос логируется в stdout структурированным JSON (`log/slog`), например:

```json
{"time":"2026-08-31T03:30:31+03:00","level":"INFO","msg":"request","method":"POST","path":"/compare","status":502,"duration_ms":429,"remote_addr":"[::1]:55492","artist_a":"...","title_a":"...","artist_b":"...","title_b":"..."}
```

Поля: `time`, `method`, `path`, `status`, `duration_ms`, `remote_addr`, а для `/compare` дополнительно `artist_a`, `title_a`, `artist_b`, `title_b` и (при успехе) `genre_score`.

## Локальная разработка через Docker

```bash
docker compose up --build
```

Поднимет три сервиса в одной сети:

- **API** — http://localhost:8080/compare
- **Prometheus** — http://localhost:9090 (скрейпит API каждые 15s, данные хранятся в volume `prometheus_data`, переживают перезапуск)
- **Grafana** — http://localhost:3000 (логин `admin` / `admin`, задаётся `GF_SECURITY_ADMIN_PASSWORD` в docker-compose.yml). Datasource на Prometheus прописан автоматически через `grafana/provisioning/datasources/prometheus.yml` — ничего настраивать руками не нужно, дашборды можно строить сразу.

Пример запроса:

```bash
curl -s -X POST localhost:8080/compare \
  -H 'Content-Type: application/json' \
  -d '{
    "song_a": {"artist": "Queen", "title": "Bohemian Rhapsody"},
    "song_b": {"artist": "Guns N Roses", "title": "Sweet Child O Mine"}
  }' | jq
```

Остановить и снести контейнеры (без потери volumes): `docker compose down`. Снести вместе с данными Prometheus/Grafana: `docker compose down -v`.

## Переменные окружения

| Переменная      | Обязательна | Назначение                                                                 |
|-----------------|-------------|------------------------------------------------------------------------------|
| `MB_USER_AGENT` | нет (но настоятельно рекомендуется) | User-Agent для MusicBrainz API вида `song-similarity/0.1.0 ( contact@example.com )`. Без нормального User-Agent с контактом MusicBrainz может блокировать запросы — так требует их API policy. Если переменная не задана, используется дефолтный placeholder. |

## Деплой на VPS

### Первоначальная настройка (один раз, руками)

1. Создать системного пользователя, от которого будет работать сервис:

   ```bash
   sudo useradd --system --no-create-home --shell /usr/sbin/nologin app
   ```

2. Создать рабочую директорию и передать её пользователю деплоя (тому, под которым логинится GitHub Actions по SSH — `VPS_USER`), чтобы он мог класть туда бинарь по scp:

   ```bash
   sudo mkdir -p /opt/song-similarity
   sudo chown "$VPS_USER":"$VPS_USER" /opt/song-similarity
   ```

3. Задать `MB_USER_AGENT` в файле окружения, который читает systemd-юнит:

   ```bash
   printf 'MB_USER_AGENT=song-similarity/0.1.0 ( you@example.com )\n' | sudo tee /opt/song-similarity/.env
   sudo chown app:app /opt/song-similarity/.env
   ```

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
   scp song-similarity "$VPS_USER@$VPS_HOST:/opt/song-similarity/app"
   ssh "$VPS_USER@$VPS_HOST" 'chmod +x /opt/song-similarity/app && sudo systemctl start song-similarity'
   ```

6. Разрешить пользователю деплоя перезапускать сервис без пароля (нужно для шага health-check в CI) — добавить в `sudo visudo`:

   ```
   VPS_USER ALL=(ALL) NOPASSWD: /usr/bin/systemctl stop song-similarity, /usr/bin/systemctl start song-similarity
   ```

   Замените `VPS_USER` на реальное имя пользователя. Без этого шаг `sudo systemctl ...` в workflow будет требовать пароль и упадёт.

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

Workflow [.github/workflows/deploy.yml](.github/workflows/deploy.yml) запускается на каждый push в `main`:

1. checkout кода;
2. `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o song-similarity ./cmd/server`;
3. scp бинаря на VPS в `/opt/song-similarity/app`;
4. по ssh: `systemctl stop` → `chmod +x` → `systemctl start` → `sleep 2` → `curl -sf /health`; если health-check не прошёл — сервис останавливается и job падает (`exit 1`), что видно в логах GitHub Actions.

Добавить в **Settings → Secrets and variables → Actions** репозитория:

| Secret     | Значение                                              |
|------------|--------------------------------------------------------|
| `VPS_HOST` | IP или домен сервера                                   |
| `VPS_USER` | пользователь для SSH-деплоя (не `app` — нужен shell и sudo-права на `systemctl`, см. п.6 выше) |
| `VPS_KEY`  | приватный SSH-ключ (без пароля) для этого пользователя  |

Публичную часть ключа нужно заранее добавить в `~/.ssh/authorized_keys` пользователя `VPS_USER` на сервере.

## Мониторинг

- `/metrics` отдаёт `http_requests_total{method,path,status}` и `http_request_duration_seconds{method,path}`.
- В docker-compose Prometheus уже настроен скрейпить `song-similarity-api:8080` каждые 15s ([prometheus.yml](prometheus.yml)).
- В Grafana datasource на Prometheus прописывается автоматически при старте контейнера — дашборды создавайте по вкусу поверх метрик выше.

## Известные ограничения MVP

- Для `/compare` берётся первый результат поиска MusicBrainz, у которого вообще есть теги (проверяются первые 25 совпадений) — но даже так у части треков тегов в MusicBrainz может не быть вовсе (это краудсорс-данные), тогда сервис вернёт 502.
- Прод-деплой предполагает бинарь, а не Docker-образ на VPS (Docker используется только для локальной разработки/наблюдаемости) — так проще для systemd-based single-VPS сетапа из задания. Если нужен деплой самим Docker-образом на VPS, потребуется отдельно docker-registry шаг в workflow.
