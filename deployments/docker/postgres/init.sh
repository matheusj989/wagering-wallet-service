#!/bin/sh
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
CREATE ROLE wallet_app LOGIN PASSWORD '${WALLET_APP_PASSWORD:-wallet_app}';
GRANT USAGE ON SCHEMA public TO wallet_app;
SQL
