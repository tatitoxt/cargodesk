-- Outbound webhook notifications: org settings live on organizations; each
-- shipment event enqueues a delivery row inside the same transaction
-- (transactional outbox), and a dispatcher posts them with retries.
ALTER TABLE organizations ADD COLUMN webhook_url text;
ALTER TABLE organizations ADD COLUMN webhook_secret text;

CREATE TABLE notification_deliveries (
    id              bigserial PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    event_id        bigint REFERENCES shipment_events (id) ON DELETE CASCADE,
    action          text NOT NULL,
    url             text NOT NULL,
    payload         jsonb NOT NULL,
    attempts        int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    delivered_at    timestamptz,
    status_code     int,
    last_error      text,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (next_attempt_at)
    WHERE delivered_at IS NULL;
