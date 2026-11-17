CREATE TABLE agent_delivery.approvals (
 id text PRIMARY KEY, tenant_id text NOT NULL, bundle_id text NOT NULL,
 bundle_digest text NOT NULL, plan_hash text NOT NULL, policy_hash text NOT NULL,
 subject text NOT NULL, reason text NOT NULL, mode text NOT NULL CHECK(mode='SIMULATION'),
 state text NOT NULL CHECK(state IN ('APPROVED','CONSUMED','REVOKED','INVALIDATED')),
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL, consumed_by text NOT NULL DEFAULT '', error_code text NOT NULL DEFAULT '',
 revoked_by text NOT NULL DEFAULT '', revoked_reason text NOT NULL DEFAULT '', revoked_at timestamptz,
 idempotency_key text NOT NULL, request_hash text NOT NULL,
 UNIQUE(tenant_id,id), UNIQUE(tenant_id,idempotency_key),
 FOREIGN KEY(tenant_id,bundle_id) REFERENCES agent_delivery.archives(tenant_id,id)
);
CREATE TABLE agent_delivery.revocations (
 tenant_id text NOT NULL, idempotency_key text NOT NULL, request_hash text NOT NULL,
 approval_id text NOT NULL, subject text NOT NULL, reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(), PRIMARY KEY(tenant_id,idempotency_key),
 FOREIGN KEY(tenant_id,approval_id) REFERENCES agent_delivery.approvals(tenant_id,id)
);
CREATE TABLE agent_delivery.simulations (
 id text PRIMARY KEY, tenant_id text NOT NULL, bundle_id text NOT NULL, approval_id text NOT NULL,
 subject text NOT NULL, scenario text NOT NULL CHECK(scenario IN ('success','response_loss','fail_before')),
 state text NOT NULL CHECK(state IN ('QUEUED','RUNNING','SIMULATED','BLOCKED','FAILED')),
 error_code text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 completed_at timestamptz, workflow_id text NOT NULL UNIQUE,
 idempotency_key text NOT NULL, request_hash text NOT NULL,
 UNIQUE(tenant_id,id), UNIQUE(tenant_id,idempotency_key), UNIQUE(tenant_id,approval_id),
 FOREIGN KEY(tenant_id,bundle_id) REFERENCES agent_delivery.archives(tenant_id,id),
 FOREIGN KEY(tenant_id,approval_id) REFERENCES agent_delivery.approvals(tenant_id,id)
);
CREATE TABLE agent_delivery.outbox (
 id text PRIMARY KEY, tenant_id text NOT NULL, run_id text NOT NULL,
 kind text NOT NULL CHECK(kind='simulation.requested.v1'), attempts integer NOT NULL DEFAULT 0,
 owner text NOT NULL DEFAULT '', fence bigint NOT NULL DEFAULT 0, lease_until timestamptz,
 available_at timestamptz NOT NULL DEFAULT clock_timestamp(), delivered_at timestamptz,
 UNIQUE(tenant_id,run_id), FOREIGN KEY(tenant_id,run_id) REFERENCES agent_delivery.simulations(tenant_id,id)
);
CREATE TABLE agent_delivery.operations (
 id text PRIMARY KEY, tenant_id text NOT NULL, run_id text NOT NULL, ordinal integer NOT NULL,
 action_id text NOT NULL, action_hash text NOT NULL,
 state text NOT NULL CHECK(state IN ('NOT_STARTED','RUNNING','UNKNOWN','SUCCEEDED','BLOCKED','FAILED')),
 attempts integer NOT NULL DEFAULT 0, fence bigint NOT NULL DEFAULT 0,
 owner text NOT NULL DEFAULT '', lease_until timestamptz,
 error_code text NOT NULL DEFAULT '', receipt_id text NOT NULL DEFAULT '', completed_at timestamptz,
 UNIQUE(tenant_id,id), UNIQUE(tenant_id,run_id,action_id),
 FOREIGN KEY(tenant_id,run_id) REFERENCES agent_delivery.simulations(tenant_id,id)
);
CREATE TABLE agent_delivery.simulation_receipts (
 operation_id text PRIMARY KEY, tenant_id text NOT NULL, action_hash text NOT NULL,
 receipt_id text NOT NULL UNIQUE, fence bigint NOT NULL,
 mode text NOT NULL CHECK(mode='SIMULATION'), created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(tenant_id,operation_id) REFERENCES agent_delivery.operations(tenant_id,id)
);
CREATE INDEX outbox_pending ON agent_delivery.outbox(available_at) WHERE delivered_at IS NULL;
CREATE INDEX simulations_bundle ON agent_delivery.simulations(tenant_id,bundle_id,created_at);
