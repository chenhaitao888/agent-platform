-- Real-write grants and dispatch records are deliberately separate from SIMULATION.
CREATE TABLE agent_delivery.write_approvals (
 tenant_id text NOT NULL, id text NOT NULL, bundle_id text NOT NULL,
 idempotency_key text NOT NULL, request_hash text NOT NULL,
 payload jsonb NOT NULL CHECK(payload->>'mode'='WRITE'), digest text NOT NULL,
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,idempotency_key),
 FOREIGN KEY(tenant_id,bundle_id) REFERENCES agent_delivery.archives(tenant_id,id)
);
CREATE TABLE agent_delivery.write_revocations (
 tenant_id text NOT NULL, idempotency_key text NOT NULL, request_hash text NOT NULL,
 approval_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,idempotency_key),
 FOREIGN KEY(tenant_id,approval_id) REFERENCES agent_delivery.write_approvals(tenant_id,id)
);
CREATE TABLE agent_delivery.write_executions (
 tenant_id text NOT NULL, id text NOT NULL, bundle_id text NOT NULL, approval_id text NOT NULL,
 workflow_id text NOT NULL UNIQUE, idempotency_key text NOT NULL, request_hash text NOT NULL,
 payload jsonb NOT NULL CHECK(payload->>'mode'='WRITE'), digest text NOT NULL,
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,idempotency_key), UNIQUE(tenant_id,approval_id),
 FOREIGN KEY(tenant_id,bundle_id) REFERENCES agent_delivery.archives(tenant_id,id),
 FOREIGN KEY(tenant_id,approval_id) REFERENCES agent_delivery.write_approvals(tenant_id,id)
);
CREATE TABLE agent_delivery.write_outbox (
 id text PRIMARY KEY, tenant_id text NOT NULL, run_id text NOT NULL,
 kind text NOT NULL CHECK(kind='write.requested.v1'), attempts integer NOT NULL DEFAULT 0,
 owner text NOT NULL DEFAULT '', fence bigint NOT NULL DEFAULT 0, lease_until timestamptz,
 available_at timestamptz NOT NULL DEFAULT clock_timestamp(), delivered_at timestamptz,
 UNIQUE(tenant_id,run_id),
 FOREIGN KEY(tenant_id,run_id) REFERENCES agent_delivery.write_executions(tenant_id,id)
);
CREATE INDEX write_outbox_pending ON agent_delivery.write_outbox(available_at) WHERE delivered_at IS NULL;
