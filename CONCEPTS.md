# Stop Exporting CSVs: What Query Federation Taught Us About Integration

*How we gave KilatMart's analytics team live answers across two databases, and what that cost.*

---

Every morning, KilatMart's analytics team opened a spreadsheet made from yesterday's data. Someone on the web shop team exported the orders table to CSV. Someone on the inventory team did the same with stock. An analyst joined the two by hand. By the time anyone asked *"which paid orders are we about to fail to ship?"*, the answer was already a day old.

For our IF4052 integration exercise we picked option A2, **query federation**. The idea is simple to say and surprisingly interesting to build. Leave the data where it is, and ask both databases at the same moment.

This post walks through what we built and how it connects to what we learned in class. It also covers what we gave up to get there, which turned out to be the most interesting part.

## What we built, in one paragraph

Orders live in the web shop's **PostgreSQL** database. Stock lives in the inventory service's **MySQL** database. We wrote a small Go service that embeds **DuckDB**. On each request it attaches both databases read-only and runs a single SQL query that joins them. The answer goes straight to the analytics team as JSON, CSV or an HTML table. Nothing is exported, copied or stored in between.

```sql
SELECT o.order_id, o.customer_name, s.product_name, o.quantity, s.qty_on_hand
FROM orders_pg.public.orders AS o          -- PostgreSQL
JOIN stock_my.stock          AS s          -- MySQL
  ON s.product_code = o.sku
WHERE o.status = 'paid' AND s.qty_on_hand < s.reorder_level;
```

When we lower a product's stock in MySQL, the very next query shows it. When we stop MySQL, the analytics team gets a clear error telling them it's the stock database that's gone.

## Where this sits in the course's map of integration

The lecture on SOA integration (W09) gives a neat taxonomy of integration solutions (p. 10). It splits them into three families:

- **Data and Information Integration:** ETL, EII – federated databases
- **Application Integration:** EAI, B2B Gateway, ESB, API Gateway
- **Process Integration:** BPM, B2Bi

Our project lives in the first family. More precisely, it's **EII, Enterprise Information Integration**. The slide on EII (W09 p. 12) defines it in one line that could be the summary of our repository:

> **EII = data federation = virtual database + distributed queries**

DuckDB is our virtual database: it holds no data of its own, only connections. The join across PostgreSQL and MySQL is our distributed query. In terms of the integration layers (W09 p. 5), we work at the **Data Integration Layer**. We never touch the web shop's or inventory's application logic, only their data.

It's worth saying what we are *not*. An ESB, in this course, is application integration. It's "core SOA infrastructure for service-to-service communication, mediation" (W09 p. 14), and it deals in messages, routing and protocol translation. Our service routes no messages between services. It answers questions about data.

## The job we took over from ETL

The classical way to solve KilatMart's problem is **ETL**, and the course describes it plainly (W09 p. 11):

1. Extract data from one or more sources
2. Transform and integrate it (syntax, semantics) to the target's requirements
3. Load the result into one or more target data repositories

Typical use: a data warehouse for business intelligence. KilatMart's nightly CSV routine is really a manual, fragile ETL job.

Our federation service does the first two steps of ETL. It extracts from both sources and transforms and integrates them on the fly. That includes *data mappings*: the web shop calls the product id `sku`, while the inventory calls it `product_code`, and the join reconciles the two. What we drop is the **L**: there is no target repository, and nothing is loaded anywhere.

The line on the ETL slide we enjoyed most is this one:

> Not: on-demand, real-time, low-latency

That's exactly the gap federation fills. The EII slide says the opposite about itself: *"Provides access, data not moved"*, with "real-time" access for "query and reporting tools". Our analytics team is a query-and-reporting user, and real-time is what they lacked.

If we had built this twenty years ago, the classical EII products named in the lecture would have been our tools: IBM WebSphere Information Integrator, Composite Software, WSO2 Data Service (W09 p. 12). DuckDB does the same job as an embedded library rather than a product suite.

## The coupling we removed, and the coupling we added

This part made us think hardest, because the honest answer isn't all good news.

**What we removed:** the nightly hand-off. The analytics team no longer waits on two other teams to run an export. There are no CSV files to go stale and no copy of the data to keep in sync. If the inventory team fixes a stock count at 10:03, the 10:04 report shows it.

**What we added** is easiest to explain with the SOA design principles from W05 (Erl's principles, on the W05b slides and the poster). Query federation, applied inside a service-oriented company, quietly bends several of them:

- **Standardized Service Contract and Service Abstraction.**
  - The principles say that information about a service should be "limited to what is published in service contracts" (poster). Its "underlying logic… is invisible and irrelevant to service requestors" (W05b p. 16).
  - We ignore the web shop's and inventory's contracts completely. We go straight past their APIs and read their private tables.
  - The poster's *Contract Centralization* pattern says the contract should be "the sole means of accessing service logic and resources". We built the opposite.

- **Service Loose Coupling.**
  - The poster separates positive coupling (the contract shapes the logic) from *negative* coupling. In negative coupling, consumers end up depending on how a service happens to be implemented.
  - Our report SQL depends on the inventory's column names, so that's negative coupling. The poster warns where this leads: "a fragile and inflexible service inventory reminiscent of past integration architectures."
  - We saw it ourselves. When we renamed `qty_on_hand` in MySQL, the report broke immediately. The inventory team had no idea anyone depended on that column, and the analytics team found out first. Contrast that with an API contract, where the rename would be the inventory team's problem to manage.

- **Service Autonomy.**
  - The poster says that "reducing shared access to service resources and increasing physical isolation can raise a service's ability to function autonomously."
  - Federation *increases* shared access. Every analytics query now runs against the same databases that take customer orders.

There's also a plainer kind of coupling the slides don't name but our experiments made obvious: **both databases must be up at the same moment**. With CSV exports, a database being down at 2 pm didn't matter as long as it was up for the nightly export. With federation, one database down means no answer at all, not even a partial one.

None of this makes federation wrong. It makes it a trade-off. For a small analytics need, reading two databases directly is far cheaper than asking two teams to build and maintain a reporting API. But we'd want to name the trade-off out loud before a real company adopted it.

## Where federation hurts: the limits

The EII slide lists its own issues (W09 p. 12), and each one turned up in our project:

**"Latency when multiple sources involved."** The slowest source sets the response time for everything.
- In our default mode the service reconnects to both databases on every request, which costs roughly 400–800 ms per query.
- When MySQL was stopped, the query took about 8 seconds to fail, mostly Docker's DNS giving up on the missing host.
- When PostgreSQL was frozen rather than stopped, it failed after the 3-second connection timeout we set.

**"Meta-data management."** This is the schema-change story above. The federation layer knows the source schemas only as well as whoever wrote the query.

**"Data not moved, small return sets."** This one surprised us. We ran `EXPLAIN` on our main query; the full plan is in [`experiments/evidence/explain-at-risk-orders.txt`](experiments/evidence/explain-at-risk-orders.txt).
- DuckDB pushes the `status = 'paid'` filter down to PostgreSQL, so only paid orders cross the network.
- It does **not** push the `qty_on_hand < reorder_level` filter down to MySQL. The entire stock table gets pulled over, filtered, and joined inside DuckDB.
- With four products nobody notices. With four million, every report would drag a large table across the network.

**"Caching for optimization."** The slide suggests caching to fight latency. We tried a halfway version: a *persistent* mode that keeps the database connections open between requests. Queries dropped to about 30 ms. We deliberately cache no *data*, because the moment you cache results you're back to serving stale answers, which is the problem we set out to solve.

One more limit comes from the lecture's list of data integration problems (W09 p. 7), which ends with: *"The hardest problems are not syntactic/structural, but rather semantic!!"*
- In our small world that already shows up twice. The two systems use different names for the same product id (`sku` vs `product_code`) and different timestamp types.
- A real company would have dozens of these, and federation makes you solve them again in every query.

Finally, one deliberate difference from the textbook EII. The slide promises "real-time read & write access". We made ours **read-only on purpose**: the analytics team should never be able to change operational data.
- Both databases are attached as read-only, and the logins only have `SELECT` rights.
- That second layer turned out to matter. In testing, DuckDB passed a `DELETE` through to MySQL despite the read-only attach, and only MySQL's permission check stopped it.

## Copy or query?

The deeper question behind all of this is whether you should copy data to where it's needed, or query it where it lives. The two lecture slides on ETL and EII (W09 pp. 11–12) make the contrast for us:

| | ETL (copy) | EII / federation (query) |
|---|---|---|
| Data | moved into a repository | not moved |
| Freshness | as of the last load | real-time |
| Good for | large volumes, warehouses, BI | applications, query and reporting tools, small results |
| Main issues | data quality, performance | latency across sources, metadata management |
| If a source is down | the last copy still works | no answer |
| History | yes, if you keep loads | no, only the current state |

For KilatMart's question, "which paid orders are at risk *right now*", federation is the right call. The data is small, freshness is the whole point, and a short outage is acceptable. If the analytics team later wanted trends over months, or reports that keep working while the inventory database is down for maintenance, we would move toward copying (ETL or ELT into an analytical store), and federation would stay for the live, operational questions.

The same goes for the engine itself. DuckDB runs inside one program, which suits one team asking a handful of questions. If KilatMart grew to many analysts and BI tools querying at once, or many more sources, a federation *server* like Trino would be the natural next step. The idea would be the same, just a different tool.

## What we'd tell the next group

Query federation feels almost too easy at first: two `ATTACH` statements and a join, and the analytics team has live data. The real lessons were in the failure experiments:
- What happens when a source disappears?
- Who finds out when a schema changes?
- What does the query plan actually send over the network?

That's where the trade-offs we read about in W05 and W09 stopped being bullet points and became something we could see in a log file.

---

### Slides referenced

| Slide | What we used it for |
|---|---|
| W09 p. 5, Integration Layers | We sit at the Data Integration Layer |
| W09 p. 7, data integration problems | "The hardest problems are… semantic" |
| W09 p. 10, Integration Solution Taxonomy | Data and Information Integration: ETL, EII – federated databases |
| W09 p. 11, ETL | The classical counterpart; "Not: on-demand, real-time, low-latency" |
| W09 p. 12, EII | "EII = data federation = virtual database + distributed queries", its issues and vendors |
| W09 pp. 14–15, ESB | Why an ESB is application integration, not our category |
| W05b pp. 11–18 and the W05a poster | Standardized Service Contract, Abstraction, Loose Coupling, Autonomy; negative coupling; Contract Centralization |

*Our measurements (timings, failure behaviour, the `EXPLAIN` plan) come from our own runs during development on a single laptop with Docker; numbers will vary by machine.*
