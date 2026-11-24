# Failure experiments

The brief asks for one failure experiment: *predict the result, run it, record what happened* (slide 8). E1 is our main experiment; E2 and E3 are additional. The predictions below were written before the recorded runs. The results section comes from the logs in `results/`.

| # | Experiment | Command |
|---|---|---|
| E1 | A source is down: stop the stock database | `experiments/run.sh source-down` |
| E2 | A source hangs: freeze the orders database | `experiments/run.sh source-slow` |
| E3 | A source schema changes: rename `stock.qty_on_hand` | `experiments/run.sh schema-change` |

E1 and E3 are run in both engine modes:
- **`fresh`** (the default): a new DuckDB instance, with new attachments, for every request.
- **`persistent`**: one DuckDB instance, attached once and reused.

## Predictions

Written 2026-11-24, before the recorded runs.

### E1: stock database stopped

**`fresh` mode.**
- The query attaches PostgreSQL first, which succeeds, and then MySQL, which fails because the host no longer exists.
- Prediction: HTTP 503, with `failed_source` = stock (MySQL) and orders reported as up. There is **no partial result**: the join needs both inputs, and there is no copy to fall back on.
- Time to fail: under the 10 s query timeout, but not instant, because resolving the missing host name takes time.
- After the database is started again, the next query returns 200. No restart or resync of the federation service is needed, because every request attaches from scratch.

**`persistent` mode.**
- Normal queries are noticeably faster than in `fresh` mode, because the attach cost is paid only once.
- When the stock database stops, the query fails while reading from MySQL rather than while attaching. It is still a 503, but the service cannot name the failed source from the attach step, so it falls back to probing each source.
- After the restart, the service recovers without being restarted, because the extension opens a new MySQL connection.

### E2: orders database frozen (`docker compose pause`)

- A paused container keeps its network address but does not answer, so the connection neither succeeds nor is refused. It hangs.
- Prediction: the PostgreSQL connect timeout (3 s) fires first, giving HTTP 503 naming orders (PostgreSQL) after about 3 s. The 10 s query timeout (504) is not reached.
- After unpausing, the next query returns 200.

### E3: column `stock.qty_on_hand` renamed to `quantity_available`

**`fresh` mode.**
- Each request reads the MySQL schema again, so DuckDB sees the new column name when it checks the query.
- Prediction: HTTP 400 ("column not found"), not 503. From the service's point of view, a valid query has become invalid, so the error looks like the analyst's fault even though the cause is a change made by the inventory team.
- Nobody is warned in advance. The analytics team (the consumer) is the first to notice.
- After renaming the column back, the next query returns 200.

**`persistent` mode.**
- DuckDB may keep the MySQL schema it loaded at attach time, so it may not notice the rename when checking the query.
- Prediction: the query still fails, but the error comes from MySQL itself ("Unknown column") rather than from DuckDB.
- Possible risk: after renaming back, the stale schema could cause errors until the service restarts.

## Results

Recorded 2026-11-24 on one laptop (Docker Desktop on WSL 2), starting from freshly reset data. The query timeout was 10 s. Times are the client-side totals measured by `curl`.

| Run | Mode | Normal query | During the failure | After recovery | Log |
|---|---|---|---|---|---|
| E1 #1 | fresh | 200, 0.94 s | **504** timeout, 14.4 s | 200, 1.11 s | `source-down-20261124-143709.log` |
| E1 #2 | fresh | 200, 0.43 s | **503** naming stock (MySQL), 8.3 s | 200, 1.11 s | `source-down-20261124-143809.log` |
| E1 #3 | fresh | 200, 0.67 s | **504** timeout, 14.4 s | 200, 0.61 s | `source-down-20261124-143838.log` |
| E1 | persistent | 200, 0.60 s (first query, includes the attach) | **504** timeout, 15.0 s | 200, **0.04 s** | `source-down-20261124-143917.log` |
| E2 | fresh | 200, 0.47 s | **503** naming orders (PostgreSQL), 3.3 s | 200, 0.55 s | `source-slow-20261124-143751.log` |
| E3 | fresh | 200, 0.56 s | **400** DuckDB: `Table "s" does not have a column named "qty_on_hand"`, 0.47 s | 200, 0.56 s | `schema-change-20261124-143758.log` |
| E3 | persistent | 200, 0.02 s | **400** from MySQL: `Unknown column 'qty_on_hand'`, 0.02 s | 200, 0.02 s | `schema-change-20261124-143943.log` |

### Prediction versus result

**E1, stock database stopped.**
- **Confirmed:** there was never a partial result. Every run returned an error and no rows. The service recovered on the next query in both modes, without a restart or resync.
- **Not confirmed:** we predicted a 503 naming the stock database. That happened in only one of the three `fresh` runs. In the other two, the failing host-name lookup took longer than the 10 s query timeout, so the client received a 504 instead.
  - The lookup time sits close to the timeout, so the type of error the analytics team sees is not deterministic.
  - Each 504 response also arrived about 4 s after the timeout, because the service checks every source before answering. That extra check makes the failure slower to report.
- **`persistent` mode:** as predicted, normal queries were much faster once the attach was done (0.04 s against 0.4–1.1 s). The failure itself also ended as a 504 rather than a 503.

**E2, orders database frozen.** Confirmed exactly: HTTP 503 naming orders (PostgreSQL) after 3.3 s, from the 3 s connect timeout. It recovered immediately after unpausing.

**E3, column renamed.**
- **`fresh` mode, confirmed:** HTTP 400 from DuckDB's query check. The error presents a change in the source schema as if the analyst's query were wrong.
- **`persistent` mode, confirmed:** DuckDB kept its cached schema and sent a query naming the old column, and MySQL rejected it (`Unknown column`). The possible risk we noted, stale errors after renaming back, did **not** happen: the cached schema matched again.

**Unexpected finding: a filter was sent to MySQL.**
- The error text in the persistent E3 run includes the SQL that DuckDB sent to MySQL. It contains `WHERE product_code IN ('KM-003', 'KM-002', 'KM-004')`.
- So at run time DuckDB derives a filter from the join and sends it to MySQL, even though the static `EXPLAIN` plan shows no filter on that side.
- We confirmed this in `fresh` mode with MySQL's query log ([`evidence/mysql-received-query.txt`](evidence/mysql-received-query.txt)).
- MySQL therefore returns only the products that appear in paid orders, not the whole table. The comparison `qty_on_hand < reorder_level` is still evaluated in DuckDB.

### What this means

- Federation has no fallback when a source fails. The analytics team gets no answer at all until the source returns, which is the availability coupling discussed in `CONCEPTS.md` §5.2.
- When a host is unreachable, the time to fail depends on the environment (here, DNS resolution) and is close to our timeout. That makes the error code unpredictable (503 or 504) and the wait long (8–15 s).
- Schema changes are detected only by the consumer, at query time. They are reported as a client error (400).
- Recovery needs no intervention, because nothing is copied or replicated.

## Switching mode

```bash
FEDERATION_MODE=persistent docker compose up -d --wait federation   # attach once
docker compose up -d --wait federation                              # back to fresh (default)
```

Reset all data afterwards with `scripts/reset.sh`.
