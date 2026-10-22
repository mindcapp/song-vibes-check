"""Offline pipeline: FMA librosa features -> PCA -> UMAP -> Qdrant + points.json."""
import argparse
import logging
import os
import time
from typing import List, Optional

import numpy as np
import umap
from qdrant_client import QdrantClient, models

from evaluate import log_scores, same_genre_at_k
from fma_loader import SUBSETS, load_subset
from payloads import build_payloads, write_points_json
from preprocess import RANDOM_STATE, SCALERS, drop_non_finite, fit_preprocessor

log = logging.getLogger("pipeline")


def compute_umap(vectors: np.ndarray) -> np.ndarray:
    # fixed random_state => reproducible layout (disables UMAP multithreading)
    reducer = umap.UMAP(
        n_components=2, metric="cosine", n_neighbors=15, min_dist=0.1,
        random_state=RANDOM_STATE,
    )
    return reducer.fit_transform(vectors)


def upload_to_qdrant(
    client: QdrantClient, collection: str, vectors: np.ndarray,
    track_ids: List[int], payloads: List[dict], batch_size: int = 500,
) -> None:
    if client.collection_exists(collection):
        log.info("recreating collection %s", collection)
        client.delete_collection(collection)
    client.create_collection(
        collection,
        vectors_config=models.VectorParams(
            size=vectors.shape[1], distance=models.Distance.COSINE
        ),
    )
    for start in range(0, len(track_ids), batch_size):
        end = start + batch_size
        client.upsert(
            collection,
            points=models.Batch(
                ids=track_ids[start:end],
                vectors=vectors[start:end].tolist(),
                payloads=payloads[start:end],
            ),
        )
        log.info("uploaded %d/%d", min(end, len(track_ids)), len(track_ids))


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--data-dir", default=os.path.expanduser("~/data/fma/fma_metadata"),
                   help="directory with tracks.csv and features.csv")
    p.add_argument("--subset", default="small", choices=SUBSETS)
    p.add_argument("--collection", default="tracks")
    p.add_argument("--qdrant-url", default="http://127.0.0.1:6333")
    p.add_argument("--output-dir", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "output"))
    p.add_argument("--components", type=int, default=64, help="PCA dimensions")
    p.add_argument("--scaler", default="standard", choices=SCALERS,
                   help="normaliser before PCA (standard won the comparison, see README)")
    p.add_argument("--limit", type=int, default=None, help="use only first N tracks (debug)")
    p.add_argument("--skip-qdrant", action="store_true", help="only write local files")
    return p.parse_args(argv)


def main(argv: Optional[List[str]] = None) -> None:
    args = parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    t0 = time.time()
    os.makedirs(args.output_dir, exist_ok=True)

    features, tracks = load_subset(args.data_dir, args.subset, args.limit)
    features = drop_non_finite(features)
    tracks = tracks.loc[features.index]

    pre, vectors = fit_preprocessor(
        features.to_numpy(dtype=np.float64), args.components, scaler=args.scaler
    )
    pre.save(os.path.join(args.output_dir, "preprocessor.joblib"))

    # genres/artists are used only to report quality, not for fitting
    scores = same_genre_at_k(
        vectors, tracks["track", "genre_top"].to_numpy(), tracks["artist", "name"].to_numpy()
    )
    log_scores(args.scaler, scores)

    log.info("running UMAP on %d vectors", len(vectors))
    coords = compute_umap(vectors)

    payloads = build_payloads(tracks, coords)
    write_points_json(payloads, os.path.join(args.output_dir, "points.json"))

    if not args.skip_qdrant:
        client = QdrantClient(url=args.qdrant_url)
        upload_to_qdrant(client, args.collection, vectors,
                         [int(i) for i in features.index], payloads)
    log.info("done in %.0fs", time.time() - t0)


if __name__ == "__main__":
    main()
