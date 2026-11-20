# ADR 0001 — Next.js UI + separate Go API (modular monolith)

Status: accepted

## Context

The spec asks for a desktop/tablet UI and explicit contracts for workflow,
audit and export. Options: (a) Next.js only, with route handlers talking to
PostgreSQL; (b) Next.js UI + a separate Go HTTP API.

## Decision

(b). One Go binary (`api/`) owns every rule: validation, the state machine,
tenant scoping, audit events, optimistic concurrency and CSV export. The
Next.js app (`web/`) is a thin client; its only server code is a
same-origin proxy (`/api/v1/*` → Go API) so the browser never needs CORS.

Inside the Go binary, packages are modules of a monolith (`workflow`,
`store`, `httpapi`, `export`, `sla`), not services. No queues, no Redis.

## Consequences

- The API contract (`api/openapi.json`) is testable without a browser.
- Two toolchains in CI (Go + Node), accepted for the clearer boundary.
- Business logic is not duplicated in the UI; the UI only pre-validates
  required fields for faster feedback and always shows API errors.
