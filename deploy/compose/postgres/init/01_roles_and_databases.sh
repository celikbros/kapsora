#!/bin/sh
# Runs once on an empty data directory. Creates the non-privileged application role
# (no BYPASSRLS, owns nothing) and the Keycloak database. Local development only.
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE ROLE kapsora_app LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE
        PASSWORD '${KAPSORA_APP_PASSWORD}';

    CREATE ROLE keycloak LOGIN PASSWORD 'keycloak_local';
    CREATE DATABASE keycloak OWNER keycloak;
EOSQL
