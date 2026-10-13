# Endurecimiento de seguridad: diseño técnico

Estado: borrador para revisión. Fecha: 2026-10-12. Requisitos: `requirements.md` (NX-07).

## 1. Principios

1. **Defensa en profundidad**: la base de datos impide lo que el código no debería hacer (privilegios),
   y el código impide lo que la base no puede expresar (roles, límites).
2. **Compatible por defecto**: sin configuración nueva, el servidor funciona como hoy y avisa de lo
   que falta. La seguridad completa se activa por configuración documentada.
3. **Aislamiento**: código en `internal/nexo/security` (límites, alertas, cadena, exportación,
   revisión) y archivos `nexo_*.go`; enganches en Engram de una línea marcados `// NX-07`.

## 2. Fase S1. Base de datos

### 2.1 Dos cuentas

| Variable | Cuenta | Uso |
|---|---|---|
| `ENGRAM_DATABASE_URL` | `nexo_app` (aplicación) | Todas las peticiones |
| `NEXO_MIGRATION_DATABASE_URL` (nuevo, opcional) | `nexo_owner` (dueña del esquema) | Solo al iniciar: migraciones de Engram y Nexo, privilegios, funciones de retención |

Arranque (enganche `// NX-07` en la creación del `CloudStore`):

1. Si existe `NEXO_MIGRATION_DATABASE_URL`: abrir conexión dueña, ejecutar `migrate` (Engram) y
   `nexoMigrate`, aplicar `nexoApplyGrants(appRole)` y cerrar. Luego abrir la conexión de aplicación
   y **no** ejecutar migraciones con ella.
2. Si no existe: comportamiento actual (migra con la única cuenta) y registro
   `seguridad degradada: una sola cuenta de base de datos (ver docs/nexo/seguridad)`.
3. Al iniciar con dos cuentas, verificación: la cuenta de aplicación no debe ser superusuario ni dueña
   de tablas (`pg_roles.rolsuper`, `pg_tables.tableowner`); si lo es, el servidor no arranca.

### 2.2 Privilegios (`nexoApplyGrants`, idempotente)

```sql
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO nexo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO nexo_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO nexo_app;
-- tablas de evidencia: solo leer y agregar
REVOKE UPDATE, DELETE, TRUNCATE ON
  cloud_auth_audit_log, cloud_mutations, nexo_activity_log, nexo_authz_events, nexo_alerts,
  nexo_audit_exports FROM nexo_app;
-- columnas que el sistema sí actualiza en tablas de evidencia: ninguna (se verifica en pruebas)
ALTER DEFAULT PRIVILEGES FOR ROLE nexo_owner IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nexo_app;
```

La lista de tablas de evidencia es una constante en Go y NX-10 agrega las suyas
(`nexo_kb_events`, `nexo_kb_versions`). Antes de cerrar la fase se revisa si Engram hace `UPDATE` o
`DELETE` sobre `cloud_mutations` o `cloud_auth_audit_log` en algún camino (por ejemplo, compactación o
pausa de proyectos); si lo hace, esa tabla queda con el privilegio mínimo que ese camino necesita y se
documenta.

#### 2.2.1 Inventario de escrituras (S1-01, resultado)

Búsqueda de `UPDATE`, `DELETE`, `TRUNCATE`, `ON CONFLICT ... DO UPDATE` y bloqueos `FOR UPDATE` /
`FOR NO KEY UPDATE` (que exigen privilegio `UPDATE`) en `internal/cloud/**`, `internal/nexo/**` y
`cmd/engram` (código que no es de prueba), más los caminos de arranque (`migrate`,
`backfillAllowedProjectMutationChunks`).

| Tabla | Escrituras en ejecución | Dónde | Clase |
|---|---|---|---|
| `cloud_principals` | `UPDATE` rol y habilitado; `FOR UPDATE` | `cloudstore/identity.go` (222, 407, 415, 624, 641, 812, 1150), `authz/service.go` (122, 280) | Operativa |
| `cloud_principal_tokens` | `UPDATE last_used_at`, revocación; `FOR UPDATE` | `identity.go` (570, 584, 823, 835, 849) | Operativa |
| `cloud_project_grants` | upsert `DO UPDATE`, `DELETE` | `identity.go` (873, 914) | Operativa |
| `cloud_project_controls` | upsert `DO UPDATE` (pausa de proyectos) | `project_controls.go` (69) | Operativa |
| `cloud_users` (legado) | upsert `DO UPDATE` | `cloudstore.go` (101) | Operativa |
| `nexo_actor_presence` | upsert `DO UPDATE` | `nexo_activity.go` (106) | Operativa |
| `nexo_role_bindings` | upsert, `DELETE`; `FOR UPDATE` | `authz/service.go` (248, 269, 320, 648, 765) | Operativa |
| `nexo_scopes` | `UPDATE`; `FOR UPDATE` | `authz/service.go` (513, 528, 559, 570, 599, 739) | Operativa |
| `cloud_chunks` | solo `INSERT ... ON CONFLICT DO NOTHING` | `cloudstore.go` (998, 1102) | **Evidencia** |
| `cloud_mutations` | solo `INSERT` | `cloudstore.go` (509, 975) | **Evidencia** |
| `cloud_auth_audit_log` | solo `INSERT` | `identity.go` (`insertAuthAuditEvent`) | **Evidencia** |
| `cloud_sync_audit_log` | solo `INSERT` | `audit_log.go` (86) | **Evidencia** |
| `nexo_activity_log` | solo `INSERT`; `DELETE` solo por la depuración (función de §2.3) | `nexo_activity.go` | **Evidencia** |
| `nexo_authz_events` | solo `INSERT` | `authz/pgstore.go`, `authz/migrate.go` | **Evidencia** |
| `cloud_project_sessions`, `cloud_session_authority`, `cloud_prompt_pair_claims`, `cloud_prompt_source_attestations` | solo `INSERT` (algunas con `DO NOTHING`) | `cloudstore.go`, `session_authority.go`, `prompt_pair_claim.go`, `prompt_source_attestation.go` | Operativa (sin cambios) |

Resultado:

- Engram **no** actualiza ni borra `cloud_mutations`, `cloud_chunks`, `cloud_auth_audit_log` ni
  `cloud_sync_audit_log` en ejecución. No hay compactación de chunks ni de mutaciones en el
  servidor; la pausa de proyectos solo escribe `cloud_project_controls`; el borrado de un recuerdo
  viaja como mutación `op=delete` (un `INSERT`). Las únicas sentencias `UPDATE` sobre
  `cloud_mutations` y `cloud_chunks` están en `migrate()` (normalización de columnas heredadas), que
  con dos cuentas corre con la cuenta dueña. **No hace falta ninguna excepción.**
- Ninguna tabla de evidencia participa en un `SELECT ... FOR UPDATE`; las claves foráneas que las
  referencian se comprueban con los privilegios del dueño de la tabla.
- Lista final (`security.EvidenceTables`, `internal/nexo/security/dbaccounts.go`):
  `cloud_auth_audit_log`, `cloud_sync_audit_log`, `cloud_mutations`, `cloud_chunks`,
  `nexo_activity_log`, `nexo_authz_events` y, cuando existan, `nexo_alerts`, `nexo_alert_acks`,
  `nexo_audit_exports`, `nexo_access_reviews` (S4) y `nexo_kb_events`, `nexo_kb_versions` (NX-10).
  Las que todavía no existen se omiten; al crearlas, el siguiente arranque las protege (también
  `ALTER DEFAULT PRIVILEGES` les da `SELECT/INSERT/UPDATE/DELETE` hasta ese arranque, que retira
  `UPDATE` y `DELETE`).
- Candidata para más adelante: `cloud_prompt_source_attestations` (solo inserción). Queda fuera por
  ahora porque es un detalle interno de Engram sin uso de auditoría en Nexo.

#### 2.2.2 Privilegios aplicados (implementación)

`security.ApplyGrants`, con la cuenta dueña, en una transacción con advisory lock, en cada arranque:

1. Esquema actual (`current_schema()` de la cuenta dueña, no un `public` fijo): `REVOKE ALL ... FROM
   PUBLIC`, `REVOKE ALL ... FROM app`, `GRANT USAGE ... TO app`. Exige que la cuenta dueña sea dueña
   del esquema.
2. `REVOKE ALL` y luego `GRANT SELECT, INSERT, UPDATE, DELETE` en todas las tablas; `USAGE, SELECT` en
   todas las secuencias; `ALTER DEFAULT PRIVILEGES` para lo que la dueña cree después.
3. Vistas (`nexo_mutation_history`): solo `SELECT`.
4. Tablas de evidencia existentes: `REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER`.
5. `nexo_purge_activity(integer)`: `REVOKE ALL FROM PUBLIC`, `GRANT EXECUTE TO app`.
6. Si la cuenta dueña es dueña de la base: `REVOKE CREATE, TEMPORARY ON DATABASE ... FROM PUBLIC` y de
   la aplicación, `GRANT CONNECT TO app`.

El rol de aplicación es el usuario de `ENGRAM_DATABASE_URL` (`current_user` de esa conexión) o
`NEXO_DB_APP_ROLE` si se define.

### 2.3 Retención segura

Se elimina el bypass por `nexo.allow_retention`. En su lugar:

```sql
CREATE OR REPLACE FUNCTION nexo_purge_activity(retention_days int) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE n bigint;
BEGIN
  IF retention_days < 30 THEN RAISE EXCEPTION 'retention below minimum'; END IF;
  PERFORM set_config('nexo.purge_in_progress', 'on', true);
  DELETE FROM nexo_activity_log WHERE occurred_at < now() - make_interval(days => retention_days);
  GET DIAGNOSTICS n = ROW_COUNT;
  INSERT INTO nexo_authz_events (action, details, ...) VALUES ('retention.purged', ...);
  RETURN n;
END $$;
REVOKE ALL ON FUNCTION nexo_purge_activity(int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION nexo_purge_activity(int) TO nexo_app;
```

La función pertenece a `nexo_owner`; el trigger de inmutabilidad permite `DELETE` solo cuando
`current_user = 'nexo_owner'` dentro de esa función y la fila es más antigua que el mínimo, de modo que
ni la aplicación ni una variable de sesión pueden borrar filas recientes. Con una sola cuenta, se
mantiene el trigger con la comprobación de antigüedad.

Implementado (`security.MigrateRetention` y `nexo_activity_log_immutable` en
`cloudstore/nexo_migrations.go`): el trigger deja pasar un `DELETE` solo si se cumplen a la vez
(a) `current_user` es el dueño de la tabla (se compara con `pg_class.relowner`, no con un nombre
fijo), (b) `nexo.purge_in_progress = 'on'` (lo pone la función; es una marca para obligar a pasar por
ella y dejar evento, no una frontera de seguridad) y (c) la fila tiene más de 30 días. `UPDATE` y
`TRUNCATE` siempre se rechazan. La función fija `search_path` al esquema actual, rechaza plazos
menores a 30 días (`nexo_retention_minimum`) y, si borró filas, deja `retention.purged` en
`nexo_authz_events` con actor `sistema` y `details = {table, retention_days, deleted, session_user}`.
`PurgeActivityOlderThan` solo llama a la función; `NEXO_ACTIVITY_RETENTION_DAYS` menor que 30 se
eleva a 30 con un aviso en el log.

### 2.4 TLS y despliegue

- Documentar `sslmode=verify-full` con `sslrootcert` para producción.
- `deploy/nexo/postgres-init/01-cuentas.sql` crea `nexo_owner` y `nexo_app` con contraseñas de
  variables de entorno; `deploy/nexo/compose.example.yaml` muestra el despliegue con dos cuentas,
  Caddy y TLS.

## 3. Fase S2. Abuso y escalada

### 3.1 Límite de peticiones

- Middleware `nexoRateLimit` dentro de `nexoHandler` (después de resolver la IP real de NX-04).
- Algoritmo: token bucket en memoria por clave (`ip:<ip>` y `token:<token_id>` cuando ya se
  autenticó), con expiración de claves inactivas. Grupos y valores por defecto:

| Grupo | Rutas | Por IP | Por token |
|---|---|---|---|
| sync | `/sync/*` | 600/min | 600/min |
| admin | `/admin/*`, `/nexo/admin/*`, `/nexo/authz/*` | 120/min | 120/min |
| login | login del panel | 20/min | – |
| nexo | `/nexo/kb/*` (NX-10) | 300/min | 300/min |
| resto | dashboard y estáticos | 600/min | – |

- Configurable con `NEXO_RATE_LIMITS` (JSON) y desactivable con `NEXO_RATE_LIMITS=off`.
- Respuesta 429 con `Retry-After` y cuerpo con `error_code: rate_limited`. Se cuenta, no se registra
  cada 429 (para no inundar); se emite un evento agregado por minuto y clave.

### 3.2 Bloqueo por fallos de autenticación

- Contador por IP y por usuario de fallos de autenticación (token inválido, vencido, login fallido).
  Umbral por defecto: 20 en 5 minutos. Bloqueo de 15 minutos con respuesta 429 genérica.
- Cada bloqueo es un evento `auth.lockout` y dispara la regla de alerta correspondiente.

### 3.3 Sin auto-asignación por Engram

- Enganche `// NX-07` en `handleAdminCreateGrant` y `handleDashboardCreateManagedGrant`: si el
  principal objetivo es quien hace la petición, 403 `self_grant_forbidden` con evento. Además exige
  `platform.users.manage`.
- La compatibilidad "permiso de Engram = publicador" se mantiene para permisos dados por otra persona.

### 3.4 Redes permitidas para administración de plataforma

- `NEXO_SUPERADMIN_ALLOWED_CIDRS`: lista de CIDR. `authz.Can` recibe la IP del `Identity` y niega
  `platform.admins.manage` y `platform.settings` fuera de esas redes (`Decision.Reason = network`).

## 4. Fase S3. Tokens (NX-06)

### 4.1 Datos

Columnas nuevas en `cloud_principal_tokens` (migración Nexo, aditiva):

| Columna | Tipo | Notas |
|---|---|---|
| `nexo_expires_at` | TIMESTAMPTZ NULL | NULL = sin caducidad (tokens existentes, o cuentas de servicio por decisión de superadmin) |
| `nexo_scope` | TEXT NOT NULL DEFAULT 'write' | CHECK `read`, `write` |

### 4.2 Validación

- Enganche `// NX-07` en la resolución del token administrado (`cloudstoreManagedTokenLookup` /
  `ResolveBearerToken`): si `nexo_expires_at <= now()` se rechaza con 401 `token_expired`, evento y
  regla de alerta. La comprobación va en la misma consulta que ya valida `revoked_at`.
- `Identity.TokenScope` se llena desde `nexo_scope`. Para las rutas de Engram que escriben
  (`/sync/push`, `/sync/mutations/push`) se exige `write` (enganche en la variante de
  `authorizeProjectScope`); `authz.Can` ya recorta por alcance en `/nexo/*` y `/admin/*`.

### 4.3 Emisión

- `/admin/*` (Engram), panel y `/nexo/authz/v1` aceptan `expires_in_days` (por defecto 90, máximo
  `NEXO_TOKEN_MAX_DAYS`, 365) y `scope`. "Sin caducidad" solo para cuentas de servicio y solo
  superadmin. El panel muestra caducidad y alcance.
- `engram cloud bootstrap admin --issue-token` emite con 90 días.

## 5. Fase S4. Evidencia y monitoreo

### 5.1 Cadena de hashes

- Columnas `nexo_prev_hash BYTEA`, `nexo_hash BYTEA` en `cloud_auth_audit_log` y
  `nexo_authz_events` (y `nexo_kb_events` en NX-10).
- Trigger `BEFORE INSERT` que toma `pg_advisory_xact_lock` por tabla, lee el último `nexo_hash`, y
  calcula `nexo_hash = sha256(prev_hash || representación canónica de la fila)` (función
  `nexo_canonical_row` estable: columnas en orden fijo, JSON normalizado).
- Verificación: `engram nexo audit verify [--table] [--from]` y `GET /nexo/admin/audit/verify`
  (auditor, superadmin). Reporta la primera fila que no coincide y los huecos de `id`.
- Las filas anteriores a la migración forman un "ancla" inicial (hash de la última fila previa) para
  no recalcular el pasado.

### 5.2 Exportación firmada

- Tarea diaria (y `engram nexo audit export --since`): exporta los eventos del día de las tablas de
  evidencia a `NEXO_AUDIT_EXPORT_DIR/<tabla>/<fecha>.jsonl` con un archivo `.sig` (HMAC-SHA256 con
  `NEXO_AUDIT_EXPORT_KEY`) y el último hash de la cadena. Registro en `nexo_audit_exports`.
- `engram nexo audit verify-export <archivo>` comprueba firma y continuidad con la cadena.

### 5.3 Alertas

- Tabla `nexo_alerts` (solo inserción para la aplicación): `rule`, `severity`, `subject`,
  `details`, `occurred_at`, `acknowledged_by`, `acknowledged_at` (el reconocimiento es un evento
  aparte para no necesitar `UPDATE`: `nexo_alert_acks`).
- Evaluador cada minuto (por réplica con advisory lock para que corra uno solo) sobre las tablas de
  eventos. Reglas iniciales:

| Regla | Condición por defecto |
|---|---|
| `authz_denied_burst` | 5 denegaciones de una misma persona o IP en 1 minuto |
| `auth_failures_burst` | 10 fallos de autenticación de una IP en 5 minutos |
| `platform_role_changed` | Cualquier cambio de rol de plataforma |
| `shared_token_used` | Uso del token compartido (`no-identificado`) |
| `expired_token_used` | Intentos con token vencido |
| `audit_chain_broken` | Verificación periódica de la cadena falla |
| `logging_stalled` | Sin eventos de actividad en N minutos con sincronizaciones esperadas (configurable, desactivada por defecto) |

- Webhook opcional `NEXO_ALERT_WEBHOOK_URL` (JSON compatible con Teams y Slack), con reintentos.
- API: `GET /nexo/admin/alerts`, `POST /nexo/admin/alerts/{id}:ack`.

### 5.4 Revisión de accesos y retención

- `GET /nexo/admin/access-review`: por persona, roles de plataforma y de ámbito con fecha de
  asignación y quién la hizo, tokens con caducidad, alcance y último uso, días de inactividad;
  marcas para tokens sin caducidad e inactivos.
- `POST /nexo/admin/access-review:complete` registra la revisión (quién, cuándo, observaciones).
- `NEXO_DISABLE_INACTIVE_DAYS` (vacío = desactivado): deshabilita cuentas sin uso, con evento y alerta.
- Retención por tabla en variables (`NEXO_RETENTION_*`), aplicada con funciones `SECURITY DEFINER`
  como en 2.3, con mínimos: actividad 30 días (por defecto 180), auditoría y eventos 365 días.

## 6. Fase S5. Respuestas y pruebas

- Revisión de todas las respuestas de `/admin/*`, panel y `/nexo/*`; tipos de respuesta explícitos
  (sin serializar estructuras internas completas).
- Registro de rutas Nexo con su capacidad (`nexoRoute(method, path, capability, handler)`); prueba
  que recorre el mux y falla si una ruta Nexo no está en el registro o si aparece una ruta de Engram
  nueva no listada en una lista revisada.
- Prueba que captura los logs y los eventos de una corrida E2E y falla si encuentra un token
  `egc_live_`, un hash de token o una cabecera `Authorization`.
- Workflow de CI (`.github/workflows/nexo-security.yml`) con Postgres de servicio que corre las
  pruebas de autorización y seguridad como puerta.

## 7. Impacto en otras especificaciones

- Roles (NX-05): R4 queda cubierta por S3; `Identity` incorpora IP para RS-13.
- Conocimiento (NX-10): sus tablas de evidencia entran en la lista de RS-02 y en la cadena de hashes;
  sus rutas usan el registro de rutas y el límite del grupo `nexo`.
- Trazabilidad (NX-02): la depuración pasa a la función segura.
- `NEXO_CHANGES.md`: nueva sección NX-07 con variables de entorno y enganches.

## 8. Desviaciones y notas de implementación

### 8.1 Fase S1

Fecha: 2026-10-12. Implementación de S1-01..S1-06 en `feat/seguridad` (sin commit).

| ID | Diseño | Implementado | Motivo |
|---|---|---|---|
| D-S1-1 | Evidencia: `cloud_auth_audit_log`, `cloud_mutations`, `nexo_activity_log`, `nexo_authz_events`, `nexo_alerts`, `nexo_audit_exports` | Se agregan `cloud_sync_audit_log` y `cloud_chunks` (el inventario §2.2.1 muestra que en ejecución solo se insertan) y los nombres futuros `nexo_alert_acks`, `nexo_access_reviews`, `nexo_kb_events`, `nexo_kb_versions`; las tablas que no existen se omiten | Privilegio mínimo; NX-10 y S4 quedan cubiertos sin tocar código |
| D-S1-2 | `GRANT ... IN SCHEMA public` | Se usa el esquema actual de la cuenta dueña (`current_schema()`), y se exige que ambas cuentas usen el mismo | Las pruebas usan un esquema por prueba (`search_path`) y un despliegue puede no usar `public` |
| D-S1-3 | `REVOKE UPDATE, DELETE, TRUNCATE` en evidencia | Además `REFERENCES` y `TRIGGER`; vistas solo `SELECT`; `REVOKE CREATE, TEMPORARY ON DATABASE` a `PUBLIC` y a la aplicación cuando la dueña es dueña de la base | "Sin derecho a crear objetos" (RS-01) incluye tablas temporales y triggers |
| D-S1-4 | Rol de aplicación `nexo_app` | `current_user` de `ENGRAM_DATABASE_URL`, o `NEXO_DB_APP_ROLE` | Nombres de cuentas libres; poolers con usuario distinto |
| D-S1-5 | Verificación: no superusuario ni dueña de tablas | También rechaza: miembro de la dueña (podría `SET ROLE`), `CREATEROLE`, `CREATEDB`, `CREATE` en esquema o base, y la misma cuenta en ambas URL. `TEMPORARY` y `BYPASSRLS` solo generan advertencia | Cada una de esas condiciones permite saltar los privilegios |
| D-S1-6 | Trigger: `current_user = 'nexo_owner'` | `current_user` igual al dueño de la tabla (`pg_class.relowner`), más la marca `nexo.purge_in_progress` y antigüedad mayor a 30 días | Funciona con cualquier nombre de cuenta y en modo de una cuenta; la marca obliga a pasar por la función (y su evento), pero la frontera real es el privilegio y la antigüedad |
| D-S1-7 | Evento de purga siempre | Evento `retention.purged` solo si se borraron filas (`details.deleted > 0`), con `session_user` | Evitar un evento diario vacío; la acción se agregó a `authz.EventActions` (filtrable en `/nexo/admin/authz-events`) |
| D-S1-8 | Plazo configurable libre | `NEXO_ACTIVITY_RETENTION_DAYS` entre 1 y 29 se eleva a 30 con aviso | La función rechaza plazos menores; así la depuración diaria no falla |
| D-S1-9 | `01-cuentas.sql` | `01-cuentas.sh` | Un `.sql` de `docker-entrypoint-initdb.d` no lee variables de entorno; el `.sh` usa variables de `psql` (sin interpolar contraseñas en el shell). `deploy/nexo/.gitattributes` fuerza LF |
| D-S1-10 | — | Healthcheck de Postgres por TCP (`pg_isready -h 127.0.0.1`) en los compose | Durante los scripts de inicio Postgres solo escucha en el socket y el chequeo por socket daba "listo" antes de tiempo |
| D-S1-11 | Guía de TLS | `guia-despliegue.md`; sin cambios de código: `pgx` ya admite `sslmode=verify-full&sslrootcert=...` en ambas URL | La prueba E2E usa la red interna sin TLS (demostración, RS-05 lo admite) |

Verificación (2026-10-12): pruebas Go 1254 OK, 0 fallos, 1 omitida (línea base 1241 + 13 nuevas);
E2E `06-pruebas/seguridad/Run-Seguridad.ps1` (repositorio de Nexo) con imagen
`nexo-engram-cloud:3.3.2-nexo.2-dev` y clientes oficiales 3.3.2: 36 OK, 0 fallos (CA-01..CA-36 de
trazabilidad con dos cuentas, CA-S01, CA-S02, CA-S03 y rechazo de cuenta de aplicación superusuario).

Riesgos y límites conocidos:

- **Una sola cuenta (modo degradado).** La cuenta es dueña de las tablas: puede desactivar triggers,
  y con la marca de sesión puede borrar filas de actividad de más de 30 días sin evento. Las filas
  recientes siguen protegidas. Se avisa al iniciar.
- **La cuenta dueña es de confianza.** Puede `ALTER TABLE ... DISABLE TRIGGER` y modificar evidencia;
  solo se usa al iniciar, su contraseña no debe estar en otro lugar que la configuración del
  servidor. La detección de alteraciones por esa vía es la cadena de hashes (S4).
- **Pasar un despliegue existente a dos cuentas** exige que la cuenta dueña sea dueña de los objetos
  (`REASSIGN OWNED`), si no `CREATE OR REPLACE` de funciones y triggers falla al migrar
  (`guia-despliegue.md` §3).
- **Imagen oficial sobre la misma base.** Engram 3.3.2 oficial con la cuenta de aplicación no puede
  migrar (no tiene `CREATE`); para volver a la imagen oficial hay que usar la cuenta dueña.
- `TestPGAuthorizationCostWith100kBindings` (NX-05) es una prueba de tiempos (objetivo < 2 ms) que
  puede fallar con la máquina cargada; en la línea base de esta fase falló una vez (2,31 ms) corriendo
  todos los paquetes en paralelo y pasa sola.

### 8.2 Fases S2 y S3 (NX-06)

Fecha: 2026-10-12. Implementación de S2-01..S2-04 y S3-01..S3-04 en `feat/seguridad` (sin commit).
Código: `internal/nexo/security/{ratelimit,lockout,network,tokens}.go`, `internal/nexo/authz/network.go`,
`cloudserver/nexo_security.go`, `cloudserver/nexo_tokens.go`, `cloudstore/nexo_tokens.go`,
`dashboard/nexo_tokens.go`; enganches `// NX-07` listados en `NEXO_CHANGES.md` §4.

| ID | Diseño | Implementado | Motivo |
|---|---|---|---|
| D-S2-1 | `nexoRateLimit` dentro de `nexoHandler` | `CloudServer.nexoHandler` (método) encadena el middleware de NX-04 (IP real) y el límite por IP; el límite por token va en el enganche final de `authenticateRequest` (`nexoAfterAuth`), porque el token solo se conoce después de resolverlo. El estado vive en un campo `nexo` del `CloudServer` | Un limitador por servidor (no por llamada a `Handler()`) y sin resolver el token dos veces |
| D-S2-2 | Token bucket con expiración de claves | Capacidad = límite por minuto, recarga límite/60 por segundo. Una clave sin uso durante 60 s tiene el balde lleno y se borra sin cambiar ninguna decisión (limpieza perezosa por fragmento, sin goroutines). Tope de 32 768 claves por fragmento (32 fragmentos); IPv6 se agrupa por /64 | Memoria acotada sin fugas; un atacante con muchas IP solo obtiene baldes llenos, que ya tendría |
| D-S2-3 | Bloqueo por IP y por usuario contando token inválido, vencido y login fallido | Por **IP** cuentan los fallos no atribuibles (token desconocido o mal formado, login del panel con token desconocido). Por **persona** cuentan token revocado y persona deshabilitada (se identifica por el token encontrado). **Un token vencido no cuenta** en ningún contador: queda auditado (`token_expired`) y lo vigilará la regla `expired_token_used` (S4) | El autosync del cliente oficial reintenta cada ~30 s: una PC con el token vencido bloquearía en minutos a toda una oficina detrás de un NAT, o a la persona recién al recibir su token nuevo. Adivinar tokens (256 bits) solo produce fallos no atribuibles. **Revisado por D-S2-8** (§8.3): por IP se cuentan tokens distintos, no intentos |
| D-S2-4 | 429 genérico durante el bloqueo | 429 con `Retry-After` y `error_code: auth_locked`, sin resolver el token ni decir si existe; el panel muestra el mismo "invalid token" de siempre | RS-11 sin revelar usuarios; HU-27 (aviso comprensible con tiempo de espera) |
| D-S2-5 | Evento por minuto y clave | `nexo.rate_limited` en `cloud_auth_audit_log` (grupo, tipo de clave, rechazos, minuto; `client_ip` o `credential_id`), escrito por una tarea que corre solo mientras hay rechazos y termina sola tras dos minutos sin ellos. `auth.lockout` (uno por bloqueo) en la misma tabla | No inundar la auditoría ni dejar goroutines vivas |
| D-S2-6 | Sin auto-asignación en `/admin/*`, panel y API Nexo con `self_grant_forbidden` | `/admin/*` y panel: 403 `self_grant_forbidden` + `authz.denied` (`reason=self_grant_forbidden`) y `platform.users.manage` explícito. La API Nexo ya lo impedía por R-11 y responde su código existente `self_promotion` (403) | No cambiar el contrato de NX-05; misma regla y mismo evento |
| D-S2-7 | CIDR en `authz.Can` | `authz.NetworkPolicy` (opción del motor) niega `platform.admins.manage` y `platform.settings` fuera de las redes (`Decision.Reason = network`, auditado por `Require`). IP ilegible = fuera. Variable con valor pero sin redes válidas = se niega todo (con aviso) | Fallar cerrado ante un error de configuración |
| D-S3-1 | Columnas y validación en la misma consulta | `FindPrincipalTokenByHash` trae `nexo_expires_at`, `nexo_scope` y el cálculo `nexo_expires_at <= NOW()` (reloj de la base). Si vence y no está revocado: `security.ErrTokenExpired` → 401 `unauthorized: token_expired: ...` y `reason_code=token_expired` | Una sola consulta (como pedía el diseño) |
| D-S3-2 | `Identity.TokenScope` desde `nexo_scope` | La misma consulta deja los datos del token en un contenedor del contexto de la petición (lo crea el middleware); `nexoAuthzIdentity` lo lee. Sin ese dato (almacenes falsos de las pruebas de Engram) el alcance queda `write` como antes | Sin consulta extra ni cambios a `cloudauth.Principal` |
| D-S3-3 | — | **Sesiones del panel**: en Engram una sesión administrada (8 h) no vuelve a presentar el token; ahora cada petición revisa que el token de la sesión no esté revocado ni vencido y toma su alcance | Si no, un token vencido o `read` seguiría administrando por el panel. **Completado por D-S3-9** (§8.3): la acción del panel que Engram autoriza solo por rol (pausar la sincronización) no pasaba por `authz.Can` |
| D-S3-4 | Rutas de escritura de Engram exigen `write` | `POST /sync/push` y `POST /sync/mutations/push` con token `read`: 403 `policy_forbidden` ("this token is read-only"), auditado como `sync.authorize` `token_scope_read` | Código que el cliente oficial ya trata como rechazo de política. **Reemplazado por D-S3-8** (§8.3): la lista negra de dos rutas dejaba pasar otras tres POST de escritura |
| D-S3-5 | 90 días por defecto | Valor por defecto de la columna (`now() + 90 días`, o el máximo si es menor) para cualquier `INSERT` (bootstrap por CLI, recuperación) y `nexoApplyTokenIssue` en la transacción de `CreatePrincipalTokenWithAudit` con lo pedido; la auditoría `token.create` agrega `expires_at` y `scope`. Trigger `nexo_token_expiry_guard`: solo una cuenta de servicio puede tener un token sin caducidad (también por SQL) | Los caminos de Engram que no conocen Nexo nacen con caducidad |
| D-S3-6 | Emisión en `/nexo/authz/v1` | Rutas nuevas `GET /nexo/authz/v1/tokens[?principal=]` (propios con `self.read`, ajenos con `platform.tokens.manage`) y `POST /nexo/authz/v1/tokens` (`platform.tokens.manage` + R-7). "Sin caducidad" (`expires_in_days: 0`) exige cuenta de servicio y `platform.admins.manage` (superadmin), denegación auditada. `/admin/*` de Engram no emite tokens a cuentas de servicio (solo personas) | No había ruta de emisión en la API Nexo |
| D-S3-7 | El panel muestra caducidad y alcance | Columna "Last Used / Expires / Scope", estado "Expired" y campos `expires_in_days` y `scope` en el formulario; página del token creado con caducidad y alcance. Plantilla cambiada sin agregar nodos (reutiliza expresiones) para que `components_templ.go` cambie 7 líneas (templ v0.3.1001) | Diff mínimo en archivos generados de Engram |

Verificación (2026-10-12):

- Go: 1281 OK, 0 fallos reales, 1 omitida (línea base 1254 + 27 nuevas). En la corrida completa
  falló una vez `autosync.TestManagerPushQuarantinesLegacyPaddedProject...` por la limpieza del
  directorio temporal de Windows ("directorio no está vacío"; paquete del cliente, no tocado) y pasa
  al repetirla. `go test -race` del paquete `security` y del middleware: OK (en un contenedor
  `golang:1.25`, porque en el equipo no hay gcc).
- Sobrecosto (RNF-S1): decisión del limitador ~0,1 µs; middleware completo (NX-04 + NX-07)
  ~0,6 µs más por petición que solo NX-04, muy por debajo de 0,2 ms.
- E2E `06-pruebas/seguridad/Run-Seguridad.ps1` con la imagen `nexo-engram-cloud:3.3.2-nexo.2-dev`
  y clientes oficiales 3.3.2: 41 OK, 0 fallos (CA-01..CA-36 y CA-S01..CA-S03 de antes, más CA-S04
  1000 pulls en 10 s: 696 permitidos, 304 con 429, un evento agregado, otra IP sin efecto y el
  autosync de las PCs nunca limitado; CA-S05; CA-S06; CA-S07; CA-S08 con el cliente oficial).
  Ajustes del script: límite `admin` elevado para la prueba de volumen CA-35 y patrón sin tilde en
  CA-S02 (la salida de `psql` depende de la codificación de la consola).
- `Run-Trazabilidad.ps1` no se volvió a ejecutar: usa el proyecto Docker `nexo-traza` (fuera del
  alcance de esta tarea) y la imagen publicada `3.3.2-nexo.1`, que no cambia; sus 36 casos corren
  dentro de `Run-Seguridad.ps1` con la imagen nueva (CA-S15).

Riesgos y límites conocidos:

- **Límites por réplica (DS-1).** Con N réplicas el límite efectivo es N veces el configurado y un
  bloqueo solo vale en la réplica que lo vio. Para un límite global, moverlo al proxy (Caddy) o a
  un almacén compartido.
- **IP real.** El límite y el bloqueo por IP dependen de `NEXO_TRUSTED_PROXIES` (NX-04). Detrás de
  un proxy sin esa variable, todas las peticiones parecen venir del proxy y comparten un balde.
- **NAT corporativo.** Todas las PCs de una oficina comparten IP: el límite de sync por IP
  (600/min) alcanza para unas 100 PCs con autosync (~6 peticiones/min cada una); con más, subir
  `sync.ip` en `NEXO_RATE_LIMITS` (el límite por token sigue protegiendo a cada una).
- **Autosync del cliente oficial 3.3.2.** En uso normal hace pocas peticiones por ciclo de ~30 s,
  muy por debajo de 600/min (en la E2E nunca fue limitado). Una importación inicial con muchos
  paquetes (`engram sync --cloud --import`) descarga uno por petición y podría recibir 429 por
  encima de ~600 paquetes en un minuto; el cliente reintenta en el siguiente ciclo.
- **Token compartido (legado).** No tiene límite por token (sería uno para todo el equipo); solo
  el de IP.
- **Superadmin y caducidad.** El token del bootstrap vence a los 90 días: el superadmin debe emitirse
  uno nuevo antes (sus propios tokens no requieren a otra persona) o tener un segundo superadmin. Si
  vence sin alternativa, la recuperación es por SQL con la cuenta dueña (`engram cloud bootstrap
  recover-token` solo sirve si la instalación no tiene ningún token).
- **`/me` lista capacidades** según rol y alcance, sin considerar la red: `platform.admins.manage`
  puede aparecer aunque se niegue fuera de las redes permitidas.

### 8.3 Parche de seguridad de S2 y S3 (revisión del 2026-10-12)

Fecha: 2026-10-12. Sobre `develop` 90d15e3, rama `feat/seguridad` (sin commit). Corrige tres
hallazgos de la revisión. Código: `internal/nexo/security/lockout.go` y
`internal/cloud/cloudserver/nexo_security.go`; no se agregan enganches en archivos de Engram (el
guardia del panel corre dentro del middleware de Nexo que ya instala `Handler()`).

| ID | Antes | Ahora | Motivo |
|---|---|---|---|
| D-S2-8 (revisa D-S2-3) | Por IP sumaba cada intento con token desconocido, mal formado o login fallido | Por IP se cuentan **tokens distintos** rechazados dentro de la ventana. Cada token se identifica por una huella HMAC-SHA256 truncada a 64 bits con una clave aleatoria generada al iniciar el proceso (`Lockout.Fingerprint`); el valor en claro no se guarda y la huella no sirve fuera del proceso. Las huellas viven en memoria, por clave de IP, y el conjunto se vacía al vencer la ventana o al bloquear (a lo sumo `max_failures` por IP; `max_failures` acotado a 10 000). Cabecera mal formada: huella de la cabecera completa. El login del panel usa el mismo criterio. Por persona se siguen contando intentos (token revocado, persona deshabilitada). Token vencido: no cuenta (sin cambios). El evento `auth.lockout` agrega `counts` (`distinct_tokens` o `attempts`). Cada rechazo sigue auditado con el evento existente de Engram (`sync.auth`, `dashboard.login`), con IP y User-Agent | El autosync de una PC con un token mal pegado o de un usuario eliminado reintenta cada ~30 s y en ~10 min bloqueaba 15 min a toda la IP (oficinas detrás de NAT). Adivinar tokens exige valores distintos, así que el umbral (20 en 5 min) sigue cortando la fuerza bruta (CA-S05) |
| D-S3-8 (reemplaza D-S3-4) | Lista negra: `POST /sync/push` y `POST /sync/mutations/push` | **Negar por defecto** (OWASP A01): en las rutas de Engram un token `read` solo usa `GET` y `HEAD`, más la lista blanca `POST /sync/prompt-source-attestations/verify` (verificado en el código: `handleVerifyPromptSourceAttestation` solo hace un `SELECT` en `CloudStore.VerifyPromptSourceAttestation`). Toda otra petición, incluidas las rutas que se agreguen, recibe 403 `policy_forbidden` "read-only", auditada como `sync.authorize` `token_scope_read` con la ruta. `/nexo/*` y `/admin/*` quedan fuera de esta regla: los gobierna `authz.Can` con `TokenScope` (las POST de `/admin/*` exigen capacidades que no terminan en `.read`; verificado con pruebas HTTP). Si el contexto no trae los datos del token y el almacén es de Nexo, el alcance se lee de la base; un error cuenta como `read` (fallar cerrado) | Un token `read` podía llamar `POST /sync/session-authorities`, `/sync/prompt-pair-claims` y `/sync/prompt-source-attestations` |
| D-S3-9 (completa D-S3-3) | Las acciones del panel que pasan por `requireManagedAdmin` ya negaban un token `read` (`authz.Can` con el alcance de la sesión), pero `POST /dashboard/admin/projects/{name}/sync` (pausar o reanudar la sincronización de un proyecto) solo mira el rol de Engram y **una sesión con token `read` podía pausarla** (verificado con una prueba antes de corregir: 303) | La sesión del panel hereda el alcance de su token: el middleware de Nexo niega por defecto toda petición del panel que no sea `GET`/`HEAD` (excepto login, logout y bootstrap) si la sesión administrada se abrió con un token `read`: 403 en texto y evento `dashboard.authorize` `token_scope_read`. Mirar el panel sigue permitido | Deny by default: también cubre acciones del panel que se agreguen |

Pruebas nuevas: `security.TestLockoutFailTokenCountsDistinct` y, en `cloudserver` (HTTP con Postgres y
resolución real de tokens), `TestNexoAuthLockoutCountsDistinctTokensPerIP`,
`TestNexoReadScopeDenyByDefault` y `TestNexoDashboardReadSessionCannotAdminister` (write administra,
read no, en las siete acciones del panel, con y sin HTMX).

Verificación (2026-10-12):

- Go: `go build ./...` y `go vet` OK; `go test -count=1 ./internal/cloud/... ./internal/nexo/...`
  1286 OK, 0 fallos, 1 omitida (línea base 1281); `-run TestCloudRuntime ./cmd/engram/` OK;
  `go test -race` del paquete `security` OK (contenedor `golang:1.25`).
- E2E `06-pruebas/seguridad/Run-Seguridad.ps1` con la imagen reconstruida
  `nexo-engram-cloud:3.3.2-nexo.2-dev`: 44 OK, 0 fallos (540 s). Casos nuevos: D-S2-8 (PC
  `pc-malpegado` con autosync y token inválido fijo, que comparte la IP de `pc-ana`, más 60
  reintentos: todos 401 y auditados, sin bloqueo; `pc-ana` sigue subiendo por autosync), CA-S05
  rehecho (19 tokens distintos repetidos no bloquean; el 20.º sí, evento con
  `counts=distinct_tokens`), D-S3-8 (token read: 403 en las tres POST de `/sync` y en POST de
  `/admin`; `verify` no se niega) y D-S3-9 (panel read: 403 en crear usuario, emitir token,
  deshabilitar y pausar sync; panel write: 303). Ajuste del script: la cota superior de CA-S04
  pasa a depender de la duración medida de la ráfaga (capacidad 600 + 10/s), porque en una
  máquina cargada la ráfaga tardó 17-18 s y la cota fija de 720 fallaba sin relación con el
  parche.

Riesgos y límites:

- **Bloqueo por persona sigue contando intentos.** Una persona con un token revocado en dos PCs viejas
  con autosync (2 x 10 intentos cada 5 min) llega a 20 y bloquea también su token nuevo durante 15
  min. Mitigación operativa: retirar el token de las PCs al revocarlo; `user_max_failures` es
  configurable (`0` lo desactiva).
- **Rotación de valores.** Una PC que presente un valor distinto en cada intento suma uno por intento,
  como un atacante; no es el comportamiento del cliente oficial.
- **Memoria.** Cada IP con fallos guarda hasta `max_failures` huellas de 8 bytes; el tope de claves por
  fragmento del contador acota el total.
- **Auditoría por rechazo.** El evento de Engram por cada rechazo se mantiene (un token mal pegado deja
  ~2 880 filas por día); no se agregó un resumen para no cambiar el contrato de Engram (engram#1134).
- `nexoAuthzIdentity` (rutas `/nexo/*` y `/admin/*`) sigue leyendo el alcance solo del contexto; en
  producción siempre está (lo pone el middleware), pero sin él quedaría `write` (D-S3-2).

