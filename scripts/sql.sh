#!/usr/bin/env bash
# Run ad-hoc SQL through the federation engine (both catalogs available).
#   scripts/sql.sh "SELECT count(*) FROM orders_pg.public.orders"
#   scripts/sql.sh "EXPLAIN SELECT ... "
set -euo pipefail
curl -s -X POST --data "$1" "${API:-http://localhost:8080}/sql"
