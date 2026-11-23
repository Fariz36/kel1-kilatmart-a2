#!/usr/bin/env bash
# Failure-experiment runner. Logs everything to experiments/results/<name>-<time>.log
#   experiments/run.sh source-down      # stop stock-db, query, start, query
#   experiments/run.sh source-slow      # pause orders-db (hangs instead of refusing)
#   experiments/run.sh schema-change    # rename a column the query depends on
# Compare the engine modes with:  FEDERATION_MODE=persistent docker compose up -d federation
set -uo pipefail
cd "$(dirname "$0")/.."
name=${1:?usage: experiments/run.sh source-down|source-slow|schema-change}
log="experiments/results/${name}-$(date +%Y%m%d-%H%M%S).log"
API=${API:-http://localhost:8080}

q() { echo "\$ GET /reports/at-risk-orders"; curl -s -w '\nHTTP %{http_code} after %{time_total}s\n' "$API/reports/at-risk-orders?format=csv"; }
wait_healthy() { until docker compose ps "$1" | grep -q healthy; do sleep 1; done; }

{
  echo "# experiment: $name   mode: $(docker compose exec -T federation printenv FEDERATION_MODE)   at: $(date -Is)"
  echo "## baseline"; q
  case "$name" in
    source-down)
      echo "## stop stock-db"; docker compose stop stock-db; q
      echo "## start stock-db"; docker compose start stock-db; wait_healthy stock-db; q ;;
    source-slow)
      echo "## pause orders-db (process frozen, TCP open)"; docker compose pause orders-db; q
      echo "## unpause orders-db"; docker compose unpause orders-db; q ;;
    schema-change)
      echo "## rename stock.qty_on_hand -> stock.quantity_available"
      docker compose exec -T stock-db mysql -uinventory -pinventory kilatmart \
        -e "ALTER TABLE stock RENAME COLUMN qty_on_hand TO quantity_available" 2>/dev/null; q
      echo "## rename it back"
      docker compose exec -T stock-db mysql -uinventory -pinventory kilatmart \
        -e "ALTER TABLE stock RENAME COLUMN quantity_available TO qty_on_hand" 2>/dev/null; q ;;
    *) echo "unknown experiment: $name"; exit 1 ;;
  esac
} 2>&1 | tee "$log"
echo; echo "saved: $log"
