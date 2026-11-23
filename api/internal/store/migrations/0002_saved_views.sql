-- Saved board views: named filter sets shared across an organization.
CREATE TABLE saved_views (
    organization_id uuid NOT NULL,
    id              uuid NOT NULL DEFAULT gen_random_uuid(),
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    filters         jsonb NOT NULL,
    created_by      uuid NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES users (organization_id, id)
);

-- Names are unique per organization regardless of case.
CREATE UNIQUE INDEX saved_views_name_key ON saved_views (organization_id, lower(name));
