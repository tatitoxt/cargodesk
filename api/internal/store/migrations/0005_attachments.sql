CREATE TABLE IF NOT EXISTS attachments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    shipment_id     uuid NOT NULL REFERENCES shipments(id) ON DELETE CASCADE,
    filename        text NOT NULL,
    content_type    text NOT NULL DEFAULT 'application/octet-stream',
    byte_size       bigint NOT NULL CHECK (byte_size >= 0),
    sha256          text NOT NULL,
    storage_key     text NOT NULL UNIQUE,
    uploaded_by     uuid NOT NULL REFERENCES users(id),
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS attachments_shipment_idx ON attachments (shipment_id, created_at);

ALTER TABLE shipment_events DROP CONSTRAINT shipment_events_action_check;
ALTER TABLE shipment_events ADD CONSTRAINT shipment_events_action_check
    CHECK (action IN ('created', 'transitioned', 'assigned', 'note_added', 'exception_opened', 'exception_resolved', 'attachment_added', 'attachment_removed'));
