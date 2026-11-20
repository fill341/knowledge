#!/usr/bin/env bash
# Runs once, on first initialization of an empty data volume.
# Creates the runtime role; table privileges are granted by migrations.
set -euo pipefail

app_password="$(cat "$KN_APP_PASSWORD_FILE")"

psql -v ON_ERROR_STOP=1 -v app_password="$app_password" \
     --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
CREATE ROLE kn_app LOGIN PASSWORD :'app_password';
GRANT CONNECT ON DATABASE knowledge TO kn_app;
GRANT USAGE ON SCHEMA public TO kn_app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL
