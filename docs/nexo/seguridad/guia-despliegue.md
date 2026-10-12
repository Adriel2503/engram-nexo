# Guía de despliegue: dos cuentas de base de datos, TLS, límites y tokens (NX-07, fases S1 a S3)

Fecha: 2026-10-12. Requisitos: RS-01..RS-06 (§1 a §5), RS-10..RS-13 y RS-20..RS-24 (§6) (`requirements.md`).
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

## 6. Abuso, escalada y tokens (fases S2 y S3)

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_RATE_LIMITS` | vacía: `sync` 600/600, `admin` 120/120, `login` 20/-, `nexo` 300/300, `other` 600/- (por IP / por token, por minuto) | JSON con los grupos a cambiar, por ejemplo `{"sync":{"ip":1200},"admin":{"token":240}}` (`0` = sin límite en esa dimensión); `off` desactiva el límite. Un JSON inválido se ignora con aviso y quedan los valores por defecto |
| `NEXO_AUTH_LOCKOUT` | vacía: 20 tokens distintos rechazados desde una IP, o 20 fallos de una persona, en 5 min bloquean 15 min | JSON `{"max_failures":20,"user_max_failures":20,"window_seconds":300,"lockout_seconds":900}`. `max_failures` (1 a 10 000) es la cantidad de **tokens distintos** inválidos (desconocidos o mal formados, también en el login del panel) por IP dentro de la ventana, no de intentos: el mismo token repetido suma una sola vez. `user_max_failures` cuenta intentos con un token revocado o de una persona deshabilitada (`0` desactiva el bloqueo por persona). `off` lo desactiva |
| `NEXO_SUPERADMIN_ALLOWED_CIDRS` | vacía (sin restricción, con aviso al iniciar) | Redes (CIDR o IP, separadas por comas) desde las que se pueden usar `platform.admins.manage` y `platform.settings` (crear, promover o habilitar admins; tokens sin caducidad). Fuera de ellas: 403 auditado (`reason=network`) |
| `NEXO_TOKEN_MAX_DAYS` | `365` | Caducidad máxima al emitir un token. Por defecto se emiten con 90 días (o el máximo, si es menor) |

Grupos de rutas: `sync` = `/sync/*`; `admin` = `/admin/*`, `/nexo/admin/*`, `/nexo/authz/*`;
`login` = `POST /dashboard/login` y `/dashboard/bootstrap`; `nexo` = resto de `/nexo/*` (NX-10);
`other` = panel, estáticos y `/health`. Al superar un límite: `429` con `Retry-After` y
`error_code: rate_limited`; cada minuto queda un evento `nexo.rate_limited` por clave en la
auditoría. Un bloqueo por fallos responde `429` `auth_locked` y deja un evento `auth.lockout`.

Al iniciar, el log resume la configuración efectiva:

```text
[nexo] seguridad: limite de peticiones: sync=600/600 admin=120/120 ... (ip/token por minuto)
[nexo] seguridad: bloqueo por fallos de autenticacion: ip 20 tokens distintos y usuario 20 fallos en 5m0s bloquean 15m0s
[nexo] seguridad: NEXO_SUPERADMIN_ALLOWED_CIDRS vacio: ... (RS-13)
[nexo] seguridad: tokens nuevos vencen a los 90 dias (maximo 365, NEXO_TOKEN_MAX_DAYS)
```

Recomendaciones de producción:

- `NEXO_TRUSTED_PROXIES` con la IP del proxy (Caddy): sin ella todas las peticiones parecen venir
  del proxy y comparten el límite y el bloqueo por IP.
- `NEXO_SUPERADMIN_ALLOWED_CIDRS` con la red de administración o la VPN (ASVS 8.4.2).
- Con muchas PCs detrás de un mismo NAT, subir `sync.ip` (600/min alcanza para unas 100 PCs con
  autosync). Los límites son por réplica: con varias réplicas, el efectivo se multiplica.
- Una PC con un token mal pegado o de un usuario eliminado reintenta cada ~30 s: suma un solo token
  al contador de su IP y no bloquea a la oficina. Cada rechazo queda auditado (`sync.auth`,
  `unknown_token`, con la IP y el User-Agent): ese es el rastro para encontrar la PC.

Tokens (fase S3):

- Emisión: `POST /admin/users/{id}/tokens` con `{"name":"...","expires_in_days":90,"scope":"read|write"}`,
  el formulario del panel (campos de caducidad y alcance) o `POST /nexo/authz/v1/tokens`
  `{"principal":"ana","expires_in_days":30,"scope":"read"}`. `expires_in_days: 0` (sin caducidad)
  solo para cuentas de servicio y solo un superadmin (y desde las redes permitidas).
- `engram cloud bootstrap admin --issue-token` emite con 90 días: el superadmin debe emitirse un
  token nuevo antes de que venza.
- Los tokens emitidos antes de NX-07 no tienen caducidad y siguen funcionando; se ven con
  `expires_at: null` en `/admin/users/{id}/tokens` y deben rotarse (RS-23; reporte en S4).
- Un token `read` descarga y consulta, pero no cambia nada: en las rutas de Engram solo usa `GET` y
  `HEAD` (más `POST /sync/prompt-source-attestations/verify`, que solo lee); cualquier otra petición
  (`/sync/push`, `/sync/mutations/push`, `/sync/session-authorities`, `/sync/prompt-pair-claims`,
  `/sync/prompt-source-attestations` y las que se agreguen) recibe 403 "read-only". En `/admin/*` y
  `/nexo/*` solo tiene las capacidades de lectura. Una sesión del panel abierta con un token `read`
  puede mirar pero no administrar ni pausar la sincronización (403). Un token vencido recibe
  `401 unauthorized: token_expired: ...`; la sesión del panel se cierra en la siguiente petición.
- `GET /nexo/authz/v1/me` incluye `token` con `scope`, `expires_at`, `no_expiry` y `last_used_at`.
