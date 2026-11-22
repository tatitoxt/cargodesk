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
| POST | `/api/v1/auth/password-login` | – | `{"email": "…", "password": "…"}` → session cookie; body carries `must_change_password` for invited users. Wrong credentials → `401 bad_credentials` (same for unknown email). |
| POST | `/api/v1/signup` | – | `{"slug", "name", "timezone", "reference_prefix", "email", "display_name", "password"}` → provisions an organization with default SLA policies plus its first admin, then issues the session cookie. `409 slug_taken`/`email_taken`, `422` field errors, `429` after 10 sign-ups/hour per IP. |
| GET | `/api/v1/me` | any | Current user, role and organization. |
| POST | `/api/v1/me/password` | any | `{"current_password": "…", "new_password": "…"}` → `204`; wrong current → `401`, weak (under 10 chars or same) → `422`. Clears `must_change_password`. |
| GET | `/api/v1/users` | any | Users of the caller's organization (owner picker). |
| POST | `/api/v1/users/invite` | admin | `{"email": "…", "display_name": "…", "role": "…"}` → `201` with the user and a one-time `temporary_password`; the invitee must change it on first sign-in. Duplicate email → `409`. |
| PATCH | `/api/v1/users/{id}` | admin | Change role/display name. Last-admin demotion → `409`. |
| DELETE | `/api/v1/users/{id}` | admin | Removes the member and kills their sessions. Last admin → `409`. |
| GET | `/api/v1/shipments` | any | Filters: `state`, `owner` (`<uuid>` or `none`), `overdue=true`, `due_today=true`, `q` (reference/account/origin/destination). |
| POST | `/api/v1/shipments` | coordinator, admin | Create request. Supports `Idempotency-Key`. |
| GET | `/api/v1/shipments/{id}` | any | Detail incl. quotes, open exceptions, SLA status. Other tenants' IDs → `404`. |
| GET | `/api/v1/shipments/{id}/events` | any | Append-only timeline. |
| POST | `/api/v1/shipments/{id}/track-link` | coordinator, admin | Rotates the public tracking token → `{"track_token": "…"}`; previously shared links stop resolving. |
| GET | `/api/v1/track/{token}` | – | Unauthenticated public tracking view: reference, origin → destination, state, carrier and a sanitized event timeline (no notes, quotes, owners or internal data). Unknown or rotated tokens → `404`. Served by the web app at `/track/{token}`. |
| POST | `/api/v1/shipments/{id}/transitions` | coordinator, admin | `{"to": "quoted", "version": 3, "reason": "…", "quote": {…}, "schedule": {…}, "evidence": {…}}`. Stale `version` → `409`. Supports `Idempotency-Key`. |
| POST | `/api/v1/shipments/{id}/events` | coordinator, admin | Notes and exceptions: `{"kind": "note", "body": "…"}`, `{"kind": "exception", "version": 4, "exception_kind": "…", "reason": "…"}` or `{"kind": "exception_resolved", …}`. |
| PATCH | `/api/v1/shipments/{id}/assignment` | coordinator, admin | Owner, priority, next action. Requires `version`. |
| GET | `/api/v1/exports/shipments.csv` | coordinator, admin | Same filters as the list. Allow-listed columns, formula-escaped. |
| GET | `/api/v1/views` | any | Saved board views of the caller's organization. |
| POST | `/api/v1/views` | any | `{"name": "…", "filters": {"state": "…", "owner": "…", "overdue": "…", "due": "…", "q": "…"}}`. Names unique per tenant (case-insensitive) → `409`; unknown filter keys → `422`. |
| DELETE | `/api/v1/views/{id}` | any | Deletes the view. Other tenants' IDs → `404`. |
| GET | `/api/v1/settings/sla` | any | Quote SLA hours per priority. |
| PUT | `/api/v1/settings/sla` | admin | Update SLA hours (1–720). |
| GET | `/api/v1/settings/business-hours` | any | Org business-hours calendar (`null` when unset) + timezone. |
| PUT | `/api/v1/settings/business-hours` | admin | `{"calendar": {"days": [1..7], "start_minute": 540, "end_minute": 1020, "holidays": ["YYYY-MM-DD"]}}`. Invalid → `422`. |
| DELETE | `/api/v1/settings/business-hours` | admin | Remove the calendar; SLAs revert to wall-clock hours. |
| GET | `/api/v1/settings/webhook` | any | Webhook URL, `has_secret` and the last 10 deliveries. |
| PUT | `/api/v1/settings/webhook` | admin | `{"url": "https://…", "secret": "…"}`. https only; `422` otherwise. |
| DELETE | `/api/v1/settings/webhook` | admin | Disable notifications. |
| POST | `/api/v1/settings/webhook/test` | admin | Queue a `webhook_test` ping delivery. |
| GET | `/api/v1/settings/whatsapp` | any | WhatsApp config: `phone_number_id`, `recipients`, `api_url`, `token_set`. |
| PUT | `/api/v1/settings/whatsapp` | admin | `{"phone_number_id": "…", "token": "…", "recipients": ["+549…"], "api_url": "https://…"}`. E.164 recipients; `api_url` must be https. `422` on invalid. |
| DELETE | `/api/v1/settings/whatsapp` | admin | Clear the channel config including the stored token. |

### Outbound webhooks

Every shipment event (`created`, `transitioned`, `assigned`, `note_added`,
`exception_opened`, `exception_resolved`, …) is queued into
`notification_deliveries` **inside the same transaction** as the event itself
(transactional outbox — a notification can never be lost between commit and
delivery). A dispatcher posts the JSON payload to the org's `webhook_url` with
`Content-Type: application/json` and, when a secret is configured,
`X-CargoDesk-Signature: sha256=<hmac-sha256 of the raw body>`. Failures retry
with 30 s × attempts backoff, up to 8 attempts; the last 10 deliveries are
visible via `GET /api/v1/settings/webhook`.

Mutations require `Content-Type: application/json` (`415` otherwise) and
bodies over 64 KiB are rejected with `413`. Reusing an `Idempotency-Key`
with a different body returns `422`; a retry while the first request is still
running returns `409 idempotency_in_progress`. Only successful (2xx) responses
are replayed, so a request rejected with a 4xx can be corrected and retried
with the same key.

Exact request/response shapes are defined in `api/internal/httpapi/handlers.go`
and exercised in `api/internal/httpapi/api_integration_test.go`.
