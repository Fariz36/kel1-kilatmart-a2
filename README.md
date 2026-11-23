# KilatMart · A2 Query Federation

This is our project for the IF4052 Service Computing integration exercise. We picked Track A, option A2: query federation.

KilatMart's analytics team has been working from yesterday's data. Someone exports CSV files by hand every night, and the reports are built from those files. We wanted the team to get today's answers without copying anything. Orders live in the web shop's PostgreSQL database and stock lives in the inventory's MySQL database. A small Go service runs DuckDB inside it and reads both databases whenever someone asks a question. Nothing is exported, copied, or stored in between.

```
   orders-db (PostgreSQL)         stock-db (MySQL)
   web shop orders                warehouse stock
            \                         /
             \   read-only logins    /
              v                     v
         federation (Go + DuckDB, in memory)
                      |
                      v   HTTP: JSON, CSV or HTML
               Analytics team
```

## Running it

All you need is Docker with Compose. It uses about 1 GB of memory, and ports 8080, 15432 and 13306 must be free.

```bash
docker compose up --build
```

When all three containers show as healthy, open http://localhost:8080 in a browser. You can also use curl:

```bash
curl localhost:8080/reports/at-risk-orders               # JSON
curl "localhost:8080/reports/at-risk-orders?format=csv"  # CSV
curl localhost:8080/sources                              # is each database reachable?
```

The first build takes a while because it downloads the Go modules and the DuckDB extensions. They are saved into the image, so after that everything works offline.

## The demo

```bash
scripts/demo.sh    # goes through the demo one step at a time; press Enter to continue
scripts/reset.sh   # puts the original sample data back afterwards
```

The demo covers the three things the brief asks for:

1. **One query across both databases.** It lists paid orders whose product is running low in the warehouse.
2. **Changes show up right away.** We lower a product's stock in MySQL, then mark an order as paid in PostgreSQL. The next query reflects each change, and there's no sync step.
3. **What happens when a database goes down.** We stop the MySQL container and ask again. The service replies with HTTP 503 and says it was the stock database that failed. Once MySQL is back, the next query works again.

This is the query, from [`federation/queries/at-risk-orders.sql`](federation/queries/at-risk-orders.sql):

```sql
SELECT o.order_id, o.customer_name, o.sku, s.product_name,
       o.quantity AS ordered_qty, s.qty_on_hand, s.reorder_level, s.warehouse, o.ordered_at
FROM orders_pg.public.orders AS o          -- PostgreSQL
JOIN stock_my.stock          AS s          -- MySQL
  ON s.product_code = o.sku                -- the two systems name the product id differently
WHERE o.status = 'paid' AND s.qty_on_hand < s.reorder_level
ORDER BY o.ordered_at;
```

## Failure experiments

The experiments and their write-up are in [`experiments/`](experiments/README.md). Each experiment has one command:

```bash
experiments/run.sh source-down     # stop the stock database
experiments/run.sh source-slow     # freeze the orders database so it hangs
experiments/run.sh schema-change   # rename a column the query depends on
```

Each run saves its output to `experiments/results/`, so we have evidence for the slides.

## Endpoints

| Endpoint | What it does |
|---|---|
| `GET /` | A simple page listing the reports |
| `GET /reports` | The same list as JSON |
| `GET /reports/{name}` | Runs a report. Add `?format=csv` or `?format=html` for those formats; a browser gets HTML automatically. |
| `GET /sources` | Checks each database on its own |
| `POST /sql` | Runs your own read-only SQL across both databases (or use `scripts/sql.sh "..."`) |
| `GET /healthz` | Says whether the service is up; Docker uses it |

There are two reports. `at-risk-orders` answers the main question. `stock-coverage` checks whether the stock can cover the orders that are already paid. To add a report, put a `.sql` file in `federation/queries/` and rebuild. The comment at the top of the file becomes its description.

What the error codes mean:
- `503`: a database is down. The response names it in `failed_source`.
- `504`: a database took longer than the timeout to answer.
- `400`: the query itself is wrong.

Passwords are removed from all error messages.

## Settings

You set these on the `federation` service, for example `FEDERATION_MODE=persistent docker compose up -d federation`.

| Variable | Default | Meaning |
|---|---|---|
| `FEDERATION_MODE` | `fresh` | `fresh` connects to both databases again on every request. `persistent` connects once and keeps the connection. |
| `QUERY_TIMEOUT` | `10s` | The longest a request can take, even if a database hangs |
| `ORDERS_DSN`, `STOCK_DSN` | see `docker-compose.yml` | Connection details for the two databases, using the read-only `analytics_ro` logins |

If you want to look at the databases directly:
- PostgreSQL is on `localhost:15432`. Log in as `webshop` with password `webshop`; the database is `kilatmart`.
- MySQL is on `localhost:13306`. Log in as `inventory` with password `inventory`; the database is `kilatmart`.

## How this relates to the lecture

**Integration style.** This is query federation, also known as data virtualization. Instead of copying the data somewhere central, you query it where it already lives.

**The coupling we removed.** There are no more nightly CSV exports and no copies that slowly go out of date. Analytics doesn't have to wait for an export to run.

**The coupling we added.**
- Every report now needs both databases to be up at that moment.
- Analytics queries add load to the same databases that run the shop.
- The report queries depend on each database's column names, so a rename breaks them (see the schema-change experiment).

**The classical counterpart.** Data-virtualization products like Denodo do the same job. They join data from several systems without copying it, which is the work an ETL tool would otherwise do with a copy.

**Limits.**
- You only ever see the current state, with no history.
- A slow database makes every report slow.
- If one database is down, you get no answer at all.
- Joining across databases sends data over the network on every query.
- Differences in naming and types have to be handled in every query.

## What's in the repo

```
docker-compose.yml        starts everything
orders-db/init.sql        orders table, sample orders, read-only login
stock-db/init.sql         stock table, sample stock, read-only login
federation/               the Go service: main.go, queries/*.sql, Dockerfile
scripts/                  demo.sh, reset.sh, sql.sh
experiments/              the failure experiments and how to run them
```
