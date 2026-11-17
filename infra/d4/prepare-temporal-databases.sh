#!/bin/sh
set -eu
export PGPASSWORD
PGPASSWORD="$(cat /run/secrets/database-password)"
mkdir -p /setup-state
for database_name in temporal temporal_visibility; do
  psql -h postgres -U agent_platform -d postgres -v ON_ERROR_STOP=1 <<SQL
SELECT 'CREATE DATABASE ${database_name} OWNER agent_platform'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname='${database_name}')\gexec
SQL
  psql -h postgres -U agent_platform -d "$database_name" -At -v ON_ERROR_STOP=1 \
    -c "SELECT to_regclass('public.schema_version') IS NULL" > "/setup-state/$database_name"
done
