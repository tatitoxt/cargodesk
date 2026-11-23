-- WhatsApp notification channel on the shared outbox: per-org Meta Cloud API
-- credentials (phone-number id + access token), a recipient list and an API
-- base URL (defaults to graph.facebook.com; overridable for gateways/tests).
-- Deliveries reuse channel='whatsapp' rows with the rendered text frozen in
-- the payload and the resolved endpoint frozen in url; the token is read live
-- so rotating it takes effect for already-queued deliveries.
ALTER TABLE organizations
    ADD COLUMN whatsapp_phone_number_id text,
    ADD COLUMN whatsapp_token text,
    ADD COLUMN whatsapp_recipients text[],
    ADD COLUMN whatsapp_api_url text;

ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check
    CHECK (channel IN ('webhook', 'email', 'slack', 'whatsapp'));
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_target_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_target_check
    CHECK ((channel IN ('webhook', 'slack') AND url IS NOT NULL)
        OR (channel IN ('email', 'whatsapp') AND cardinality(rcpt) > 0));
