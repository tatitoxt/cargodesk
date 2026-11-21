-- Slack notification channel: a per-org incoming-webhook URL; deliveries use
-- the same outbox as webhook/email, with channel='slack' and a Slack-shaped
-- ({"text": ...}) payload frozen at enqueue time.
ALTER TABLE organizations ADD COLUMN slack_webhook_url text;

ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check
    CHECK (channel IN ('webhook', 'email', 'slack'));
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_target_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_target_check
    CHECK ((channel IN ('webhook', 'slack') AND url IS NOT NULL)
        OR (channel = 'email' AND cardinality(rcpt) > 0));
