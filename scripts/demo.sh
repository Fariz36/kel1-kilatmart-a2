#!/usr/bin/env bash
# Live demo for IF4052 option A2 (query federation). Press Enter between steps.
# Prerequisite: `docker compose up -d --build` and all three services healthy.
set -euo pipefail
cd "$(dirname "$0")/.."

API=${API:-http://localhost:8080}
step()   { echo; echo "=== $* ==="; read -rp "(Enter) " _; }
report() { curl -s "$API/reports/at-risk-orders?format=csv" | column -s, -t; }
orders() { docker compose exec -T orders-db psql -U webshop -d kilatmart -c "$1"; }
stock()  { docker compose exec -T stock-db mysql -uinventory -pinventory kilatmart -e "$1" 2>/dev/null; }

step "1. The two sources: orders live in PostgreSQL, stock lives in MySQL"
orders "SELECT order_id, customer_name, sku, quantity, status FROM orders ORDER BY order_id;"
stock  "SELECT product_code, product_name, qty_on_hand, reorder_level FROM stock;"

step "2. ONE federated query across both: paid orders whose product is low on stock"
report

step "3a. Change a source row in MySQL: KM-003 stock drops from 25 to 4"
stock "UPDATE stock SET qty_on_hand = 4 WHERE product_code = 'KM-003';"
report

step "3b. Change a source row in PostgreSQL: order 7 (KM-002) gets paid"
orders "UPDATE orders SET status = 'paid' WHERE order_id = 7;"
report

step "4. Stop the stock database and ask again"
docker compose stop stock-db
curl -s -w '\nHTTP %{http_code} after %{time_total}s\n' "$API/reports/at-risk-orders"

step "5. Start it again: the next query works, no resync needed"
docker compose start stock-db
until docker compose ps stock-db | grep -q healthy; do sleep 1; done
report

echo; echo "Demo done. Reset the data with: scripts/reset.sh"
