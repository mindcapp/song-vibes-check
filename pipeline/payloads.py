"""Builds Qdrant points and the compact points.json for the frontend."""
import json
import os
from typing import Any, Dict, List

import numpy as np
import pandas as pd


def build_payloads(
    tracks: pd.DataFrame, coords: np.ndarray
) -> List[Dict[str, Any]]:
    """One payload per track (row order of `tracks` == row order of `coords`)."""
    payloads = []
    for (track_id, row), (x, y) in zip(tracks.iterrows(), coords):
        payloads.append(
            {
                "track_id": int(track_id),
                "title": str(row["track", "title"]),
                "artist": str(row["artist", "name"]),
                "genre": str(row["track", "genre_top"]),
                "x": round(float(x), 4),
                "y": round(float(y), 4),
            }
        )
    return payloads


def write_points_json(payloads: List[Dict[str, Any]], path: str) -> None:
    """Compact file for the frontend: track_id, x, y, genre only."""
    points = [{k: p[k] for k in ("track_id", "x", "y", "genre")} for p in payloads]
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(points, fh, ensure_ascii=False, separators=(",", ":"))
