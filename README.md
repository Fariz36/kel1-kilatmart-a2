# KilatMart · A2 Query Federation (DuckDB)

IF4052 Service Computing: hands-on integration exercise, **Track A, option A2: query federation**.

KilatMart's analytics team used to report on *yesterday's* data, built from manual CSV exports.
This project answers their questions **live, across two databases, without copying any data**:
web shop orders are in **PostgreSQL**, warehouse stock is in **MySQL**, and a small **Go**
service with an embedded **DuckDB** engine joins them at query time.

```
 ┌──────────────────────┐          ┌──────────────────────┐
 │ orders-db            │          │ stock-db             │
 │ PostgreSQL 16        │          │ MySQL 8.4            │
 │ orders (web shop)    │          │ stock (inventory)    │
 └──────────┬───────────┘          └───────────┬──────────┘
            │ postgres extension                │ mysql extension
            │ (read-only user)                  │ (read-only user)
            └───────────────┬───────────────────┘
                 ┌──────────▼───────────┐
                 │ federation (Go)      │  embedded DuckDB, in memory
                 │ ATTACH both, READ_ONLY│  nothing is stored or copied
                 └──────────┬───────────┘
                            ▼  HTTP: JSON · CSV · HTML
                     Analytics team
```

## Quick start

Requirements: Docker with Compose v2. About 1 GB of free RAM. Ports 8080, 15432 and 13306 must be free.

```bash
docker compose up --build
```

When all three services are healthy, open **http://localhost:8080**.

```bash
curl localhost:8080/reports/at-risk-orders              # JSON
curl "localhost:8080/reports/at-risk-orders?format=csv" # CSV
curl localhost:8080/sources                             # is each source reachable?
```

The first build downloads Go modules and the DuckDB extensions. After that, everything runs
**offline**, because the extensions are baked into the image.

## Live demo (must-show items)

```bash
scripts/demo.sh      # step by step, press Enter between steps
scripts/reset.sh     # back to the seed data afterwards
```

| Must show (slide 6) | Demo step |
|---|---|
| One SQL query joining both databases: paid orders whose item is low on stock | step 2: `GET /reports/at-risk-orders` |
| Change a source row; the next query sees it | step 3: update stock in MySQL, then an order in PostgreSQL |
| Stop one source and show the error | steps 4–5: `docker compose stop stock-db` → HTTP 503 naming the failed source; start it again and the next query works |

The federated query, from [`federation/queries/at-risk-orders.sql`](federation/queries/at-risk-orders.sql):

```sql
SELECT o.order_id, o.customer_name, o.sku, s.product_name,
       o.quantity AS ordered_qty, s.qty_on_hand, s.reorder_level, s.warehouse, o.ordered_at
FROM orders_pg.public.orders AS o          -- PostgreSQL
JOIN stock_my.stock          AS s          -- MySQL
  ON s.product_code = o.sku                -- different column names in each system
WHERE o.status = 'paid' AND s.qty_on_hand < s.reorder_level
ORDER BY o.ordered_at;
```

## Failure experiment

See [`experiments/README.md`](experiments/README.md). `experiments/run.sh source-down | source-slow | schema-change`
runs an experiment and saves a log. You can run each one in `fresh` and `persistent` engine mode.

## API

| Endpoint | What it does |
|---|---|
| `GET /` | Report list (HTML) |
| `GET /reports` | Report list (JSON) |
| `GET /reports/{name}` | Runs a report. `?format=json\|csv\|html` (a browser gets HTML) |
| `GET /sources` | Checks each source on its own: `up` / `down: …` |
| `POST /sql` | Ad-hoc read-only SQL across both catalogs (`scripts/sql.sh "..."`) |
| `GET /healthz` | Liveness, used by the compose healthcheck |

**Reports:** `at-risk-orders` (the main question) and `stock-coverage` (can stock cover paid
orders?). To add a report, drop a `.sql` file into `federation/queries/` and rebuild. The leading
`--` comment becomes its description.

**Errors:** `503` names the source that is down (`failed_source`) and gives the status of each source.
`504` means a source hung longer than `QUERY_TIMEOUT`. `400` means the SQL itself is wrong.
Passwords are removed from error messages.

## Configuration (environment of `federation`)

| Variable | Default | Meaning |
|---|---|---|
| `FEDERATION_MODE` | `fresh` | `fresh`: new DuckDB and new ATTACH per request (nothing kept between requests). `persistent`: attach once and reuse. |
| `QUERY_TIMEOUT` | `10s` | Upper bound per request, even if a source hangs |
| `ORDERS_DSN` / `STOCK_DSN` | see compose | libpq-style connection strings, using the read-only `analytics_ro` users |

Set them from the shell: `FEDERATION_MODE=persistent docker compose up -d federation`.

For manual access: PostgreSQL is on `localhost:15432` (`webshop`/`webshop`, db `kilatmart`) and
MySQL on `localhost:13306` (`inventory`/`inventory`, db `kilatmart`).

## Design decisions

**Assumed requirements.** We read these from the brief:

1. Data stays where it is: no copy, no export, no ETL schedule.
2. Answers are fresh: a change in a source is visible on the next query.
3. One query spans two *different* database systems (PostgreSQL and MySQL), each with its own naming (`sku` vs `product_code`).
4. Analysts write plain SQL.
5. Read-only: analytics must never write to operational databases.
6. Runs on one laptop with `docker compose up`, built within one week.

**Why DuckDB rather than Trino.** Both meet requirements 1–5. DuckDB is a library (in-process,
about 100 MB of RAM), while Trino is a distributed server (a coordinator plus workers, about 4 GB of RAM).
KilatMart has one analytics team, one main question and small data, so a server built for many users
and big data adds cost without benefit. We would choose **Trino** if many analysts or BI tools needed
to query at the same time, if sources multiplied, or if the data outgrew one machine.

**Why a Go service around DuckDB.** DuckDB is embedded, not a server, so something has to host it in
a container and deliver results to the analytics team. The Go service does that. It turns
the federated query into an HTTP service with clear error semantics, a timeout and read-only safety.

**Defence in depth for "read-only".**
- Both sources are attached `READ_ONLY`, and the database logins only have `SELECT` rights.
- During development, DuckDB passed a `DELETE` through to MySQL despite the `READ_ONLY` attach, and
  MySQL's grants rejected it. So the database user's rights are the real guard.
- Ad-hoc SQL also cannot read local files (`disabled_filesystems`) or change settings (`lock_configuration`).

## Link to the lecture

- **Integration style:** query federation, also called data virtualization (EII). It is the "query instead of copy" side of the copy-versus-query trade-off.
- **Coupling removed:** no nightly CSV exports and no duplicated copies to keep in sync. Analytics is no longer tied to an export schedule or a copy that is out of date.
- **Coupling added:** *runtime and availability coupling*. Every report needs **both** source databases to be up, and analytical load lands on the operational databases. The analytics query is also tied to each source's schema (see experiment E3).
- **Classical counterpart:** EII and data-virtualization products (e.g. Denodo). Their virtual views take over the "join data from several systems" job that an ETL tool would do with a copy.
- **Limits:**
  - No history: you only see the current state.
  - The slowest source sets the response time.
  - No result if a source is down.
  - Cross-database joins move data over the network at query time.
  - Type and naming differences must be handled in every query.

## Repository layout

```
docker-compose.yml          the whole setup (pinned image versions)
orders-db/init.sql          PostgreSQL schema + seed orders + read-only user
stock-db/init.sql           MySQL schema + seed stock + read-only user
federation/                 Go service: main.go, queries/*.sql, Dockerfile
scripts/demo.sh             live demo, step by step
scripts/reset.sh            reset to seed data
scripts/sql.sh              ad-hoc SQL via the federation engine
experiments/                failure experiments: runner + prediction table
```
