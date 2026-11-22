# Benchmarks

Measured through the live HTTP API against real PostgreSQL — every op is an
authenticated request hitting the same handlers, validation, workflow rules,
optimistic locking and outbox writes the React app uses. Benchmarks live in
`api/internal/httpapi/bench_integration_test.go`.

**How to reproduce**

```sh
export CARGODESK_TEST_DATABASE_URL=postgres://cargodesk:cargodesk@localhost:5432/cargodesk_test?sslmode=disable
go test -count=1 -p 1 -run '^$' -bench . -benchtime=100x ./api/internal/httpapi/
go test -count=1 -p 1 -run '^$' -bench Concurrent -benchtime=400x ./api/internal/httpapi/
```

**Results (2026-11-22, 8-vCPU Intel Xeon cloud VM, PostgreSQL 17 in Docker on the same host)**

`ns/op` from `go test -bench` is a mean over the iterations, not a median.

| Benchmark | Mean per op | Derived rate |
|---|---:|---:|
| `BenchmarkShipmentLifecycle` (create + quote → accept → schedule → transit → deliver, 6 requests) | 18.6 ms | ~54 shipments/s sequential |
| `BenchmarkListShipments` (board list over 50 shipments) | 2.5 ms | ~400 requests/s sequential |
| `BenchmarkConcurrentLifecycle`, `-benchtime=400x`, `RunParallel` with GOMAXPROCS=8 | 2.8 ms | ~358 shipments/s (~2,150 requests/s) |

The concurrent benchmark counts every unexpected status or version conflict
in a `failures` metric; it reported 0 failures over 400 lifecycles
(2,400 requests). That shows the happy path does not trip optimistic
locking under parallel workers; it is not a substitute for the state-machine
and integration tests.

**Reading the numbers**

- The lifecycle op is intentionally heavy: six HTTP requests, each doing its
  own auth, RBAC check, version-checked UPDATE, audit-event insert and
  (when webhooks are configured) a transactional outbox row.
- Numbers are machine-dependent and were taken on a shared VM with the
  database on the same host; rerun the commands above on your hardware.
  This is not a load test of a deployed system.
