# HTTP API (v1)

All endpoints except `/healthz` and the demo auth routes require the
`cargodesk_session` cookie. Responses are JSON unless noted. Every response
carries `X-Correlation-ID` (echoed from the request or generated). Errors use:

```json
{ "error": "validation_failed", "message": "…", "correlation_id": "…", "fields": { "origin": "required" } }
```

| Method | Path | Role | Notes |
|---|---|---|---|
| GET | `/healthz` | – | Liveness. |
| GET | `/api/v1/auth/demo-users` | – | Seeded synthetic users (only when `DEMO_AUTH=true`). |
| POST | `/api/v1/auth/demo-login` | – | `{"user_id": "…"}` → sets an HttpOnly session cookie (12h). |
| POST | `/api/v1/auth/logout` | any | Deletes the session. |
| GET | `/api/v1/me` | any | Current user, role and organization. |
| GET | `/api/v1/users` | any | Users of the caller's organization (owner picker). |
| GET | `/api/v1/shipments` | any | Filters: `state`, `owner` (`<uuid>` or `none`), `overdue=true`, `due_today=true`, `q` (reference/account/origin/destination). |
| POST | `/api/v1/shipments` | coordinator, admin | Create request. Supports `Idempotency-Key`. |
| GET | `/api/v1/shipments/{id}` | any | Detail incl. quotes, open exceptions, SLA status. Other tenants' IDs → `404`. |
| GET | `/api/v1/shipments/{id}/events` | any | Append-only timeline. |
| POST | `/api/v1/shipments/{id}/transitions` | coordinator, admin | `{"to": "quoted", "version": 3, "reason": "…", "quote": {…}, "schedule": {…}, "evidence": {…}}`. Stale `version` → `409`. Supports `Idempotency-Key`. |
| POST | `/api/v1/shipments/{id}/events` | coordinator, admin | Notes and exceptions: `{"kind": "note", "body": "…"}`, `{"kind": "exception", "version": 4, "exception_kind": "…", "reason": "…"}` or `{"kind": "exception_resolved", …}`. |
| PATCH | `/api/v1/shipments/{id}/assignment` | coordinator, admin | Owner, priority, next action. Requires `version`. |
| GET | `/api/v1/exports/shipments.csv` | coordinator, admin | Same filters as the list. Allow-listed columns, formula-escaped. |
| GET | `/api/v1/settings/sla` | any | Quote SLA hours per priority. |
| PUT | `/api/v1/settings/sla` | admin | Update SLA hours (1–720). |

Mutations require `Content-Type: application/json` (`415` otherwise) and
bodies over 64 KiB are rejected with `413`. Reusing an `Idempotency-Key`
with a different body returns `422`; a retry while the first request is still
running returns `409 idempotency_in_progress`. Only successful (2xx) responses
are replayed, so a request rejected with a 4xx can be corrected and retried
with the same key.

Exact request/response shapes are defined in `api/internal/httpapi/handlers.go`
and exercised in `api/internal/httpapi/api_integration_test.go`.
