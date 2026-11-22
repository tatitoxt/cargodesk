# Benchmarks

Measured through the live HTTP API against real PostgreSQL — every op is an
authenticated request hitting the same handlers, validation, workflow rules,
optimistic locking and outbox writes the React app uses. Benchmarks live in
`api/internal/httpapi/bench_integration_test.go`.

**How to reproduce**

```sh
CARGODESK_TEST_DATABASE_URL=postgres://cargodesk:cargodesk@host:55434/cargodesk \
go test -count=1 -p 1 -run '^$' -bench . -benchtime=100x ./api/internal/httpapi/
```

**Results (2026-11-21, 100 iterations, local Postgres 17)**

| Metric | Value |
|---|---:|
| Full shipment lifecycle (create + quote → accept → schedule → transit → deliver) | **11.2 ms** per shipment |
| Sustained lifecycle throughput | ~89 shipments/sec end-to-end |
| Per-request writes inside the lifecycle | ~530 writes/sec (6 ops/shipment) |
| Filtered board list (`?status=…&limit=50`) | 1.8 ms median (~560 req/sec) |

**Concurrent load (2026-11-22, `-bench Concurrent -benchtime 400x`, 8-way parallelism)**

| Metric | Value |
|---|---:|
| Full lifecycle under concurrency | 4.1 ms per shipment |
| Aggregate lifecycle throughput | **~176 shipments/sec** end-to-end |
| Aggregate HTTP ops | ~1,050 requests/sec (6 ops/shipment) |
| Errors / optimistic-lock failures | **0** across 2,400 requests |

`BenchmarkConcurrentLifecycle` gives each parallel worker its own session and
runs the full create→deliver workflow; a failure counter records any
unexpected status or version conflict, so a clean run proves correctness
under concurrency, not just speed.

**Reading the numbers**

- The lifecycle op is intentionally heavy: six HTTP requests, each doing its
  own auth, RBAC check, version-checked UPDATE, audit-event insert and
  (when webhooks are configured) a transactional outbox row.
- Numbers are machine-dependent; they are a reproducible baseline, not a
  marketing figure. Concurrency numbers used GOMAXPROCS=8 parallelism.
