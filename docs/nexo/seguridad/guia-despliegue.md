# Guía de despliegue: dos cuentas de base de datos, TLS, límites, tokens y evidencia (NX-07, fases S1 a S4)

Fecha: 2026-10-12. Requisitos: RS-01..RS-06 (§1 a §5), RS-10..RS-13 y RS-20..RS-24 (§6), RS-30..RS-34
(§7) (`requirements.md`).
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
| `NEXO_AUTH_LOCKOUT` | vacía: 20 tokens distintos rechazados desde una IP en 5 min bloquean 15 min; **sin bloqueo por persona** (A1, S4) | JSON `{"max_failures":20,"user_max_failures":0,"window_seconds":300,"lockout_seconds":900}`. `max_failures` (1 a 10 000) es la cantidad de **tokens distintos** inválidos (desconocidos o mal formados, también en el login del panel) por IP dentro de la ventana, no de intentos: el mismo token repetido suma una sola vez. `user_max_failures` (por defecto `0`, desactivado desde S4) cuenta intentos con un token revocado o de una persona deshabilitada; **no se recomienda activarlo**: quien conserve un token viejo de alguien podría dejarlo sin acceso. Esos intentos quedan auditados y generan la alerta `revoked_token_used` (§7). `off` desactiva todo el bloqueo |
| `NEXO_SUPERADMIN_ALLOWED_CIDRS` | vacía (sin restricción, con aviso al iniciar) | Redes (CIDR o IP, separadas por comas) desde las que se pueden usar `platform.admins.manage` y `platform.settings` (crear, promover o habilitar admins; tokens sin caducidad). Fuera de ellas: 403 auditado (`reason=network`) |
| `NEXO_TOKEN_MAX_DAYS` | `365` | Caducidad máxima al emitir un token. Por defecto se emiten con 90 días (o el máximo, si es menor) |

Grupos de rutas: `sync` = `/sync/*`; `admin` = `/admin/*`, `/nexo/admin/*`, `/nexo/authz/*`;
`login` = `POST /dashboard/login` y `/dashboard/bootstrap`; `nexo` = resto de `/nexo/*` (NX-10);
`other` = panel, estáticos y `/health`. Al superar un límite: `429` con `Retry-After` y
`error_code: rate_limited`; cada minuto queda un evento `nexo.rate_limited` por clave en la
auditoría. Un bloqueo por fallos responde `429` `auth_locked` y deja un evento `auth.lockout`.

Al iniciar, el log resume la configuración efectiva:

```text
[nexo] seguridad: limite de peticiones: sync=600/600 admin=120/120 login=20/- nexo=300/300 other=600/- (ip/token por minuto)
[nexo] seguridad: bloqueo por fallos de autenticacion: ip 20 tokens distintos en 5m0s bloquean 15m0s; sin bloqueo por persona (tokens revocados: alerta)
[nexo] seguridad: NEXO_SUPERADMIN_ALLOWED_CIDRS vacio: ... (RS-13)
[nexo] seguridad: tokens nuevos vencen a los 90 dias (maximo 365, NEXO_TOKEN_MAX_DAYS)
[nexo] seguridad: rechazos repetidos (misma huella, IP y motivo) se resumen cada 10m0s (NEXO_AUDIT_DEDUP_SECONDS)
```

Recomendaciones de producción:

- `NEXO_TRUSTED_PROXIES` con la IP del proxy (Caddy): sin ella todas las peticiones parecen venir
  del proxy y comparten el límite y el bloqueo por IP. Con ella, además, los enlaces de las citas
  del conocimiento salen con el esquema y el host públicos (`X-Forwarded-Proto` y
  `X-Forwarded-Host` del proxy); `NEXO_KB_PUBLIC_URL`, si está, sigue mandando.
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

## 7. Evidencia y monitoreo (fase S4)

Todo es aditivo: sin variables nuevas el servidor ya encadena la auditoría, resume los rechazos
repetidos, evalúa alertas (quedan en la base y en la API) y verifica la cadena cada hora; la
exportación, el webhook, la depuración por retención y la baja de cuentas inactivas se activan por
configuración.

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_AUDIT_DEDUP_SECONDS` | `600` | Ventana del resumen de rechazos repetidos (A2): el primer rechazo de cada (huella del token, IP, motivo) se escribe completo y los siguientes se cuentan en una fila resumen por ventana (`nexo_repeated`, `nexo_first_at`, `nexo_last_at`, `nexo_user_agents`). `0` = una fila por rechazo, como Engram |
| `NEXO_AUDIT_VERIFY_MINUTES` | `60` | Cada cuántos minutos el servidor verifica la cadena de hashes (alerta `audit_chain_broken`). `0` = solo a pedido |
| `NEXO_ALERT_RULES` | vacía: reglas por defecto (`design.md` §5.3 y §8.4); `logging_stalled` desactivada | JSON por regla: `{"authz_denied_burst":{"threshold":5,"window_seconds":60,"cooldown_seconds":600},"logging_stalled":{"enabled":true,"window_seconds":1800}}`; campos `enabled`, `threshold`, `window_seconds`, `cooldown_seconds`, `severity` (`info`, `warning`, `high`, `critical`). `off` las desactiva. Inválido: valores por defecto con aviso |
| `NEXO_ALERT_WEBHOOK_URL` | vacía (las alertas quedan en `nexo_alerts` y en `GET /nexo/admin/alerts`) | URL del webhook entrante de Slack o de Teams (conector clásico). Cuerpo JSON `{"text","summary","nexo_alert":{id, rule, severity, layer, subject, occurred_at}}`, sin detalles ni secretos; 3 intentos (0, 2 y 10 s). La URL suele llevar una clave: tratarla como secreto (el log solo muestra el host). `http://` se acepta con aviso (redes internas) |
| `NEXO_AUDIT_EXPORT_DIR` | vacía: **exportación desactivada** (aviso al iniciar) | Carpeta de la exportación diaria firmada: `<dir>/<tabla>/<AAAA-MM-DD>.jsonl` y `.jsonl.sig` (día UTC). Debe ser escribible por el usuario del contenedor (uid 10001) y, en producción, un volumen distinto del de la base que se copie fuera del host (por ejemplo a un almacenamiento con retención o WORM). Con varias réplicas, un volumen compartido |
| `NEXO_AUDIT_EXPORT_KEY` | — (sin ella la exportación queda desactivada) | Clave HMAC-SHA256 de las firmas, 32 caracteres o más (`openssl rand -hex 32`). Guardarla fuera de la base y fuera del alcance de la cuenta dueña (gestor de secretos); `key_id` en cada firma permite rotarla |
| `NEXO_DISABLE_INACTIVE_DAYS` | vacía (desactivado, DS-4) | Días sin uso tras los cuales el servidor deshabilita la cuenta (una vez por día; mínimo 7). Nunca al último superadmin. Deja `principal.disabled_inactive`, `user.disable` (`reason_code=inactive`) y la alerta `inactive_account_disabled` |
| `NEXO_RETENTION_AUDIT_DAYS` | vacía (no se depura) | Retención de `cloud_auth_audit_log`; mínimo 365 (un valor menor se eleva con aviso) |
| `NEXO_RETENTION_AUTHZ_EVENTS_DAYS` | vacía (no se depura) | Retención de `nexo_authz_events`; mínimo 365 |
| `NEXO_RETENTION_KB_EVENTS_DAYS` | vacía (no se depura) | Retención de `nexo_kb_events`; mínimo 365 (`nexo_kb_versions` no se depura nunca) |
| `NEXO_RETENTION_ALERTS_DAYS` | vacía (no se depura) | Retención de `nexo_alerts` y sus reconocimientos; mínimo 365 |

La actividad (`nexo_activity_log`) sigue con `NEXO_ACTIVITY_RETENTION_DAYS` (por defecto 180, mínimo
30). La depuración corre una vez por día con `nexo_purge_evidence(tabla, días)` (función de la cuenta
dueña): en las tablas encadenadas borra solo el inicio de la cadena y deja un ancla, así la
verificación sigue pasando.

Al iniciar, el log resume la configuración:

```text
[nexo] seguridad: rechazos repetidos (misma huella, IP y motivo) se resumen cada 10m0s (NEXO_AUDIT_DEDUP_SECONDS)
[nexo] seguridad: alertas activas: authz_denied_burst(5/1m0s) auth_failures_burst(10/5m0s) ...
[nexo] seguridad: webhook de alertas: hooks.slack.com
[nexo] seguridad: exportacion diaria firmada en /export (clave 3f2a9c1d)
[nexo] seguridad: verificacion de la cadena de hashes cada 1h0m0s
[nexo] seguridad: retencion: cloud_auth_audit_log=sin depurar nexo_authz_events=sin depurar ...
```

API (auditor, admin y superadmin; las acciones exigen además un token `write`):

| Ruta | Uso |
|---|---|
| `GET /nexo/admin/audit/verify[?table=]` | Verifica la cadena de `cloud_auth_audit_log`, `nexo_authz_events` y `nexo_kb_events`: `ok`, filas revisadas, primera fila alterada (`first_broken`), huecos y filas sin cadena |
| `GET /nexo/admin/alerts?rule=&layer=&severity=&unacked=true&from=&to=&cursor=&limit=` | Alertas con su capa (`plataforma`, `proyectos`, `conocimiento`) y reconocimiento |
| `POST /nexo/admin/alerts/{id}:ack` `{"note":"..."}` | Reconoce una alerta (una vez; 409 si ya estaba) |
| `GET /nexo/admin/access-review[?inactive_days=90&format=csv]` | Revisión de accesos: por persona, rol de plataforma y roles por ámbito con fecha y quién asignó, permisos de proyecto, tokens (caducidad, alcance, último uso), días de inactividad y marcas `inactive`, `token_no_expiry`, `token_never_used`. Con `format=csv` o `format=json` se descarga como evidencia con una huella SHA-256 (ver el manual de administración, 9.5) |
| `POST /nexo/admin/access-review:complete` `{"notes":"..."}` | Registra la revisión (quién, cuándo, notas y el hash del reporte revisado) |

CLI (en el contenedor o con `ENGRAM_DATABASE_URL` de la cuenta de aplicación):

```sh
engram nexo audit verify [--table cloud_auth_audit_log]          # sale con 1 si la cadena falla
engram nexo audit export --since 2026-10-01 [--until 2026-10-11]  # días ya exportados se saltan
engram nexo audit export --since today --include-today            # día en curso (parcial)
engram nexo audit verify-export /export/cloud_auth_audit_log/2026-10-11.jsonl --db
```

Recomendaciones de producción:

- Configurar `NEXO_AUDIT_EXPORT_DIR` y `NEXO_AUDIT_EXPORT_KEY`: es la única defensa frente a la
  cuenta dueña (que puede recalcular la cadena). Copiar la carpeta fuera del host y verificar con
  `verify-export --db` en la revisión periódica.
- Webhook hacia el canal del equipo de seguridad; revisar `GET /nexo/admin/alerts?unacked=true`.
- Revisión de accesos trimestral registrada con `access-review:complete` (ISO 27002 5.18).
- Retención: definir la política (por ejemplo 365 días en caliente y el archivo exportado hasta lo que
  exija la empresa) antes de activar `NEXO_RETENTION_*`.
