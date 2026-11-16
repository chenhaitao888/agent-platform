CREATE TABLE agent_delivery.archives (
    id text PRIMARY KEY,
    tenant_id text NOT NULL,
    change_id text NOT NULL,
    plan_id text NOT NULL,
    plan_hash text NOT NULL CHECK (length(plan_hash) = 64),
    created_at timestamptz NOT NULL,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    digest text NOT NULL CHECK (length(digest) = 64),
    UNIQUE (tenant_id, change_id, plan_id, plan_hash),
    UNIQUE (tenant_id, id)
);
CREATE INDEX archives_tenant_created ON agent_delivery.archives (tenant_id, created_at DESC, id);
CREATE TABLE agent_delivery.requests (
    tenant_id text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    archive_id text NOT NULL,
    PRIMARY KEY (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, archive_id) REFERENCES agent_delivery.archives (tenant_id, id)
);
