-- One-time local bootstrap on a native PostgreSQL 18. Run as a superuser against the
-- maintenance database, e.g.:
--   psql "postgres://postgres:PASSWORD@localhost:5432/postgres" -v ON_ERROR_STOP=1 -f scripts/db-init.sql
-- Creates the non-privileged application role and the kapsora database. Passwords here
-- are local-only defaults matching .env.example; change both places together.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kapsora_app') THEN
        CREATE ROLE kapsora_app LOGIN NOBYPASSRLS NOSUPERUSER NOCREATEDB NOCREATEROLE
            PASSWORD 'kapsora_app_local';
    END IF;
END
$$;

SELECT 'CREATE DATABASE kapsora'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'kapsora')
\gexec
