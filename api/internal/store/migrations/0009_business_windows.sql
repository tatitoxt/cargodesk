-- Per-weekday working windows; '[]' means the shared days/start/end apply.
ALTER TABLE business_calendars
    ADD COLUMN windows jsonb NOT NULL DEFAULT '[]'::jsonb;
