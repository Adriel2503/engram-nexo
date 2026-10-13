#!/bin/sh
# Nexo IA · NX-07 fase S1 (RS-01, RS-06): crea las dos cuentas de Postgres.
#
#   nexo_owner : dueña de la base y del esquema public. Solo la usa el servidor
#                al iniciar para migrar, aplicar privilegios y crear la función
#                de retención (NEXO_MIGRATION_DATABASE_URL).
#   nexo_app   : cuenta de aplicación (ENGRAM_DATABASE_URL). Sin superusuario,
#                sin propiedad de objetos y sin derecho a crear objetos. Los
#                privilegios por tabla los aplica el servidor al iniciar
#                (internal/nexo/security, ApplyGrants).
#
# La imagen oficial de Postgres ejecuta este archivo una sola vez, al crear el
# volumen de datos (/docker-entrypoint-initdb.d). Es idempotente por si se
# ejecuta a mano sobre una base existente:
#
#   docker compose exec -T -e NEXO_OWNER_PASSWORD -e NEXO_APP_PASSWORD postgres \
#     sh /docker-entrypoint-initdb.d/01-cuentas.sh
#
# Variables (las contraseñas nunca se escriben en archivos versionados):
#   NEXO_OWNER_PASSWORD, NEXO_APP_PASSWORD  obligatorias
#   NEXO_DB_NAME     (engram_cloud)  NEXO_OWNER_ROLE (nexo_owner)  NEXO_APP_ROLE (nexo_app)
set -eu

: "${NEXO_OWNER_PASSWORD:?Falta NEXO_OWNER_PASSWORD}"
: "${NEXO_APP_PASSWORD:?Falta NEXO_APP_PASSWORD}"
NEXO_DB_NAME="${NEXO_DB_NAME:-engram_cloud}"
NEXO_OWNER_ROLE="${NEXO_OWNER_ROLE:-nexo_owner}"
NEXO_APP_ROLE="${NEXO_APP_ROLE:-nexo_app}"

psql -v ON_ERROR_STOP=1 --no-psqlrc --username "${POSTGRES_USER:-postgres}" --dbname postgres \
	-v owner="$NEXO_OWNER_ROLE" -v app="$NEXO_APP_ROLE" -v db="$NEXO_DB_NAME" \
	-v owner_pw="$NEXO_OWNER_PASSWORD" -v app_pw="$NEXO_APP_PASSWORD" <<'EOSQL'
-- Cuentas (sin superusuario, sin crear bases ni roles).
SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', :'owner')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'owner') \gexec
SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT', :'app')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'app') \gexec
ALTER ROLE :"owner" PASSWORD :'owner_pw';
ALTER ROLE :"app" PASSWORD :'app_pw';

-- Base de la cuenta dueña. Nadie más crea esquemas ni tablas temporales; la
-- aplicación solo se conecta.
SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'owner')
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'db') \gexec
SELECT format('ALTER DATABASE %I OWNER TO %I', :'db', :'owner') \gexec
SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', :'db') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'db', :'app') \gexec

\connect :"db"
-- Esquema de la cuenta dueña; la aplicación solo lo usa.
ALTER SCHEMA public OWNER TO :"owner";
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO :"app";

-- NX-10 R2: búsqueda por significado. La extensión vector (pgvector) solo la
-- crea un superusuario; la cuenta dueña no puede. Si la imagen la trae
-- (pgvector/pgvector:pg16), se crea aquí; si no, el servidor funciona solo
-- con búsqueda por texto.
SELECT 'CREATE EXTENSION IF NOT EXISTS vector SCHEMA public'
 WHERE EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') \gexec
EOSQL

echo "nexo: cuentas $NEXO_OWNER_ROLE (dueña) y $NEXO_APP_ROLE (aplicación) listas en la base $NEXO_DB_NAME"
