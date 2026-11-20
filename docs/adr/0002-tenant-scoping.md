# ADR 0002 — Tenant scoping by organization_id

Status: accepted

## Decision

Single database, shared schema. Every tenant-owned table has
`organization_id`. The store layer takes the organization ID from the
authenticated session (never from the request body or URL) and includes it in
every `WHERE` clause and in composite foreign keys such as
`(organization_id, owner_id) → users(organization_id, id)`, so a shipment
cannot point to another organization's user even through a bug.

A shipment ID from another organization returns `404`, not `403`, to avoid
confirming that it exists.

## Consequences

Integration tests create two organizations and assert that reads, writes and
exports never cross them. Row-level security is not used in the MVP.
