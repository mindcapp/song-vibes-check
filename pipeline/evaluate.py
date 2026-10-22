"""same_genre@k: share of k nearest neighbours (cosine) with the query's genre.

Genres and artists are used for evaluation only, never for fitting.
"""
import logging
from typing import Dict, List, Sequence

import numpy as np

log = logging.getLogger(__name__)


def _unit(vectors: np.ndarray) -> np.ndarray:
    v = np.asarray(vectors, dtype=np.float32)
    return v / np.maximum(np.linalg.norm(v, axis=1, keepdims=True), 1e-12)


def nearest_neighbours(
    vectors: np.ndarray, artists: Sequence[str], k: int = 5,
    candidates: int = 30, exclude_same_artist: bool = False, chunk: int = 1000,
) -> List[np.ndarray]:
    """Indices of k nearest neighbours per row by cosine similarity, self excluded.

    With exclude_same_artist the top `candidates` are filtered to other artists;
    if fewer than k remain, the whole ranking is used for that row.
    """
    unit = _unit(vectors)
    artists = np.asarray(artists)
    n = len(unit)
    result = []
    for start in range(0, n, chunk):
        sims = unit[start:start + chunk] @ unit.T
        rows = np.arange(start, min(start + chunk, n))
        sims[rows - start, rows] = -np.inf  # drop self
        top = min(candidates if exclude_same_artist else k, n - 1)
        part = np.argpartition(-sims, top - 1, axis=1)[:, :top]
        for i, row in enumerate(rows):
            order = part[i][np.argsort(-sims[i, part[i]], kind="stable")]
            if exclude_same_artist:
                order = order[artists[order] != artists[row]]
                if len(order) < k:
                    full = np.argsort(-sims[i], kind="stable")[: n - 1]
                    order = full[artists[full] != artists[row]]
            result.append(order[:k])
    return result


def same_genre_at_k(
    vectors: np.ndarray, genres: Sequence[str], artists: Sequence[str],
    k: int = 5, candidates: int = 30,
) -> Dict[str, float]:
    """Return {'all': ..., 'other_artist': ...} same_genre@k (0..1)."""
    genres = np.asarray(genres)
    scores = {}
    for name, exclude in (("all", False), ("other_artist", True)):
        nn = nearest_neighbours(vectors, artists, k, candidates, exclude)
        hits = [np.mean(genres[idx] == genres[i]) for i, idx in enumerate(nn)]
        scores[name] = float(np.mean(hits))
    return scores


def log_scores(label: str, scores: Dict[str, float], k: int = 5) -> None:
    log.info(
        "%s same_genre@%d: all=%.1f%% other_artist=%.1f%%",
        label, k, 100 * scores["all"], 100 * scores["other_artist"],
    )
