# pipeline — офлайн-подготовка данных для поиска похожих треков

Берёт признаки librosa из FMA (подмножество `small`, 8 000 треков), чистит их,
нормализует, сжимает PCA до 64 измерений, считает 2D-координаты UMAP и грузит
всё в Qdrant. Для фронта отдельно пишет компактный `points.json`.

Данные FMA лежат **вне репозитория** (`~/data/fma/fma_metadata/`: `tracks.csv`,
`features.csv`) — их не копируем и не коммитим.

## Запуск по шагам

Нужны Python 3.9+ и Docker.

```bash
cd pipeline

# 1. окружение
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt

# 2. Qdrant (порты только на 127.0.0.1)
docker compose up -d
curl http://127.0.0.1:6333/healthz

# 3. пайплайн
.venv/bin/python prepare_data.py \
    --data-dir ~/data/fma/fma_metadata \
    --collection tracks \
    --qdrant-url http://127.0.0.1:6333
```

Аргументы `prepare_data.py`: `--data-dir`, `--subset` (small/medium/large),
`--collection`, `--qdrant-url`, `--output-dir`, `--components` (PCA, по умолчанию 64),
`--limit N` (отладка), `--skip-qdrant` (только локальные файлы).
Коллекция пересоздаётся при каждом запуске.

Что получается в `pipeline/output/` (папка в `.gitignore`):

| файл | содержимое |
|---|---|
| `points.json` | `[{"track_id","x","y","genre"}, ...]` для фронта, ~0.5 МБ |
| `preprocessor.joblib` | обученные `StandardScaler` и `PCA` |

Применить параметры к новым данным:

```python
from preprocess import Preprocessor
pre = Preprocessor.load("output/preprocessor.joblib")
vectors = pre.transform(raw_features)  # (n, 518) -> (n, 64), для запроса в Qdrant
```

В Qdrant (коллекция `tracks`, косинусное расстояние, 64 измерения) id точки равен
`track_id`, payload: `track_id, title, artist, genre, x, y`.

## Сколько занимает

Замер на MacBook (Python 3.9, один поток UMAP из-за фиксированного `random_state=42`),
`small` = 8 000 треков:

- чтение CSV: ~20 с (почти всё — 951 МБ `features.csv`)
- z-нормализация + PCA: <1 с (64 компоненты объясняют ~76 % дисперсии)
- UMAP: ~65 с
- загрузка в Qdrant: ~1 с
- итого ~1.5–2.5 мин, плюс ~1 мин на первый импорт numba/UMAP

## Как проверить

Ближайшие соседи для случайных треков (по одному на жанр):

```bash
.venv/bin/python neighbors.py                # 5 случайных треков
.venv/bin/python neighbors.py --track-id 2   # конкретный трек
```

Запрос напрямую в REST API (ближайшие к треку с id 2):

```bash
curl -s http://127.0.0.1:6333/collections/tracks/points/query \
  -H 'Content-Type: application/json' \
  -d '{"query": 2, "limit": 5, "with_payload": true}'
```

Количество точек: `curl -s http://127.0.0.1:6333/collections/tracks | python3 -m json.tool`
(`points_count` должен быть 8000).

## Тесты

```bash
.venv/bin/python -m pytest -q
```
