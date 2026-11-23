-- Email notification channel: per-org recipient lists; deliveries carry a
-- channel tag so webhook and mail rows share one outbox and retry policy.
ALTER TABLE organizations ADD COLUMN notification_emails text[];

ALTER TABLE notification_deliveries ADD COLUMN channel text NOT NULL DEFAULT 'webhook';
ALTER TABLE notification_deliveries ADD COLUMN rcpt text[] NOT NULL DEFAULT '{}';
ALTER TABLE notification_deliveries ALTER COLUMN url DROP NOT NULL;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check
    CHECK (channel IN ('webhook', 'email'));
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_target_check
    CHECK ((channel = 'webhook' AND url IS NOT NULL)
        OR (channel = 'email' AND cardinality(rcpt) > 0));
