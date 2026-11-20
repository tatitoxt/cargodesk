-- CargoDesk initial schema. Timestamps are UTC (timestamptz). Every
-- tenant-owned table carries organization_id and uses composite foreign keys
-- so rows can only reference rows of the same organization.

CREATE TABLE organizations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug             text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9-]{2,40}$'),
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    timezone         text NOT NULL DEFAULT 'UTC',
    reference_prefix text NOT NULL DEFAULT 'CD' CHECK (reference_prefix ~ '^[A-Z]{2,4}$'),
    next_reference   integer NOT NULL DEFAULT 1001 CHECK (next_reference > 0),
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sla_policies (
    organization_id uuid NOT NULL REFERENCES organizations (id),
    priority        text NOT NULL CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    hours           integer NOT NULL CHECK (hours BETWEEN 1 AND 720),
    PRIMARY KEY (organization_id, priority)
);

CREATE TABLE users (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations (id),
    email           text NOT NULL UNIQUE CHECK (length(email) BETWEEN 3 AND 254),
    display_name    text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 120),
    role            text NOT NULL CHECK (role IN ('viewer', 'coordinator', 'admin')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE TABLE shipments (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  uuid NOT NULL REFERENCES organizations (id),
    reference        text NOT NULL,
    account_name     text NOT NULL CHECK (length(account_name) BETWEEN 1 AND 120),
    contact_label    text NOT NULL CHECK (length(contact_label) BETWEEN 1 AND 120),
    origin           text NOT NULL CHECK (length(origin) BETWEEN 1 AND 200),
    destination      text NOT NULL CHECK (length(destination) BETWEEN 1 AND 200),
    cargo_type       text NOT NULL CHECK (cargo_type IN ('general', 'palletized', 'refrigerated', 'hazardous', 'bulk', 'oversized')),
    required_date    date NOT NULL,
    notes            text NOT NULL DEFAULT '' CHECK (length(notes) <= 2000),
    state            text NOT NULL DEFAULT 'new' CHECK (state IN ('new', 'needs_info', 'quoted', 'accepted', 'scheduled', 'in_transit', 'delivered', 'cancelled', 'lost')),
    priority         text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    owner_id         uuid,
    next_action_at   timestamptz,
    next_action_note text NOT NULL DEFAULT '' CHECK (length(next_action_note) <= 200),
    due_at           timestamptz NOT NULL,
    quoted_at        timestamptz,
    pickup_at        timestamptz,
    carrier          text,
    delivered_at     timestamptz,
    received_by      text,
    delivery_note    text,
    closed_reason    text,
    version          integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_by       uuid NOT NULL,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    UNIQUE (organization_id, reference),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, owner_id) REFERENCES users (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES users (organization_id, id),
    CHECK (state <> 'delivered' OR (delivered_at IS NOT NULL AND received_by IS NOT NULL)),
    CHECK (state NOT IN ('scheduled', 'in_transit', 'delivered') OR (pickup_at IS NOT NULL AND carrier IS NOT NULL)),
    CHECK (state NOT IN ('cancelled', 'lost') OR closed_reason IS NOT NULL)
);

CREATE INDEX shipments_org_state_idx ON shipments (organization_id, state);
CREATE INDEX shipments_org_owner_idx ON shipments (organization_id, owner_id);
CREATE INDEX shipments_org_next_action_idx ON shipments (organization_id, next_action_at);

CREATE TABLE quotes (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    shipment_id     uuid NOT NULL,
    amount_cents    bigint NOT NULL CHECK (amount_cents > 0),
    currency        text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    valid_until     timestamptz,
    status          text NOT NULL CHECK (status IN ('open', 'accepted', 'superseded')),
    created_by      uuid NOT NULL,
    created_at      timestamptz NOT NULL,
    FOREIGN KEY (organization_id, shipment_id) REFERENCES shipments (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES users (organization_id, id)
);

CREATE UNIQUE INDEX quotes_one_open_per_shipment ON quotes (shipment_id) WHERE status = 'open';
CREATE UNIQUE INDEX quotes_one_accepted_per_shipment ON quotes (shipment_id) WHERE status = 'accepted';

CREATE TABLE assignments (
    id               bigserial PRIMARY KEY,
    organization_id  uuid NOT NULL,
    shipment_id      uuid NOT NULL,
    owner_id         uuid,
    priority         text NOT NULL,
    next_action_at   timestamptz,
    next_action_note text NOT NULL DEFAULT '',
    assigned_by      uuid NOT NULL,
    created_at       timestamptz NOT NULL,
    FOREIGN KEY (organization_id, shipment_id) REFERENCES shipments (organization_id, id),
    FOREIGN KEY (organization_id, owner_id) REFERENCES users (organization_id, id),
    FOREIGN KEY (organization_id, assigned_by) REFERENCES users (organization_id, id)
);

CREATE TABLE exceptions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    shipment_id     uuid NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('delay', 'damage', 'missing_info', 'other')),
    reason          text NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
    owner_id        uuid,
    follow_up_at    timestamptz NOT NULL,
    opened_by       uuid NOT NULL,
    opened_at       timestamptz NOT NULL,
    resolved_by     uuid,
    resolved_at     timestamptz,
    resolution      text CHECK (resolution IS NULL OR length(resolution) <= 500),
    FOREIGN KEY (organization_id, shipment_id) REFERENCES shipments (organization_id, id),
    FOREIGN KEY (organization_id, owner_id) REFERENCES users (organization_id, id),
    FOREIGN KEY (organization_id, opened_by) REFERENCES users (organization_id, id),
    FOREIGN KEY (organization_id, resolved_by) REFERENCES users (organization_id, id),
    CHECK ((resolved_at IS NULL) = (resolved_by IS NULL))
);

CREATE TABLE shipment_events (
    id              bigserial PRIMARY KEY,
    organization_id uuid NOT NULL,
    shipment_id     uuid NOT NULL,
    actor_id        uuid NOT NULL,
    action          text NOT NULL CHECK (action IN ('created', 'transitioned', 'assigned', 'note_added', 'exception_opened', 'exception_resolved')),
    from_state      text,
    to_state        text,
    reason          text,
    data            jsonb NOT NULL DEFAULT '{}'::jsonb,
    correlation_id  text NOT NULL CHECK (length(correlation_id) BETWEEN 1 AND 100),
    created_at      timestamptz NOT NULL,
    FOREIGN KEY (organization_id, shipment_id) REFERENCES shipments (organization_id, id),
    FOREIGN KEY (organization_id, actor_id) REFERENCES users (organization_id, id)
);

CREATE INDEX shipment_events_shipment_idx ON shipment_events (organization_id, shipment_id, id);
CREATE INDEX shipment_events_correlation_idx ON shipment_events (correlation_id);

CREATE FUNCTION shipment_events_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'shipment_events is append-only (% rejected)', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;

CREATE TRIGGER shipment_events_no_update_delete
    BEFORE UPDATE OR DELETE ON shipment_events
    FOR EACH ROW EXECUTE FUNCTION shipment_events_append_only();

CREATE TRIGGER shipment_events_no_truncate
    BEFORE TRUNCATE ON shipment_events
    FOR EACH STATEMENT EXECUTE FUNCTION shipment_events_append_only();

CREATE TABLE idempotency_keys (
    organization_id uuid NOT NULL,
    user_id         uuid NOT NULL,
    key             text NOT NULL CHECK (length(key) BETWEEN 1 AND 100),
    scope           text NOT NULL,
    request_hash    bytea NOT NULL,
    status_code     integer NOT NULL,
    response        jsonb NOT NULL,
    created_at      timestamptz NOT NULL,
    PRIMARY KEY (organization_id, user_id, key),
    FOREIGN KEY (organization_id, user_id) REFERENCES users (organization_id, id)
);
