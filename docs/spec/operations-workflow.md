# Operations workflow spec

Status: MVP spec. The workflow below is **assumed**, not validated with real
operators yet (see "Unconfirmed assumptions"). All examples use synthetic data.

## Problem

A small logistics operation coordinates requests, quotes, assignment and
delivery across spreadsheets, chat threads and people's memory. When a request
comes in, nobody can say at a glance who owns it, what is missing to quote or
dispatch it, and what the next commitment is.

**Primary user:** an operator / coordinator at a small logistics company.

**Job to be done:** "When a request arrives, I want to know who has it, what is
missing to quote or dispatch it, and what the next commitment is, without
rebuilding context from several chats."

## Unconfirmed assumptions

- The main bottleneck is follow-up (ownership + next action), not pricing.
- One operating timezone per organization is enough.
- An internal "time to quote" SLA per priority is the commitment operators track.
- Coordinators, not customers, enter requests (no customer portal).

## Roles

| Role          | Read | Create / transition / assign / note / exceptions | Export CSV | Edit SLA policy |
|---------------|------|--------------------------------------------------|------------|-----------------|
| `viewer`      | yes  | no                                               | no         | no              |
| `coordinator` | yes  | yes                                              | yes        | no              |
| `admin`       | yes  | yes                                              | yes        | yes             |

Every user belongs to exactly one organization; every read and write is scoped
to the caller's organization.

## States and transitions

```
new → needs_info → quoted → accepted → scheduled → in_transit → delivered
alternative terminal states: cancelled, lost
```

| From         | To           | Required input                                           |
|--------------|--------------|----------------------------------------------------------|
| `new`        | `needs_info` | `reason` (what is missing)                               |
| `new`        | `quoted`     | `quote.amount_cents` > 0, `quote.currency` (ISO 4217)    |
| `needs_info` | `new`        | `reason` (what was received)                             |
| `needs_info` | `quoted`     | quote, as above                                          |
| `quoted`     | `accepted`   | an open quote exists (it becomes `accepted`)             |
| `quoted`     | `needs_info` | `reason`                                                 |
| `accepted`   | `scheduled`  | accepted quote, `pickup_at`, `carrier`                   |
| `scheduled`  | `in_transit` | —                                                        |
| `in_transit` | `delivered`  | `evidence.received_by`, `evidence.delivered_at`          |
| `new`, `needs_info`, `quoted`, `accepted`, `scheduled` | `cancelled` | `reason` |
| `new`, `needs_info`, `quoted` | `lost` | `reason` |

`delivered`, `cancelled` and `lost` are terminal. State is never edited
directly: it only changes through `POST /api/v1/shipments/{id}/transitions`.

## Internal SLA

Each organization configures "hours to quote" per priority (`low`, `normal`,
`high`, `urgent`). On creation `due_at = created_at + hours[priority]`; when
the priority changes, `due_at` is recomputed from `created_at`. The SLA is
**met** once the shipment reaches `quoted` (or later) before `due_at`, and
**breached** when `due_at` passes while it is still `new` / `needs_info`.

## Overdue work

A shipment is **overdue** when it is not terminal and either:

- `next_action_at <= now`, or
- the SLA is breached (still `new` / `needs_info` and `due_at <= now`).

"Due today" means `next_action_at` falls on today's calendar date in the
organization's operating timezone. Timestamps are stored in UTC.

## Acceptance scenarios

### A1 — create a request
- **Given** a coordinator is signed in
- **When** they submit a request without `origin` or with `required_date` = `2026-02-30`
- **Then** the form and the API (`422 validation_failed`) report each invalid field
- **When** they submit a valid request
- **Then** it gets a reference like `CD-1001`, state `new`, and a `created` event

### A2 — board
- **Given** a new request exists
- **When** the coordinator opens the board and reloads the page
- **Then** the request shows reference, state, owner and next action

### A3 — transitions
- **Given** a request in `new`
- **When** they request `new → delivered`
- **Then** the API returns `409 invalid_transition` with the current state, the requested state and the allowed targets
- **When** they request `accepted → scheduled` without `pickup_at`
- **Then** the API returns `422 transition_requirements` listing the missing fields

### A4 — audit
- **When** owner, state, next action date, a note or an exception changes
- **Then** an immutable event with actor, UTC timestamp and correlation ID is written in the same transaction as the change

### A5 — overdue filter
- **Given** a request whose next action is in the past
- **When** the board is filtered by "overdue"
- **Then** the UI and `GET /api/v1/shipments?overdue=true` return the same rows

### A6 — concurrent edits
- **Given** two coordinators loaded the same shipment at version 3
- **When** the first saves, and then the second saves with version 3
- **Then** the second receives `409 version_conflict` with `version_current` and nothing is overwritten

### A7 — synthetic demo data
- **Then** seed data contains no real names, emails, phone numbers or addresses, and the fixture scanner fails on a forbidden fixture

### A8 — CSV export
- **Given** a viewer
- **Then** `GET /api/v1/exports/shipments.csv` returns `403`
- **Given** a coordinator
- **Then** the CSV has only the allowed columns and cells starting with `=`, `+`, `-`, `@`, tab or CR are prefixed with `'`

### A9 — cancel and retention
- **When** a shipment is cancelled
- **Then** a reason is required, the row is kept, and its full history stays readable

## Retention policy (MVP)

Shipments are never hard-deleted through the API; cancellation is a state.
`shipment_events` are append-only: a database trigger rejects `UPDATE` and
`DELETE`. Purging old demo data is done only by the explicit `seed --reset`
command, which recreates the demo database.

## Out of scope

Billing/taxes, GPS tracking, route optimisation, customer portal, payments,
automated decisions, document storage, WhatsApp/email/Maps integrations, AI.
