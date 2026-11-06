#!/usr/bin/env python3
"""Offline check of /api/v1/similar?rerank=true on the lab's SYNTHETIC events.

Events in ClickHouse are split by time: the first 80% build the transition
statistics (what the API would have known), the last 20% are held out. For a
set of source tracks the top-5 neighbours of the plain Qdrant order and of the
reranked order are compared by the share of skips among the held-out plays of
those source -> neighbour transitions.

The data is generated (lab-events/producer), so the result reflects the rules
built into the generator, not the behaviour of real listeners. Standard
library only; Python 3.9+.

    python3 eval_rerank.py                      # lab defaults
    python3 eval_rerank.py --sources 100 --json result.json
"""
import argparse
import json
import math
import os
import random
import statistics
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Dict, List, Optional, Tuple

# Same defaults as internal/analytics (RERANK_WEIGHT, RERANK_SMOOTHING).
WEIGHT = 0.3
SMOOTHING = 5.0
NO_DATA_KEEP = 0.5
TOP = 5

Stat = Tuple[int, int]  # (plays, skips)


def rerank(cands: List[dict], stats: Dict[int, Stat], w: float = WEIGHT, k: float = SMOOTHING) -> List[dict]:
    """Port of analytics.Rerank. cands: dicts with id and cosine, best first."""
    raws = [1 - min(s, p) / p for p, s in (stats.get(c["id"], (0, 0)) for c in cands) if p > 0]
    avg = sum(raws) / len(raws) if raws else NO_DATA_KEEP
    out = []
    for c in cands:
        plays, skips = stats.get(c["id"], (0, 0))
        if plays > 0:
            keeps = plays - min(skips, plays)
            keep = (keeps + k * avg) / (plays + k)
        else:
            keep = avg
        out.append(dict(c, keep=keep, plays=plays, score=(1 - w) * c["cosine"] + w * keep))
    # stable sort: score desc, then cosine desc, then id asc (as in Go)
    out.sort(key=lambda r: (-r["score"], -r["cosine"], r["id"]))
    return out


class ClickHouse:
    def __init__(self, url: str, user: str, password: str, database: str):
        self.url, self.user, self.password, self.database = url.rstrip("/"), user, password, database

    def rows(self, sql: str, **params) -> List[dict]:
        query = {"database": self.database}
        query.update({"param_" + k: str(v) for k, v in params.items()})
        req = urllib.request.Request(
            self.url + "/?" + urllib.parse.urlencode(query),
            data=(sql + "\nFORMAT JSONEachRow").encode(),
            headers={"X-ClickHouse-User": self.user, "X-ClickHouse-Key": self.password},
        )
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                return [json.loads(line) for line in resp.read().decode().splitlines() if line.strip()]
        except urllib.error.HTTPError as err:
            sys.exit("ClickHouse: HTTP %d: %s" % (err.code, err.read().decode()[:300]))
        except urllib.error.URLError as err:
            sys.exit("ClickHouse is not reachable at %s: %s (is lab-events up?)" % (self.url, err.reason))


class Qdrant:
    def __init__(self, url: str, collection: str):
        self.base = "%s/collections/%s" % (url.rstrip("/"), urllib.parse.quote(collection))

    def _post(self, path: str, body: dict) -> dict:
        req = urllib.request.Request(self.base + path, data=json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                return json.loads(resp.read())["result"]
        except urllib.error.URLError as err:
            sys.exit("Qdrant is not reachable at %s: %s (is the collection loaded?)" % (self.base, err))

    def payload(self, track_id: int) -> Optional[dict]:
        found = self._post("/points", {"ids": [track_id], "with_payload": True, "with_vector": False})
        return found[0]["payload"] if found else None

    def neighbours(self, track_id: int, n: int, exclude_artist: Optional[str]) -> List[dict]:
        """Top n neighbours, optionally without the source's artist (as in the API)."""
        limit = n if exclude_artist is None else min(max(n * 6, 30), 1000)
        points = self._post("/points/query", {
            "query": track_id, "limit": limit, "with_payload": True,
            "filter": {"must_not": [{"has_id": [track_id]}]},
        })["points"]
        out = [{"id": int(p["id"]), "cosine": p["score"], "genre": p["payload"]["genre"],
                "artist": p["payload"]["artist"]} for p in points]
        if exclude_artist is not None:
            out = [c for c in out if c["artist"] != exclude_artist]
        return out[:n]


def load_pairs(ch: ClickHouse, cut_ms: int, train: bool) -> Dict[Tuple[int, int], Stat]:
    """Plays and skips per (from, to) transition before (train) or after the cut."""
    cmp = "<" if train else ">="
    sql = ("SELECT prev_track_id AS from_id, track_id AS to_id, "
           "countIf(event_type IN ('complete', 'skip')) AS n_plays, countIf(event_type = 'skip') AS n_skips "
           "FROM events WHERE prev_track_id != '' AND toUnixTimestamp64Milli(ts) %s {cut:UInt64} "
           "GROUP BY from_id, to_id HAVING n_plays > 0" % cmp)
    return {(int(r["from_id"]), int(r["to_id"])): (int(r["n_plays"]), int(r["n_skips"]))
            for r in ch.rows(sql, cut=cut_ms)}


def skip_rate(pairs: List[Stat]) -> Optional[float]:
    plays = sum(p for p, _ in pairs)
    return sum(s for _, s in pairs) / plays if plays else None


def bootstrap_ci(diffs: List[float], rounds: int = 5000, seed: int = 1) -> Tuple[float, float]:
    rnd = random.Random(seed)
    means = sorted(statistics.fmean(rnd.choices(diffs, k=len(diffs))) for _ in range(rounds))
    return means[int(0.025 * rounds)], means[int(0.975 * rounds) - 1]


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--clickhouse", default="http://127.0.0.1:8123")
    ap.add_argument("--database", default="vibe")
    ap.add_argument("--qdrant", default="http://127.0.0.1:6333")
    ap.add_argument("--collection", default="tracks")
    ap.add_argument("--sources", type=int, default=200, help="how many source tracks to evaluate")
    ap.add_argument("--min-train", type=int, default=30, help="a source needs this many plays in the training part")
    ap.add_argument("--min-test", type=int, default=10, help="and this many in the held-out part")
    ap.add_argument("--train-share", type=float, default=0.8)
    ap.add_argument("--weight", type=float, default=WEIGHT)
    ap.add_argument("--smoothing", type=float, default=SMOOTHING)
    ap.add_argument("--exclude-same-artist", action="store_true")
    ap.add_argument("--json", help="also write the numbers to this file")
    args = ap.parse_args()
    if not 0 < args.train_share < 1:
        sys.exit("--train-share must be between 0 and 1")

    # Lab defaults match lab-events/docker-compose.yml; override via the environment.
    ch = ClickHouse(args.clickhouse, os.environ.get("CLICKHOUSE_USER", "vibe"),
                    os.environ.get("CLICKHOUSE_PASSWORD", "vibe"), args.database)
    qd = Qdrant(args.qdrant, args.collection)

    head = ch.rows("SELECT toUInt64(quantileExact({q:Float64})(toUnixTimestamp64Milli(ts))) AS cut_ms, "
                   "count() AS n FROM events", q=args.train_share)[0]
    cut_ms, total = int(head["cut_ms"]), int(head["n"])
    if total == 0:
        sys.exit("no events in ClickHouse: run the generator first (lab-events/README.md)")

    train, test = load_pairs(ch, cut_ms, True), load_pairs(ch, cut_ms, False)
    train_plays: Dict[int, int] = {}
    test_plays: Dict[int, int] = {}
    for (f, _), (p, _) in train.items():
        train_plays[f] = train_plays.get(f, 0) + p
    for (f, _), (p, _) in test.items():
        test_plays[f] = test_plays.get(f, 0) + p
    print("events: %d, split at the %.0f%% mark; transitions with plays: %d train, %d held out"
          % (total, 100 * args.train_share, len(train), len(test)))

    sources = sorted((f for f, p in train_plays.items() if p >= args.min_train and test_plays.get(f, 0) >= args.min_test),
                     key=lambda f: (-train_plays[f], f))[:args.sources]
    if not sources:
        sys.exit("no source track has >= %d training and >= %d held-out plays; generate more events"
                 % (args.min_train, args.min_test))
    print("sources: %d (>= %d train plays and >= %d held-out plays, most played first)"
          % (len(sources), args.min_train, args.min_test))

    plain_pairs: List[Stat] = []
    rer_pairs: List[Stat] = []
    per_source: List[Tuple[Optional[float], Optional[float]]] = []
    same_genre = {"plain": [], "rerank": []}  # type: Dict[str, List[float]]
    moved = []  # how many of the top-5 differ between the two variants
    covered = {"plain": 0, "rerank": 0}

    for src in sources:
        info = qd.payload(src)
        if info is None:
            continue
        exclude = info["artist"] if args.exclude_same_artist else None
        cands = qd.neighbours(src, max(TOP * 4, 20), exclude)
        stats = {t: v for (f, t), v in train.items() if f == src}
        top_plain = cands[:TOP]
        top_rer = rerank(cands, stats, args.weight, args.smoothing)[:TOP]

        results = []
        for name, top, pairs in (("plain", top_plain, plain_pairs), ("rerank", top_rer, rer_pairs)):
            held = [test[(src, c["id"])] for c in top if (src, c["id"]) in test]
            pairs.extend(held)
            covered[name] += len(held)
            same_genre[name].append(sum(c["genre"] == info["genre"] for c in top) / max(len(top), 1))
            results.append(skip_rate(held))
        per_source.append((results[0], results[1]))
        moved.append(len({c["id"] for c in top_plain} ^ {c["id"] for c in top_rer}) / 2)

    n_slots = len(sources) * TOP
    both = [(a, b) for a, b in per_source if a is not None and b is not None]
    diffs = [b - a for a, b in both]
    lo, hi = bootstrap_ci(diffs) if len(diffs) > 1 else (math.nan, math.nan)

    result = {
        "events": total, "train_share": args.train_share, "sources": len(sources),
        "weight": args.weight, "smoothing": args.smoothing, "exclude_same_artist": args.exclude_same_artist,
        "plain": {"skip_rate_pooled": skip_rate(plain_pairs), "pairs_with_held_out_plays": covered["plain"],
                  "held_out_plays": sum(p for p, _ in plain_pairs), "same_genre_share": statistics.fmean(same_genre["plain"])},
        "rerank": {"skip_rate_pooled": skip_rate(rer_pairs), "pairs_with_held_out_plays": covered["rerank"],
                   "held_out_plays": sum(p for p, _ in rer_pairs), "same_genre_share": statistics.fmean(same_genre["rerank"])},
        "sources_compared": len(both),
        "skip_rate_per_source": {"plain": statistics.fmean(a for a, _ in both) if both else None,
                                 "rerank": statistics.fmean(b for _, b in both) if both else None,
                                 "mean_difference": statistics.fmean(diffs) if diffs else None,
                                 "ci95": [lo, hi]},
        "mean_top5_slots_replaced": statistics.fmean(moved) if moved else 0.0,
    }

    def pct(x: Optional[float]) -> str:
        return "n/a" if x is None else "%.1f%%" % (100 * x)

    print("\n| top-%d, held-out events | rerank=false | rerank=true |" % TOP)
    print("|---|---|---|")
    print("| skip rate, pooled over all plays | %s | %s |" % (pct(result["plain"]["skip_rate_pooled"]), pct(result["rerank"]["skip_rate_pooled"])))
    print("| skip rate, mean over %d sources | %s | %s |" % (len(both), pct(result["skip_rate_per_source"]["plain"]), pct(result["skip_rate_per_source"]["rerank"])))
    print("| same genre as the source, share of the top-%d | %s | %s |" % (TOP, pct(result["plain"]["same_genre_share"]), pct(result["rerank"]["same_genre_share"])))
    print("| slots with held-out plays (of %d) | %d | %d |" % (n_slots, covered["plain"], covered["rerank"]))
    print("| held-out plays behind the skip rate | %d | %d |" % (result["plain"]["held_out_plays"], result["rerank"]["held_out_plays"]))
    if diffs:
        print("\nper-source difference (rerank - plain): %+.1f pp, 95%% bootstrap CI [%+.1f, %+.1f] pp"
              % (100 * statistics.fmean(diffs), 100 * lo, 100 * hi))
    print("on average %.1f of %d slots differ between the two lists" % (result["mean_top5_slots_replaced"], TOP))

    if args.json:
        with open(args.json, "w") as fh:
            json.dump(result, fh, indent=2)


if __name__ == "__main__":
    main()
