# ADR 0003 — Seeded demo sign-in, no custom crypto

Status: accepted

## Decision

The MVP has no real identity provider. With `DEMO_AUTH=true` the login page
lists the seeded synthetic users; choosing one creates a server-side session
(random 32-byte token from `crypto/rand`, stored hashed with SHA-256 in
`sessions`, sent as an `HttpOnly`, `SameSite=Lax` cookie). The UI shows a
permanent "Demo mode — synthetic data" banner. With `DEMO_AUTH=false` the
login endpoint is disabled.

No passwords are stored and no tokens are signed by hand. A real deployment
would replace the login endpoint with OIDC; roles and sessions stay.

## Consequences

Anyone who can reach a demo instance can sign in as any seeded user. This is
documented as a limitation and is why the demo is meant for localhost.
