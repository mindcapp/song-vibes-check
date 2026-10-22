"""Loaders for FMA metadata CSVs (header layout follows mdeff/fma utils.py)."""
import logging
import os
from typing import Optional

import pandas as pd

log = logging.getLogger(__name__)

# tracks.csv stores the smallest subset a track belongs to; subsets are nested.
SUBSETS = ("small", "medium", "large")


def load_features(data_dir: str) -> pd.DataFrame:
    """features.csv: 3 header levels (feature / statistics / number), librosa only."""
    path = os.path.join(data_dir, "features.csv")
    df = pd.read_csv(path, index_col=0, header=[0, 1, 2])
    log.info("features: %s rows x %s cols", *df.shape)
    return df


def load_tracks(data_dir: str) -> pd.DataFrame:
    """tracks.csv: 2 header levels (group / field)."""
    path = os.path.join(data_dir, "tracks.csv")
    df = pd.read_csv(path, index_col=0, header=[0, 1])
    log.info("tracks: %s rows x %s cols", *df.shape)
    return df


def select_subset(tracks: pd.DataFrame, subset: str = "small") -> pd.DataFrame:
    """Tracks of `subset`, including the smaller nested ones (as in utils.py)."""
    if subset not in SUBSETS:
        raise ValueError("unknown subset %r, expected one of %s" % (subset, SUBSETS))
    allowed = SUBSETS[: SUBSETS.index(subset) + 1]
    selected = tracks[tracks["set", "subset"].isin(allowed)]
    log.info("subset %s: %d tracks", subset, len(selected))
    return selected


def load_subset(data_dir: str, subset: str = "small", limit: Optional[int] = None):
    """Return (features, tracks) aligned on track_id for the requested subset."""
    tracks = select_subset(load_tracks(data_dir), subset)
    features = load_features(data_dir)
    ids = tracks.index.intersection(features.index)
    if limit is not None:
        ids = ids[:limit]
    return features.loc[ids], tracks.loc[ids]
