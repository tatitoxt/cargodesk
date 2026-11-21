-- Webhook event filtering: NULL webhook_events = every shipment event enqueues
-- a delivery; a non-empty array limits deliveries to the listed actions (e.g.
-- just transitioned for an SLA-breach-only integration).
ALTER TABLE organizations ADD COLUMN webhook_events text[];
