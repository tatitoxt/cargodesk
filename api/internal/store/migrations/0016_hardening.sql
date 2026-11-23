-- Accounts created through public sign-up are not trusted for OIDC email
-- matching (their email was never verified).
ALTER TABLE users ADD COLUMN self_registered boolean NOT NULL DEFAULT false;

-- Rotating a public tracking link is audited like any other shipment change.
ALTER TABLE shipment_events DROP CONSTRAINT shipment_events_action_check;
ALTER TABLE shipment_events ADD CONSTRAINT shipment_events_action_check
    CHECK (action IN ('created', 'transitioned', 'assigned', 'note_added', 'exception_opened',
                      'exception_resolved', 'attachment_added', 'attachment_removed', 'track_link_rotated'));

-- Written by `cargodesk seed` after every fixture committed, so an
-- interrupted seed is detected instead of reported as complete.
CREATE TABLE seed_runs (
    id           smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    completed_at timestamptz NOT NULL
);
