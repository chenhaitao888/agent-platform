#!/bin/sh
set -eu
export SQL_PASSWORD
SQL_PASSWORD="$(cat /run/secrets/database-password)"
for database_name in temporal temporal_visibility; do
  if [ "$(cat "/setup-state/$database_name")" = t ]; then
    temporal-sql-tool --plugin postgres12 --ep postgres -u agent_platform -p 5432 --db "$database_name" setup-schema -v 0.0
  fi
  schema_kind=temporal
  if [ "$database_name" = temporal_visibility ]; then schema_kind=visibility; fi
  temporal-sql-tool --plugin postgres12 --ep postgres -u agent_platform -p 5432 --db "$database_name" \
    update-schema -d "/etc/temporal/schema/postgresql/v12/$schema_kind/versioned"
done
