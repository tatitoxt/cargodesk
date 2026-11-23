CREATE TABLE business_calendars (
    organization_id uuid PRIMARY KEY REFERENCES organizations (id),
    -- ISO weekday numbers, 1 = Monday .. 7 = Sunday.
    days            smallint[] NOT NULL CHECK (array_length(days, 1) BETWEEN 1 AND 7),
    start_minute    integer NOT NULL CHECK (start_minute BETWEEN 0 AND 1439),
    end_minute      integer NOT NULL CHECK (end_minute BETWEEN 1 AND 1440),
    holidays        date[] NOT NULL DEFAULT '{}',
    updated_by      uuid NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, updated_by) REFERENCES users (organization_id, id),
    CHECK (end_minute > start_minute)
);
