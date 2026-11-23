#!/usr/bin/env bash
# Throw away all data and start again from the seed scripts.
set -euo pipefail
cd "$(dirname "$0")/.."
docker compose down -v
docker compose up -d --build --wait
echo "Reset done: http://localhost:8080"
