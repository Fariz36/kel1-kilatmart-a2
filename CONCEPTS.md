# Query Federation for KilatMart: Relation to Course Concepts

IF4052 Service Computing · Hands-on Integration Exercise · Option A2

## 1. Problem statement

KilatMart's analytics team produces reports from manual CSV exports of two operational databases:
- the web shop's order data, stored in PostgreSQL;
- the inventory service's stock data, stored in MySQL.

Because of this, reports reflect the previous day's state. Producing them also depends on two other teams carrying out the export.

The question we address is one that needs data from both databases:

*Which paid orders refer to products whose stock is below the reorder level?*

## 2. System overview

The solution has one component, a federation service written in Go with an embedded DuckDB engine. For each request the service does three things:
1. attaches both source databases in read-only mode, through DuckDB's `postgres` and `mysql` extensions;
2. runs a single SQL query that joins the two databases;
3. returns the result over HTTP.

No data is copied or kept between requests. The join condition `s.product_code = o.sku` reconciles the different names the two systems use for the product identifier.

## 3. Classification of the integration style

The course groups integration solutions into three families [W09, p. 10]:

| Family | Solutions listed |
|---|---|
| Data and Information Integration | ETL, EII – federated databases |
| Application Integration | EAI, B2B Gateway, ESB, API Gateway |
| Process Integration | BPM, B2Bi |

This project belongs to the first family. Specifically, it is **Enterprise Information Integration (EII)**, which the course defines as "data federation = virtual database + distributed queries" [W09, p. 12]:
- DuckDB acts as the virtual database. It holds no data of its own, only the attached sources.
- The cross-database join is the distributed query.

In terms of integration layers [W09, p. 5], the project works at the Data Integration Layer. It never calls the application logic of the web shop or inventory services.

The project is not an ESB. The course places the ESB under application integration and describes it as infrastructure for "service-to-service communication, mediation" [W09, pp. 14–15]. No messages are routed between services here.

## 4. Classical counterpart: ETL

The classical solution to this problem is ETL [W09, p. 11]:
1. extract data from one or more sources;
2. transform and integrate it (syntax and semantics) to the target's requirements;
3. load the result into a target repository, typically a data warehouse.

The existing CSV process is in effect a manual ETL job.

The federation service performs steps 1 and 2 at query time, including the data mapping between `sku` and `product_code`. It omits step 3: there is no target repository.

The course gives the following contrast between the two approaches:

| | ETL [W09, p. 11] | EII [W09, p. 12] |
|---|---|---|
| Data movement | "Large volumes of data moved from source" | "data not moved, small return sets" |
| Timeliness | "Not: on-demand, real-time, low-latency" | real-time access |
| Typical use | Data warehouse, BI | Applications, query and reporting tools |
| Stated issues | Data quality, performance | Latency across sources, metadata management |

The analytics team is a reporting user that needs current data, so the requirement matches the EII profile.

The classical products for this role, as listed in the course, are IBM WebSphere Information Integrator, Composite Software and WSO2 Data Service [W09, p. 12].

## 5. Coupling analysis

### 5.1 Coupling removed

The federation service removes the dependency on the export schedule and on the teams that perform the export:
- There is no intermediate copy, so there is no inconsistency between a copy and its source.
- Changes in either source are visible on the next query. We verified this for updates in both PostgreSQL and MySQL.

### 5.2 Coupling introduced

We evaluate the design against the SOA design principles covered in the course [W05b, pp. 11–18; W05a].

**Standardized Service Contract and Service Abstraction.**
- The course states that information about a service should be "limited to what is published in service contracts" [W05a]. It adds that the underlying logic should be "invisible and irrelevant to service requestors" [W05b, p. 16].
- The federation service bypasses both services' contracts and reads their internal tables directly. This contradicts the Contract Centralization pattern, under which the contract is "the sole means of accessing service logic and resources" [W05a].

**Service Loose Coupling.**
- The reports depend on the internal schemas of both services. The course classifies this as negative coupling and warns that it "leads to a fragile and inflexible service inventory" [W05a].
- Experiment E3 confirms it. Renaming `stock.qty_on_hand` in MySQL caused the report to fail immediately (HTTP 400), and the change was detected by the consumer, not by the service that owns the data.

**Service Autonomy.**
- The course associates autonomy with "reducing shared access to service resources" [W05a].
- Federation increases shared access: analytical queries run against the same databases that serve operational traffic.

**Availability dependency.**
- The course does not list this as a principle, but it follows directly from the design. Each query needs every source to be reachable at query time.
- In experiment E1, stopping the stock database made the service return no result at all in every run (HTTP 503, or 504 when the failure exceeded the timeout), never a partial one.

### 5.3 Assessment

For a small, read-only analytical need, direct access is cheaper than asking both services to provide and maintain reporting interfaces. The cost is the coupling described in Section 5.2. For KilatMart we consider this acceptable at the current scale. It would need to be revisited if the number of consumers or sources grows.

## 6. Limitations

The issues the course lists for EII [W09, p. 12] were each observed in this project. Measurements were taken on a single development laptop and are indicative only.

| Stated issue | Observation in this project |
|---|---|
| Latency when multiple sources are involved | Attaching both sources on every request costs about 0.4–1.1 s per query. An unreachable source delays failure: 8–15 s for a stopped container, where host-name resolution competes with the 10 s query timeout, so the client receives either a 503 or a 504 (E1). A frozen container fails after about 3 s, from the connection timeout (E2). |
| Metadata management | Schema changes in a source are not visible to the federation layer until a query fails (E3). |
| Small return sets | The predicate `status = 'paid'` is pushed down to PostgreSQL ([`explain-at-risk-orders.txt`](experiments/evidence/explain-at-risk-orders.txt)). The static plan shows no filter on MySQL. At run time, however, DuckDB sends the product codes of the paid orders to MySQL as an `IN` list ([`mysql-received-query.txt`](experiments/evidence/mysql-received-query.txt)). The column comparison `qty_on_hand < reorder_level` and the join are still evaluated inside DuckDB. The amount of data moved therefore grows with the number of paid orders and the products they reference. |
| Caching for optimization | A persistent mode, which reuses connections without caching any data, reduces query time to about 20–40 ms. Caching results was deliberately not implemented, because it would reintroduce stale data. |

In addition, the course notes that semantic conflicts are the hardest problems in data integration [W09, p. 7]. Two such conflicts occur even in this small setting:
- different names for the product identifier (`sku` versus `product_code`);
- different temporal types (`TIMESTAMPTZ` in PostgreSQL versus `DATETIME` in MySQL).

Federation requires these conflicts to be resolved in every query.

Finally, the course describes EII as providing read and write access [W09, p. 12]. This project is restricted to read access by design:
- Both sources are attached `READ_ONLY`, and the database accounts have `SELECT` privileges only.
- The second safeguard proved necessary. During testing, a `DELETE` statement was passed through to MySQL despite the read-only attachment, and it was rejected only by the account's privileges.

## 7. Discussion: copy versus query

The choice between federation and ETL is a trade-off between timeliness and independence from the sources.
- Federation gives current data, but it depends on source availability and adds load to the operational systems.
- ETL tolerates source outages and keeps history, but its data is only as current as the last load.

For KilatMart's question, which concerns the current state and small data volumes, federation is appropriate. Requirements for historical trends, or for reporting during source maintenance, would favour an ETL approach, with federation kept for operational queries.

The choice of engine follows the same reasoning. DuckDB runs in-process, which is sufficient for one consumer. A federation server such as Trino would be preferable for many concurrent users or a larger number of sources.

## 8. Conclusion

The project implements EII-style data federation, which the course classifies under Data and Information Integration. It performs the extract and transform functions of ETL on demand, without materialising the data. It removes the dependency on scheduled exports. In exchange, it introduces schema and availability coupling to the source services, contrary to the principles of service abstraction, loose coupling and autonomy. The limitations observed in the experiments match the issues the course lists for EII.

## References

- [W05a] SOA Design Principles poster (Erl): Standardized Service Contract, Service Loose Coupling, Service Abstraction, Service Autonomy; Contract Centralization.
- [W05b] SOA Design, pp. 11–18: SOA common principles.
- [W09] SOA Integration:
  - p. 5: integration layers
  - p. 7: data integration problems
  - p. 10: integration solution taxonomy
  - p. 11: ETL
  - p. 12: EII
  - pp. 14–15: ESB
