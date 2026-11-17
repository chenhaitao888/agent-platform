#!/bin/sh
set -eu
export POSTGRES_PWD
POSTGRES_PWD="$(cat /run/secrets/database-password)"
exec /etc/temporal/entrypoint.sh
