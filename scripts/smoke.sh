#!/usr/bin/env bash
# Manual smoke test for POST /compare against a running service.
#
# It sends real requests through to MusicBrainz, so it is slow (a cold pair
# takes a few seconds under the 1 req/s limit) and its numbers change as
# MusicBrainz data changes. It is not part of CI.
#
# Usage:
#   scripts/smoke.sh                      # service on http://localhost:8080
#   scripts/smoke.sh http://host:8080     # or set BASE_URL
#
# Requires curl and python3.
set -u

BASE_URL="${1:-${BASE_URL:-http://localhost:8080}}"

# artist A | title A | artist B | title B
PAIRS=(
  "Queen|Bohemian Rhapsody|Guns N Roses|Sweet Child O Mine"
  "Daft Punk|One More Time|Justice|D.A.N.C.E."
  "Beyonce|Halo|The Beatles|Yesterday"
  # A little-known artist: few or no recording tags, so this pair is the one
  # expected to fall back on artist tags and to report low_data.
  "Sibylle Baier|Tonight|Nick Drake|Pink Moon"
)

if ! curl -sf -m 5 "$BASE_URL/health" >/dev/null; then
  echo "service is not answering at $BASE_URL/health" >&2
  exit 1
fi

printf '%-46s %-6s %-11s %-14s %-12s %-12s %-8s %s\n' \
  "PAIR" "HTTP" "genre_score" "weighted_score" "tag_source_a" "tag_source_b" "low_data" "TIME"

failed=0
for pair in "${PAIRS[@]}"; do
  IFS='|' read -r artist_a title_a artist_b title_b <<<"$pair"

  body=$(python3 -c 'import json,sys; a,ta,b,tb=sys.argv[1:]; print(json.dumps({"song_a":{"artist":a,"title":ta},"song_b":{"artist":b,"title":tb}}))' \
    "$artist_a" "$title_a" "$artist_b" "$title_b")

  response=$(curl -s -m 120 -w '\n%{http_code} %{time_total}' -X POST "$BASE_URL/compare" \
    -H 'Content-Type: application/json' -d "$body") || { echo "request failed for: $pair" >&2; failed=1; continue; }

  label="$artist_a / $artist_b"
  python3 - "$label" "$response" <<'PY' || failed=1
import json, sys

label, raw = sys.argv[1], sys.argv[2]
body, _, tail = raw.rpartition("\n")
status, seconds = tail.split()
row = "%-46s %-6s" % (label[:46], status)

try:
    data = json.loads(body)
except ValueError:
    print(row, "unreadable response:", body[:80])
    sys.exit(1)

if status != "200":
    print(row, data.get("error", body[:80]))
    sys.exit(1)

print(row, "%-11.4f %-14.4f %-12s %-12s %-8s %.1fs" % (
    data["genre_score"], data["weighted_score"],
    data["tag_source_a"], data["tag_source_b"],
    str(data["low_data"]).lower(), float(seconds)))
PY
done

exit "$failed"
