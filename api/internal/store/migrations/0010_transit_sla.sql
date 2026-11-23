ALTER TABLE sla_policies DROP CONSTRAINT sla_policies_stage_check;
ALTER TABLE sla_policies ADD CONSTRAINT sla_policies_stage_check
    CHECK (stage IN ('quote', 'schedule', 'transit'));

ALTER TABLE shipments ADD COLUMN in_transit_at  timestamptz;
ALTER TABLE shipments ADD COLUMN transit_due_at timestamptz;
