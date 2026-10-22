"""Print nearest neighbours of tracks from the Qdrant collection (sanity check)."""
import argparse
import random
from typing import List, Optional

from qdrant_client import QdrantClient


def fmt(p) -> str:
    pl = p.payload
    return "%s - %s [%s] (id=%d)" % (pl["artist"], pl["title"], pl["genre"], pl["track_id"])


def main(argv: Optional[List[str]] = None) -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--qdrant-url", default="http://127.0.0.1:6333")
    ap.add_argument("--collection", default="tracks")
    ap.add_argument("--track-id", type=int, action="append", help="repeatable; default: random")
    ap.add_argument("--count", type=int, default=5, help="random tracks when no --track-id")
    ap.add_argument("--k", type=int, default=5)
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args(argv)

    client = QdrantClient(url=args.qdrant_url)
    ids = args.track_id
    if not ids:
        # one random track per genre for a more telling sample
        points, _ = client.scroll(args.collection, limit=10000, with_payload=True)
        by_genre = {}
        for p in points:
            by_genre.setdefault(p.payload["genre"], []).append(p.id)
        rng = random.Random(args.seed)
        genres = rng.sample(sorted(by_genre), min(args.count, len(by_genre)))
        ids = [rng.choice(by_genre[g]) for g in genres]

    for tid in ids:
        query = client.retrieve(args.collection, ids=[tid], with_payload=True)[0]
        print("\nQUERY  %s" % fmt(query))
        res = client.query_points(args.collection, query=tid, limit=args.k, with_payload=True)
        for p in res.points:
            print("  %.3f  %s" % (p.score, fmt(p)))


if __name__ == "__main__":
    main()
