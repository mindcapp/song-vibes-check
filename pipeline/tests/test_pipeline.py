import json

import numpy as np
import pandas as pd

from payloads import build_payloads, write_points_json
from preprocess import Preprocessor, drop_non_finite, fit_preprocessor


def test_zscore_and_pca_shape():
    rng = np.random.RandomState(0)
    x = rng.normal(loc=5.0, scale=[1.0, 10.0, 0.1] * 10, size=(200, 30))
    pre, vectors = fit_preprocessor(x, n_components=8)
    scaled = pre.scaler.transform(x)
    assert np.allclose(scaled.mean(axis=0), 0, atol=1e-8)
    assert np.allclose(scaled.std(axis=0), 1, atol=1e-8)
    assert vectors.shape == (200, 8)


def test_drop_non_finite_and_params_roundtrip(tmp_path):
    df = pd.DataFrame(np.random.RandomState(1).normal(size=(50, 6)), index=range(10, 60))
    df.iloc[3, 2] = np.nan
    df.iloc[7, 0] = np.inf
    clean = drop_non_finite(df)
    assert len(clean) == 48 and 13 not in clean.index and 17 not in clean.index

    pre, vectors = fit_preprocessor(clean.to_numpy(), n_components=3)
    path = str(tmp_path / "pre.joblib")
    pre.save(path)
    again = Preprocessor.load(path).transform(clean.to_numpy())
    assert np.allclose(vectors, again, atol=1e-6)


def test_payload_format_and_points_json(tmp_path):
    cols = pd.MultiIndex.from_tuples(
        [("track", "title"), ("artist", "name"), ("track", "genre_top")]
    )
    tracks = pd.DataFrame([["Food", "AWOL", "Hip-Hop"], ["Freeway", "Kurt Vile", "Pop"]],
                          index=pd.Index([2, 10], name="track_id"), columns=cols)
    payloads = build_payloads(tracks, np.array([[1.23456789, -2.0], [0.5, 3.0]]))
    assert payloads[0] == {"track_id": 2, "title": "Food", "artist": "AWOL",
                           "genre": "Hip-Hop", "x": 1.2346, "y": -2.0}
    assert isinstance(payloads[1]["track_id"], int)

    out = tmp_path / "points.json"
    write_points_json(payloads, str(out))
    points = json.loads(out.read_text())
    assert set(points[0]) == {"track_id", "x", "y", "genre"}
