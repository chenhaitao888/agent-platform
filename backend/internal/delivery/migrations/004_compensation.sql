CREATE TABLE agent_delivery.compensation_plans (
 tenant_id text NOT NULL, id text NOT NULL, source_bundle_id text NOT NULL,
 source_run_id text NOT NULL, derived_bundle_id text,
 idempotency_key text NOT NULL, request_hash text NOT NULL,
 payload jsonb NOT NULL, digest text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,idempotency_key), UNIQUE(tenant_id,derived_bundle_id),
 FOREIGN KEY(tenant_id,source_bundle_id) REFERENCES agent_delivery.archives(tenant_id,id),
 FOREIGN KEY(tenant_id,source_run_id) REFERENCES agent_delivery.write_executions(tenant_id,id),
 FOREIGN KEY(tenant_id,derived_bundle_id) REFERENCES agent_delivery.archives(tenant_id,id)
);
CREATE INDEX compensation_source ON agent_delivery.compensation_plans(tenant_id,source_bundle_id,created_at);
