ALTER TABLE sla_policies
    ADD COLUMN stage text NOT NULL DEFAULT 'quote' CHECK (stage IN ('quote', 'schedule'));
ALTER TABLE sla_policies DROP CONSTRAINT sla_policies_pkey;
ALTER TABLE sla_policies ADD PRIMARY KEY (organization_id, stage, priority);

ALTER TABLE shipments ADD COLUMN accepted_at      timestamptz;
ALTER TABLE shipments ADD COLUMN scheduled_at     timestamptz;
ALTER TABLE shipments ADD COLUMN schedule_due_at  timestamptz;
