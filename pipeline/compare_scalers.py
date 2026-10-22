"""Compare normalisers before PCA by same_genre@5 and print a markdown table."""
import argparse
import logging
import os
from typing import List, Optional

import numpy as np

from evaluate import log_scores, same_genre_at_k
from fma_loader import SUBSETS, load_subset
from preprocess import SCALERS, drop_non_finite, fit_preprocessor

log = logging.getLogger("compare")


def main(argv: Optional[List[str]] = None) -> None:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--data-dir", default=os.path.expanduser("~/data/fma/fma_metadata"))
    p.add_argument("--subset", default="small", choices=SUBSETS)
    p.add_argument("--components", type=int, default=64)
    p.add_argument("--k", type=int, default=5)
    p.add_argument("--candidates", type=int, default=30)
    args = p.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")

    features, tracks = load_subset(args.data_dir, args.subset)
    features = drop_non_finite(features)
    tracks = tracks.loc[features.index]
    x = features.to_numpy(dtype=np.float64)
    genres = tracks["track", "genre_top"].to_numpy()
    artists = tracks["artist", "name"].to_numpy()

    rows = []
    for name in SCALERS:
        pre, vectors = fit_preprocessor(x, args.components, scaler=name)
        scores = same_genre_at_k(vectors, genres, artists, args.k, args.candidates)
        log_scores(name, scores, args.k)
        rows.append((name, pre.pca.explained_variance_ratio_.sum(), scores))

    best = max(rows, key=lambda r: r[2]["other_artist"])[0]
    print("\n| scaler | PCA explained var | same_genre@%d | same_genre@%d, other artist |" % (args.k, args.k))
    print("|---|---|---|---|")
    for name, var, s in rows:
        mark = " **(best)**" if name == best else ""
        print("| %s%s | %.1f%% | %.1f%% | %.1f%% |"
              % (name, mark, 100 * var, 100 * s["all"], 100 * s["other_artist"]))
    print("\nrandom baseline: %.1f%%" % (100 / tracks["track", "genre_top"].nunique()))


if __name__ == "__main__":
    main()
