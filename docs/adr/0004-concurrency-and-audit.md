# ADR 0004 — Optimistic concurrency and transactional audit

Status: accepted

## Decision

- `shipments.version` is an integer incremented on every mutation. Every
  mutating request carries the version the client saw; the update uses
  `WHERE id = $1 AND organization_id = $2 AND version = $3`. Zero rows →
  `409 version_conflict` with `version_current`.
- Each mutation and its `shipment_events` row are written in one
  transaction. If the event insert fails, the change is rolled back.
- Events are append-only, enforced by a trigger that raises on `UPDATE` and
  `DELETE`.
- Each HTTP request has a correlation ID (`X-Correlation-ID` header, or
  generated) that is logged and stored on the events it produces.
- `POST` create and transition accept an `Idempotency-Key` header; a replay
  with the same key returns the stored response instead of applying twice.
