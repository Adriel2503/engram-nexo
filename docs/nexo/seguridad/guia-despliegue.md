# Guía de despliegue: dos cuentas de base de datos y TLS (NX-07, fase S1)

Fecha: 2026-10-12. Requisitos: RS-01, RS-02, RS-03, RS-04, RS-05, RS-06 (`requirements.md`).
Plantillas: `deploy/nexo/compose.example.yaml`, `deploy/nexo/postgres-init/01-cuentas.sh`,
`deploy/nexo/Caddyfile.example`.

## 1. Qué cambia

| Variable | Cuenta | Uso |
|---|---|---|
| `ENGRAM_DATABASE_URL` | `nexo_app` | Todas las peticiones. No migra |
| `NEXO_MIGRATION_DATABASE_URL` (opcional) | `nexo_owner` | Solo al iniciar: migraciones de Engram y Nexo, privilegios y función de retención |
| `NEXO_DB_APP_ROLE` (opcional) | | Rol al que se otorgan los privilegios si no coincide con el usuario de `ENGRAM_DATABASE_URL` (por ejemplo, detrás de un pooler). Por defecto, el usuario de esa URL |

Al iniciar con las dos variables, el servidor:

1. migra con `nexo_owner` (que queda dueña de todas las tablas, vistas y funciones);
2. aplica los privilegios de `nexo_app` (idempotente): lectura y escritura en las tablas operativas,
   solo `SELECT` e `INSERT` en las de evidencia, solo lectura en vistas, `EXECUTE` en
   `nexo_purge_activity`, sin `CREATE` en el esquema; si `nexo_owner` es dueña de la base, quita
   `CREATE` y `TEMPORARY` a `PUBLIC`;
3. verifica que `nexo_app` no sea superusuario, ni dueña (o miembro de la dueña) de objetos del
   esquema, ni tenga `CREATEROLE`, `CREATEDB` o `CREATE` en el esquema o la base. Si falla, **no
   arranca** y el log dice por qué;
4. registra `[nexo] seguridad: dos cuentas de base de datos (dueña ..., aplicación ...)`.

Sin `NEXO_MIGRATION_DATABASE_URL`, todo funciona como antes y el log dice
`[nexo] seguridad degradada: una sola cuenta de base de datos (ver docs/nexo/seguridad)`.

## 2. Despliegue nuevo

```sh
cp deploy/nexo/compose.example.yaml deploy/nexo/compose.yaml
# .env junto al compose (no versionado): POSTGRES_PASSWORD, NEXO_OWNER_PASSWORD, NEXO_APP_PASSWORD,
# ENGRAM_CLOUD_TOKEN, ENGRAM_JWT_SECRET, ENGRAM_CLOUD_TOKEN_PEPPER, NEXO_DOMAIN
# Contraseñas seguras para URL, por ejemplo: openssl rand -hex 24
docker compose -f deploy/nexo/compose.yaml up -d
docker compose -f deploy/nexo/compose.yaml logs cloud | grep "\[nexo\] seguridad"
```

`01-cuentas.sh` corre una sola vez, al crear el volumen de Postgres: crea `nexo_owner` (dueña de
la base `engram_cloud` y del esquema `public`) y `nexo_app` (solo `CONNECT` y `USAGE`). El
superusuario `postgres` queda solo para administración y no lo usa el servidor.

## 3. Pasar un despliegue existente (una cuenta) a dos cuentas

Con la cuenta actual (por ejemplo `engram`, dueña de las tablas) y un respaldo hecho:

```sql
-- como superusuario, conectado a la base de Engram
CREATE ROLE nexo_owner LOGIN PASSWORD '...' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE ROLE nexo_app   LOGIN PASSWORD '...' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
REASSIGN OWNED BY engram TO nexo_owner;          -- tablas, vistas, funciones y secuencias
ALTER DATABASE engram_cloud OWNER TO nexo_owner;
ALTER SCHEMA public OWNER TO nexo_owner;
REVOKE ALL ON DATABASE engram_cloud FROM PUBLIC;
GRANT CONNECT ON DATABASE engram_cloud TO nexo_app;
```

Luego `ENGRAM_DATABASE_URL` con `nexo_app`, `NEXO_MIGRATION_DATABASE_URL` con `nexo_owner` y
reiniciar. La primera vez el servidor aplica los privilegios. Si `engram` es superusuario, conviene
dejarlo solo para administración (o quitarle `LOGIN`).

Volver atrás: quitar `NEXO_MIGRATION_DATABASE_URL` y poner en `ENGRAM_DATABASE_URL` la cuenta dueña.
El servidor vuelve al modo de una cuenta (con el aviso).

## 4. TLS hacia Postgres (RS-05)

En demostración local, con Postgres en la misma red interna de Docker y sin puertos publicados,
se puede seguir con `sslmode=disable`. En producción, y siempre que la base esté en otro host o
en un servicio administrado, usar `sslmode=verify-full`: cifra y además comprueba que el
certificado lo firmó la CA indicada y que el nombre del host coincide.

1. Certificados (o los de la CA corporativa):

   ```sh
   mkdir -p pg-tls && cd pg-tls
   openssl req -new -x509 -days 825 -nodes -subj "/CN=nexo-pg-ca" -keyout ca.key -out ca.crt
   openssl req -new -nodes -subj "/CN=postgres" -keyout server.key -out server.csr
   printf "subjectAltName=DNS:postgres,DNS:db.ejemplo.interno\n" > san.ext
   openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -extfile san.ext -out server.crt
   chmod 600 server.key   # Postgres exige que la llave no sea legible por otros (dueño uid 70 en alpine)
   ```

2. Postgres con `ssl=on` (en el compose de ejemplo, descomentar `command` y el volumen `pg-tls`).
   Para rechazar conexiones sin TLS, en `pg_hba.conf` usar `hostssl` en lugar de `host`.
3. En ambas URL del servidor:
   `postgres://nexo_app:...@postgres:5432/engram_cloud?sslmode=verify-full&sslrootcert=/tls/ca.crt`
   (y montar `ca.crt` en el contenedor `cloud`). El nombre del host de la URL debe estar en el
   `subjectAltName` del certificado.
4. Comprobar: `SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid();` desde la cuenta
   de aplicación debe devolver `t`.

Respaldos: cifrarlos (por ejemplo `pg_dump ... | age -r <clave> > respaldo.sql.age`) y guardarlos
fuera del host de la base.

## 5. Verificación rápida (SQL directo, CA-S01)

Con la cuenta `nexo_app`:

```sql
UPDATE nexo_activity_log SET actor = actor;                 -- ERROR: permission denied
DELETE FROM cloud_auth_audit_log;                           -- ERROR: permission denied
TRUNCATE cloud_mutations;                                   -- ERROR: permission denied
ALTER TABLE nexo_authz_events DISABLE TRIGGER ALL;          -- ERROR: must be owner
CREATE TABLE intruso (id int);                              -- ERROR: permission denied for schema public
CREATE TEMP TABLE intruso (id int);                         -- ERROR: permission denied to create temporary tables
SELECT nexo_purge_activity(10);                             -- ERROR: nexo_retention_minimum
```

La prueba de punta a punta está en el repositorio de Nexo: `06-pruebas/seguridad/Run-Seguridad.ps1`.
