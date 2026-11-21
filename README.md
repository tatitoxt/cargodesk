# CargoDesk

[![CI](https://github.com/tatitoxt/cargodesk/actions/workflows/ci.yml/badge.svg)](https://github.com/tatitoxt/cargodesk/actions/workflows/ci.yml)

Logistics operations desk: requests, quotes, assignment and delivery with an
explicit state machine, owners, internal SLA tracking and an auditable timeline.

> **Portfolio / demo project.** All organizations, people, accounts and places
> in this repository are synthetic. It has not been used in production or
> validated with real operators; the workflow assumptions are listed in
> [`docs/spec/operations-workflow.md`](docs/spec/operations-workflow.md).

![CargoDesk request board with SLA tracking](docs/img/board.png)

## Problem

A small logistics operation coordinates requests, quotes, assignment and
delivery across spreadsheets, chat threads and people's memory. When a request
comes in, nobody can say at a glance **who owns it, what is missing to quote or
dispatch it, and what the next commitment is**. CargoDesk is a single desk where
each request has one state, one owner, one next action and a timeline that
explains how it got there.

## What it does today

- **Requests**: create a shipment request (synthetic account/contact, origin,
  destination, cargo type, required date, priority, notes).
- **Explicit state machine** — no free-form status edits, only transition commands:

  ```text
  new → needs_info → quoted → accepted → scheduled → in_transit → delivered
  (needs_info ↔ new, quoted → needs_info, and cancelled / lost as terminal alternatives)
  ```

  Each transition has requirements enforced by the API: a reason for
  `needs_info`/`cancelled`/`lost`, an amount and a three-letter uppercase currency code (format only, not checked against the ISO 4217 list) for `quoted`, an
  open quote for `accepted`, pickup time and carrier for `scheduled`, and
  delivery evidence (received by, delivered at — not in the future) for `delivered`.
- **Ownership and next action**: owner, priority and next-action date/note per request.
- **Internal SLA**: per-priority policies for three stages — "time to quote"
  (defaults: urgent 2h, high 8h, normal 24h, low 72h), "time to
  schedule" once a quote is accepted (defaults: urgent 8h, high 24h,
  normal 48h, low 96h) and "time in transit" from pickup to delivery
  (defaults: urgent 24h, high 48h, normal 96h, low 168h). Statuses are
  shown as on track / at risk (last 2h) / breached / met / missed per stage
  (`sla_status`, `schedule_sla_status`, `transit_sla_status`); any breached
  stage flags the board's overdue filter. `PUT /api/v1/settings/sla` accepts
  `hours`, `schedule_hours` and/or `transit_hours`.
- **Business-hours calendar**: admins can set a weekly working window
  (ISO weekdays + start/end minute, in the org timezone) — or per-day
  `windows` (e.g. a Saturday half-day) — plus holiday dates.
  With a calendar, SLA deadlines count business minutes — a request opened
  Friday 16:00 under a Mon-Fri 9-17 calendar gets its remaining hours on
  Monday. Without one, SLAs count wall-clock hours.
- **Notification webhooks**: admins point the org at an `https://` endpoint and can
  subscribe to a subset of event actions (`events` on `PUT /api/v1/settings/webhook` —
  e.g. `["transitioned"]` for an SLA-breach-only integration; empty means all). Every
  subscribed shipment event is POSTed there as signed JSON (HMAC-SHA256
  `X-CargoDesk-Signature`). Deliveries are queued in the same transaction as
  the event (outbox pattern) and retried with backoff by a background
  dispatcher; `POST /api/v1/settings/webhook/test` sends a ping and
  `GET` shows the last deliveries.
  - **Email channel**: admins set a per-org recipient list via
    `PUT /api/v1/settings/email` (validated addresses, admin-only write).
    The same outbox carries `channel='email'` deliveries — subject and plain-text
    body are rendered from the event payload and sent through SMTP
    (`SMTP_HOST`/`SMTP_PORT`/`SMTP_USER`/`SMTP_PASS`/`SMTP_FROM`/`SMTP_TLS`
    deployment env, plain or implicit TLS). `GET` reports `smtp_enabled`
    and the email delivery log.
- **Auditable timeline**: every change writes an append-only event (actor,
  action, from/to state, reason, change data, timestamp, correlation ID) in the same
  transaction as the change. PostgreSQL triggers reject `UPDATE`/`DELETE`/`TRUNCATE` on events.
- **Notes and exceptions**: free-text notes (rendered as text, never HTML) and
  exceptions with kind, reason and resolution.
- **Board and filters**: state, owner (incl. unassigned), overdue, due today
  (in the organization's timezone) and text search; filters live in the URL.
- **Saved views**: any user can name the current filter set (`POST/GET/DELETE
  /api/v1/views`); views are shared across the organization and names are
  unique per tenant regardless of case.
- **Pagination**: `GET /api/v1/shipments` accepts `limit` (1–500) and `offset`,
  returns `total` alongside the page, and sorts by a unique tiebreaker so
  pages are stable under concurrent writes; the board pages at 50 rows and
  resets to page 1 on any filter change.
- **Attachments**: coordinators+ upload files per shipment via multipart
  `POST /api/v1/shipments/{id}/attachments`; every member can list and download
  (`GET .../attachments`, `GET .../attachments/{aid}`), and coordinators+ can
  delete. Blobs live under `ATTACHMENTS_DIR` (storage keys are random, filenames
  are sanitized, and path-escape attempts are rejected); size is capped by
  `MAX_ATTACHMENT_BYTES` (default 10 MiB) and each file's SHA-256 is stored with
  the row. Uploads and deletes write audit events.
  - **Retention**: `PUT /api/v1/settings/retention` (admin) sets
    `attachmentRetentionDays` per organization (0 keeps files forever). A
    background pass (`RETENTION_INTERVAL`, default hourly) deletes expired
    rows and their blobs and writes an `attachment_removed` audit event under
    the `retention-purge` correlation id; `POST /api/v1/settings/retention/purge`
    runs the same pass synchronously.
- **CSV export**: allow-listed columns only, with spreadsheet-formula escaping.
- **Member management**: admins invite, re-role and remove organization members
  from the `/members` page (backed by `POST /api/v1/users`,
  `PATCH /api/v1/users/{id}` and `DELETE /api/v1/users/{id}`); emails are unique
  case-insensitively (409 `email_taken`), demoting or removing the last admin is
  rejected (409 `last_admin`), and deleting a member kills their sessions
  immediately. Non-admins see a read-only roster.
- **Roles**: `viewer` (read-only), `coordinator` (operate), `admin` (operate + SLA policy).
- **Multi-tenant**: every row is scoped by organization; other tenants' IDs return `404`.
- **Optimistic concurrency**: mutations carry a `version`; stale writes get `409`
  and the UI offers to reload.
- **Idempotency**: `Idempotency-Key` on create and transitions replays the first response.
- **Synthetic seed** built through the same store code paths, so seeded
  timelines are real events.

Not in the MVP (by design): WhatsApp, maps/GPS, billing, customer portal,
document storage, AI.

## Quickstart (Docker Compose demo)

Requires Docker with Compose v2.

```bash
git clone https://github.com/tatitoxt/cargodesk.git
cd cargodesk
docker compose up --build --wait
```

Open <http://localhost:3000> and pick a demo user:

| User | Role | Organization |
|---|---|---|
| Ana Demo | coordinator | Demo Freight Co. (synthetic) |
| Bruno Sample | coordinator | Demo Freight Co. (synthetic) |
| Carla Admin | admin | Demo Freight Co. (synthetic) |
| Dario Viewer | viewer | Demo Freight Co. (synthetic) |
| Erin Other-Tenant | coordinator | Sample Haulage Ltd. (synthetic) |

Sign-in is demo by default: a user picker enabled with `DEMO_AUTH=true` (the compose
file sets); there are no passwords. Ports are bound to `127.0.0.1` only.

Things to try: filter **Overdue only**, open `CD-1002` and move it to *Needs info*
and back, take a new request all the way to *Delivered* and read its timeline,
sign in as Dario Viewer to see read-only mode, or as Erin to see tenant isolation.

Stop and remove data: `docker compose down -v`.

## Architecture

```mermaid
flowchart LR
  B[Browser] -->|"pages + /api/v1/* (same origin)"| W["Next.js UI (web/)"]
  W -->|"rewrite /api/v1/*"| A["Go HTTP API (api/)"]
  subgraph A2 [Go modular monolith]
    A --> H[httpapi: auth, RBAC, correlation IDs, idempotency]
    H --> WF[workflow: state machine + requirements]
    H --> SL[sla: due dates, status, timezone]
    H --> CSV[csvexport]
    H --> ST[store: tenant-scoped SQL, transactional audit]
  end
  ST --> PG[(PostgreSQL)]
```

- `web/` — Next.js 15 + TypeScript client. Its only server-side role is a
  same-origin proxy so the session cookie stays first-party (no CORS).
- `api/` — Go 1.26 binary (`cargodesk serve|migrate|seed|scan`) using `net/http`
  and `pgx`. Business rules live here; the UI never decides transitions.
- PostgreSQL 17 — embedded SQL migrations, composite tenant foreign keys,
  append-only event triggers.

Design decisions: [`docs/adr/`](docs/adr). API reference: [`docs/api.md`](docs/api.md).

## Running tests

Prerequisites: Go 1.26, Node 22, Docker (for PostgreSQL).

```bash
# PostgreSQL for integration and E2E tests
docker run -d --name cargodesk-test-pg -p 5432:5432 \
  -e POSTGRES_USER=cargodesk -e POSTGRES_PASSWORD=cargodesk -e POSTGRES_DB=cargodesk_test \
  postgres:17.6-alpine
export CARGODESK_TEST_DATABASE_URL='postgres://cargodesk:cargodesk@localhost:5432/cargodesk_test?sslmode=disable'

# Go: unit tests + integration tests against the real database
cd api
gofmt -l . && go vet ./...
go test -count=1 -p 1 ./...
cd ..
```

Integration tests reset the schema of the database they are given; never point
`CARGODESK_TEST_DATABASE_URL` at data you care about. Without it, integration
tests are skipped locally (and fail in CI).

### End-to-end (Playwright)

The E2E suite starts the real Go API and the production Next.js build, reseeds
the database with `cargodesk seed -reset`, and drives Chromium:

```bash
cd api && go build -o bin/cargodesk ./cmd/cargodesk && cd ..
cd web
npm ci
npx playwright install --with-deps chromium
npm run lint && npm run typecheck && npm run build
DATABASE_URL="$CARGODESK_TEST_DATABASE_URL" npm run e2e
```

To run the same specs against the compose demo instead (fresh volume):
`E2E_EXTERNAL=1 E2E_SKIP_SEED=1 npm run e2e`.

E2E specs (`web/e2e/`):

- Main operator flow: create → assign owner → quote → accept → schedule → in
  transit → deliver with evidence → inspect the 7-event timeline → refresh → filter board.
- Recovery: overdue/owner/search filters (persisted across refresh), invalid
  quote currency surfaced as a field error, `needs_info` → `new` with reasons,
  XSS payload in a note rendered as text.
- Concurrent edit in two browser sessions → `409` conflict → reload.
- Viewer read-only UI and API `403`; tenant isolation; admin SLA change; off-site post-login redirects ignored.
- CSV export with formula escaping.
- Saved views: save the current filters under a name, re-apply from the board,
  and delete.
- Members: admin invites a member, changes their role and removes them; a
  duplicate email surfaces the API error; a coordinator sees the roster with no
  member controls.

### Measured results (this repository, reproducible with the commands above)

- Go: 57 top-level tests and 92 subtests pass across 7 packages
  (`go test -v ./... | grep -cE '^\s*--- PASS'` → 149), including real-PostgreSQL
  integration tests in `internal/store` and `internal/httpapi`.
- Playwright: 11 E2E tests pass in Chromium.

### CI

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every pull
request and push to `main`:

- **api**: `gofmt`, `go vet`, `staticcheck`, `govulncheck`, `go test -race`
  against a PostgreSQL service, and a PII/secret-shaped-value scan of the seed and docs.
- **web**: `npm ci`, ESLint, `tsc --noEmit`, `npm audit --audit-level=high`,
  `next build`, Playwright E2E against the real API and PostgreSQL.
- **compose**: builds and starts the Docker Compose demo and checks it over HTTP.

## Configuration

| Variable | Used by | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | api | – | PostgreSQL connection string. |
| `ADDR` | api | `:8080` | Listen address. |
| `DEMO_AUTH` | api | `false` | Enables the seeded demo-user sign-in. |
| `OIDC_ISSUER` | api | | OIDC issuer URL (e.g. `https://accounts.google.com`); with `OIDC_CLIENT_ID` enables `POST /api/v1/auth/oidc` — verifies the ID token against the issuer JWKS (signature, iss, aud, exp), maps the email claim to a provisioned user, and issues the same session cookie. |
| `OIDC_CLIENT_ID` | api | | Audience required in the ID token. |
| `SECURE_COOKIE` | api | `false` | Sets `Secure` on the session cookie (use behind HTTPS). |
| `ATTACHMENTS_DIR` | api | `data/attachments` | Blob storage root for shipment attachments; empty disables uploads. |
| `MAX_ATTACHMENT_BYTES` | api | `10485760` | Per-file upload size cap in bytes. |
| `RETENTION_INTERVAL` | api | `1h` | Attachment retention purge interval; `0` disables the background pass. |
| `API_URL` | web (build time) | `http://localhost:8080` | Where the Next.js proxy forwards `/api/v1/*`. |
| `CARGODESK_TEST_DATABASE_URL` | Go tests | – | Disposable database for integration tests. |

No external API keys are used or required.

## Limitations

- **Authentication is provisioned.** With OIDC configured, the SPA runs the
  Authorization Code + PKCE flow and posts the ID token to `/api/v1/auth/oidc`;
  users must still be seeded/invited by an admin (organization and role are not
  taken from the token). Without OIDC, sign-in is the seeded user picker; there is no
  password. Do not expose the demo to the internet.
- **Not validated with real users.** The workflow, SLA defaults and fields are
  assumptions from the spec, not findings from operators.
- One currency per quote and no pricing logic; quotes are entered manually.
- Delivery "evidence" is a text record (received by, time, note); photo/POD
  evidence is a generic attachment rather than a dedicated field.
- Notifications cover outbound webhooks (HMAC-signed), Slack incoming webhooks
  and email (SMTP); no WhatsApp, maps/GPS, or customer portal.
- Organizations are created by the seed only; members sign in with the demo or OIDC login, not by password invite yet.
- No load testing or security audit has been performed.

## Roadmap

1. WhatsApp channel for the existing notification pipeline (Slack already
   ships: `PUT /api/v1/settings/slack` with an incoming-webhook URL posts
   `{"text": ...}` messages through the same signed, retried outbox).

## License

[MIT](LICENSE) © Fausto Pastura

## Benchmarks

`go test -bench . ./api/internal/httpapi/` drives the real HTTP API against Postgres: full shipment lifecycle (create → deliver) at **11.2 ms/shipment** (~89/sec) and filtered board lists at 1.8 ms. See [docs/benchmarks.md](docs/benchmarks.md).
