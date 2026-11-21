-- Per-organization retention window for shipment attachments, in days.
-- 0 (the default) keeps attachments forever.
ALTER TABLE organizations
    ADD COLUMN attachment_retention_days integer NOT NULL DEFAULT 0
    CHECK (attachment_retention_days >= 0);
