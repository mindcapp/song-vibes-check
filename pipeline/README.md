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
`--scaler` (`standard` по умолчанию, `robust`, `quantile`), `--limit N` (отладка), `--skip-qdrant` (только локальные файлы).
Коллекция пересоздаётся при каждом запуске.

Что получается в `pipeline/output/` (папка в `.gitignore`):

| файл | содержимое |
|---|---|
| `points.json` | `[{"track_id","x","y","genre"}, ...]` для фронта, ~0.5 МБ |
| `preprocessor.joblib` | обученные нормализатор (по `--scaler`) и `PCA` |

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
- метрика same_genre@5: ~2 с
- UMAP: ~50–65 с
- загрузка в Qdrant: ~1 с
- итого ~1.5–2.5 мин, плюс ~1 мин на первый импорт numba/UMAP

## Качество: same_genre@5

Для каждого из 8 000 треков берутся 5 ближайших соседей по косинусу в 64-мерном
PCA-пространстве (сам трек исключён) и считается доля соседей с тем же `genre_top`.

- **all** — соседи как есть;
- **other artist** — соседи того же `artist.name` пропускаются: берём 30 кандидатов
  и первые 5 от другого исполнителя (если их меньше 5 — дотягиваем из полного ранжирования).
  Это честнее: в FMA у исполнителя часто много треков одного альбома и жанра.

Случайная выдача даёт 12.5 % (8 жанров по 1 000 треков). Жанры и исполнители
используются **только для оценки**, в обучении скейлера/PCA/UMAP их нет.
`prepare_data.py` пишет метрику в лог после PCA.

### Сравнение нормализаций (перед PCA 64)

```bash
.venv/bin/python compare_scalers.py
```

| нормализация | объяснённая дисперсия PCA 64 | same_genre@5, all | same_genre@5, other artist |
|---|---|---|---|
| **StandardScaler (выбран)** | 75.7 % | **43.8 %** | **35.7 %** |
| RobustScaler | 100.0 % | 37.2 % | 30.6 % |
| QuantileTransformer (normal) | 84.7 % | 39.3 % | 32.4 % |
| случайная выдача | — | 12.5 % | 12.5 % |

Выбран **StandardScaler**: лучший по варианту «other artist» (35.7 %, в ~2.9 раза выше случайного).
RobustScaler проигрывает из-за признака `rmse/min/01`: его межквартильный размах
~0.0004, после деления значения доходят до ~25 000, и одна эта колонка даёт 99.9 %
всей дисперсии — отсюда «100 %» у PCA и худшие соседи. QuantileTransformer
выравнивает распределения, но заодно стирает информативные хвосты.

### Примеры соседей (StandardScaler, без того же исполнителя)

`.venv/bin/python neighbors.py --exclude-same-artist` (по одному случайному треку
из 5 разных жанров, `--seed 42`), в скобках — косинусная близость:

**File Under Toner — greyish [Experimental]**
- 0.915 Mattin — Computer Music/Post-Fordism [Experimental]
- 0.869 Seiei Jack — Ancient Greece Noise [Experimental]
- 0.865 Lately Kind of Yeah — 恐怖 [Electronic]
- 0.848 Fierbinteanu — We Wanna Change The World [Pop]
- 0.845 Qkcofse — Phase a Couple Rooms [Experimental]

**Str8 Sounds — Moving Toward You [Electronic]**
- 0.805 harmonic defiance — airtight [Electronic]
- 0.801 Adubter — Sun Rice [Electronic]
- 0.779 WatFatMan — galerie de portraits [Hip-Hop]
- 0.766 Alaclair Ensemble — STM LIGNE MAUVE PART 1 [Hip-Hop]
- 0.765 Beat Doctor — Organic (electric edit) [Electronic]

**Mamá Patxanga — Caliente! Caliente! [International]**
- 0.741 Fierbinteanu — Keep Walking Away [Pop]
- 0.694 Lamprey — Drowning Mud [Rock]
- 0.685 The Impossebulls — Six Pack [Hip-Hop]
- 0.683 TIGERBERRY — Dumb [Rock]
- 0.678 Action Will Be Taken — While you're waiting for the revolution [Rock]

**Curtis Eller — Always Sugar for the Horses [Folk]**
- 0.716 SK — The Statement [Hip-Hop]
- 0.636 Monk Turner — If She Gives You Her Heart [Pop]
- 0.622 Buguinha Dub — Celebrando a vida [International]
- 0.606 Čaj na Ex — Zlevnené zboží [International]
- 0.595 My Bubba & Mi — Apple spell [Folk]

**Connections — Cruise Control [Rock]**
- 0.726 Laura Stevenson and the Cans — Landslide Song/The Dig [Folk]
- 0.649 The Nude Party — Stingray [Rock]
- 0.645 Vows — The Great Show [Rock]
- 0.640 Supercute — Assked [Pop]
- 0.638 Emerald Park — Reality Bites [Pop]

## Как проверить

Ближайшие соседи для случайных треков (по одному на жанр):

```bash
.venv/bin/python neighbors.py                # 5 случайных треков
.venv/bin/python neighbors.py --track-id 2   # конкретный трек
.venv/bin/python neighbors.py --exclude-same-artist  # без треков того же исполнителя
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
