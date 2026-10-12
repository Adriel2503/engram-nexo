# NEXO_CHANGES — Registro de cambios del fork engram-nexo

**Proyecto:** Nexo IA · **Base:** Engram v3.3.2 (commit `35d1452`) · **Versión del fork:** `3.3.2-nexo.1`
**Especificación:** `docs/nexo/trazabilidad/` (`requirements.md`, `design.md`, `tasks.md`, `manual-admin.md`) · roles: `docs/nexo/roles/` (NX-05)

Este archivo es el inventario de todo lo que el fork cambia respecto de Engram. Sirve para revisar el fork y para integrar versiones nuevas de Engram con conflictos mínimos (RNF-04).

---

## 1. Qué es este fork

`engram-nexo` es Engram con cambios **solo en el servidor** (`engram cloud serve` y su base Postgres) para que el administrador del equipo pueda responder con datos confiables:

- **quién** creó, modificó o borró cada recuerdo (NX-01),
- **quién se conecta**, cuándo y desde dónde (NX-02, NX-04),
- **qué pasó** en la seguridad del servidor, por una API de consulta (NX-03).

La única identidad es el **token asignado** por el administrador; lo que declara la PC nunca es identidad.

Lo que **no** cambia:

- El cliente: las PCs siguen con el **Engram oficial** (probado con 3.3.2, ver §8). Ni el SQLite local ni el protocolo de sincronización cambian; las respuestas HTTP de `/sync/*` son las mismas.
- El esquema existente de Engram: las migraciones solo agregan columnas, tablas, índices, una vista y triggers (nada se renombra ni se borra).

## 2. Convención del fork

| Regla | Detalle |
|---|---|
| Lógica nueva aislada | Toda la lógica va en archivos `nexo_*.go` (y sus `nexo_*_test.go`). Tipos y funciones con prefijo `nexo`/`Nexo` |
| Ediciones mínimas y marcadas | En archivos originales de Engram solo se agregan llamadas de una línea (o un bloque muy corto), marcadas con `// NX-01` a `// NX-05`. Listado completo en §4; se verifica con `git grep -n "// NX-0"` |
| Módulos Nexo con almacenamiento propio | Paquetes en `internal/nexo/...` (NX-05: `internal/nexo/authz`), sin dependencias de `cloudserver`; reciben la base por `CloudStore.NexoDB()` y la identidad por `authz.Identity` |
| Migraciones | Aditivas e idempotentes (`ADD COLUMN IF NOT EXISTS`, `CREATE ... IF NOT EXISTS`, `CREATE OR REPLACE`), en `cloudstore/nexo_migrations.go`, al final de `migrate` |
| Ramas | `main` sigue a Engram (upstream, sin cambios propios) · `develop` integra el fork · `feat/<tema>` para cada cambio (por ejemplo `feat/trazabilidad`), con PR hacia `develop` tras revisión |
| Commits | Solo con la identidad del autor; nada se publica sin revisión |
| Integrar una versión nueva de Engram | Actualizar `main` desde upstream, fusionar en `develop`, resolver conflictos solo en las líneas marcadas (§4), correr `go test ./internal/cloud/...` con `CLOUDSTORE_TEST_DSN` y la prueba E2E de `06-pruebas/trazabilidad` (repo de Nexo) |

## 3. Cambios

### NX-01 · Autoría por cambio

**Motivo.** Engram guardaba qué cambió y cuándo, pero no quién; el único autor (por paquete) lo declaraba la PC (`created_by` = usuario de Windows o hostname) y se podía falsificar.

**Qué hace.** Cada cambio (`cloud_mutations`) y cada paquete (`cloud_chunks`) queda con el dueño del token que autenticó la petición: referencias (`actor_principal_id`, `actor_token_id`), copias históricas (`actor` = nombre mostrado, `actor_email`), `actor_verified` y lo declarado por la PC solo como referencia (`actor_client_claim`). Token compartido o servidor sin autenticación: `no-identificado`. `cloud_chunks.created_by` pasa a ser el actor resuelto. Vista `nexo_mutation_history` para el historial.

**Archivos nuevos.**
- `internal/cloud/cloudstore/nexo_actor.go` — `MutationActor`, transporte por contexto (`WithMutationActor`, `MutationActorFromContext`), `nexoApplyMutationActor` (publica el actor con `set_config(..., true)` dentro de la transacción).
- `internal/cloud/cloudstore/nexo_migrations.go` — migraciones Nexo (NX-01 y NX-02), trigger `nexo_stamp_actor` que sella cada fila.
- `internal/cloud/cloudserver/nexo_actor.go` — `nexoResolveActor` (token asignado / compartido / sin autenticación).
- Tests: `cloudstore/nexo_actor_test.go`, `cloudserver/nexo_actor_test.go`, `cloudserver/nexo_actor_handler_test.go`.

### NX-02 · Registro de conexiones

**Motivo.** No quedaba registro de quién se conecta: solo el último uso de cada token, que se sobrescribe.

**Qué hace.** Cada subida aceptada y cada descarga que entrega datos agrega una fila inmutable en `nexo_activity_log` (persona, token, proyecto, operación, cantidad, IP, cliente, lo declarado por la PC). `nexo_actor_presence` mantiene una fila por persona y token (primera y última conexión, última IP y cliente, contadores). Las consultas vacías del autosync y el manifiesto solo actualizan la presencia. Si el registro falla, la sincronización sigue (se loguea). Depuración del detalle por antigüedad (por defecto 180 días).

**Archivos nuevos.**
- `internal/cloud/cloudstore/nexo_activity.go` — `RecordActivity` (detalle + presencia en **una sola sentencia** atómica, con `synchronous_commit=off` local a esa sentencia; ver `design.md` §10.18) y `PurgeActivityOlderThan`.
- `internal/cloud/cloudserver/nexo_activity.go` — `nexoRecordActivity` (tolerante a fallos, 2 s máximo, `recover`), conteos y conservación periódica.
- Tests: `cloudstore/nexo_activity_test.go`, `cloudserver/nexo_actor_handler_test.go`, `cloudserver/nexo_overhead_bench_test.go` (benchmark de RNF-03; se puede copiar sin cambios sobre Engram 3.3.2 para comparar).

### NX-04 · Metadatos de la conexión (enfoque Utopia, sin tocar el cliente)

**Motivo.** La auditoría de seguridad de Engram no guardaba la IP de origen ni el cliente, y detrás de un proxy HTTPS solo se vería la IP del proxy.

**Qué hace.** Un middleware captura una vez por petición la IP real (con `X-Forwarded-For` solo si la conexión viene de un proxy confiable de `NEXO_TRUSTED_PROXIES`) y el User-Agent, y los deja en el contexto. Los usan la actividad, la presencia y **todos** los eventos de `cloud_auth_audit_log` (se agregan `client_ip` y `user_agent` a `metadata`, sin cambiar el esquema). El login rechazado del panel también queda con origen.

**Archivos nuevos.**
- `internal/cloud/cloudserver/nexo_clientctx.go` — middleware, `NexoClientContext`, proxies confiables.
- `internal/cloud/cloudstore/nexo_audit.go` — `WithNexoRequestOrigin`, `nexoAuditMetadata`.
- `internal/cloud/dashboard/nexo_login.go` — validación del login con la petición.
- Tests: `cloudserver/nexo_clientctx_test.go`.

### NX-03 · Auditoría consultable (API JSON)

**Motivo.** La auditoría de Engram no se podía consultar desde ninguna parte.

**Qué hace.** Rutas de solo lectura para administradores (rol `admin` y token asignado; el resto `403`, sin token `401`): `GET /nexo/admin/audit`, `/nexo/admin/activity`, `/nexo/admin/presence`, `/nexo/admin/history/{entityKey}`. Filtros, paginación por cursor (máximo 200 por página) y cada consulta queda auditada (`nexo.*.read`). Uso: `docs/nexo/trazabilidad/manual-admin.md`.

**Archivos nuevos.**
- `internal/cloud/cloudserver/nexo_admin_routes.go` — handlers, validación de filtros, auditoría de la lectura.
- `internal/cloud/cloudstore/nexo_queries.go` — `ListAuthAudit`, `ListActivity`, `ListPresence`, `MutationHistory`.
- Tests: `cloudserver/nexo_admin_routes_test.go`, `cloudstore/nexo_queries_test.go` (incluye prueba de tiempos con 1 millón de filas, `NEXO_PERF_TEST=1`).

### NX-05 · Roles: motor y plataforma (etapa R1)

**Especificación.** `docs/nexo/roles/` (`requirements.md`, `design.md` con desviaciones en §10, `tasks.md`).

**Motivo.** Engram solo distingue `admin` y `member`: cualquier admin crea otros admins, no hay rol de auditoría ni roles por proyecto o espacio. Los espacios de conocimiento (NX-10) y la sincronización por capacidad (etapas R2 y R3) necesitan un motor común.

**Qué hace.** Motor único de autorización con tres niveles (plataforma, proyecto, espacio): roles como conjuntos de capacidades, herencia por orden, alcance de token (`read`/`write`), rol implícito en ámbitos abiertos, compatibilidad con Engram (admin de Engram = admin; primer admin = superadmin; `cloud_project_grants` = publicador). Reglas R-1 a R-11 y R-13 a R-15 de los requisitos; todo cambio y toda denegación queda en `nexo_authz_events`. API `/nexo/authz/v1` (mis roles, plataforma, ámbitos, miembros, archivo, recuperación, eventos) y `GET /nexo/admin/authz-events`. En la administración de Engram: solo un superadmin crea, habilita o deshabilita admins, superadmins y auditores y gestiona sus tokens; el último superadmin no se degrada ni deshabilita; las rutas `/nexo/admin/*` y las de lectura de `/admin/*` admiten al auditor. Las rutas de sincronización de proyectos no cambian todavía (R2).

**Archivos nuevos.**
- `internal/nexo/authz/roles.go` — niveles, roles, capacidades, herencia, alcance de token.
- `internal/nexo/authz/identity.go` — `Identity`, `Decision`, `ScopeRole`, identidad y caché por petición en el contexto.
- `internal/nexo/authz/engine.go` — `Engine` (`EffectiveRole`, `Can`, `VisibleScopes`, `Require`, `RecordDenied`), resolución del rol efectivo.
- `internal/nexo/authz/pgstore.go` — Postgres: datos de decisión (una consulta), visibles, listados, eventos.
- `internal/nexo/authz/service.go` — casos de uso con reglas (plataforma, ámbitos, miembros, recuperación, eventos).
- `internal/nexo/authz/migrate.go` — tablas, triggers, fila de plataforma y arranque del superadmin.
- `internal/nexo/authz/events.go` — acciones y consulta de eventos.
- `internal/cloud/cloudserver/nexo_authz_bridge.go` — identidad desde el token (`PrincipalFromContext`, `nexoResolveActor`, `NexoClientContextFrom`) y lógica de los enganches `// NX-05`.
- `internal/cloud/cloudserver/nexo_authz_routes.go` — API `/nexo/authz/v1` y `/nexo/admin/authz-events`.
- Tests: `internal/nexo/authz/roles_test.go`, `engine_test.go` (matriz completa, 23 616 casos), `pg_test.go` (Postgres: migración, triggers, reglas, eventos, rendimiento con 100 000 asignaciones), `cloudserver/nexo_authz_routes_test.go` (HTTP: CA-R01 a CA-R04, CA-R09, CA-R10 por la API Nexo, `/admin/*` y el panel).

**Archivos Nexo modificados.** `cloudstore/nexo_migrations.go` (llama a `authz.Migrate` en la misma transacción y expone `CloudStore.NexoDB()`), `cloudserver/nexo_admin_routes.go` (registra `/nexo/admin/authz-events` y la API de roles).

## 4. Líneas marcadas en archivos originales de Engram

Verificado con `git grep -n "// NX-0"` (fuera de `docs/`). Las líneas sin marca propia dentro de un bloque se indican como continuación del bloque marcado.

| Archivo:línea | Marca | Propósito |
|---|---|---|
| `internal/cloud/cloudserver/cloudserver.go:219` | NX-02 | `s.nexoStartActivityRetention()` en `Start()`: depuración del registro al iniciar y cada 24 h |
| `internal/cloud/cloudserver/cloudserver.go:227` | NX-04 | `Handler()` devuelve `nexoHandler(s.mux)`: middleware de IP y User-Agent para todas las rutas |
| `internal/cloud/cloudserver/cloudserver.go:301-303` | NX-04 | Campo `NexoValidateLoginTokenRequest` en `dashboard.MountConfig` (bloque de 3 líneas): el login del panel se valida con el contexto de la petición |
| `internal/cloud/cloudserver/cloudserver.go:341` | NX-03 | `s.registerNexoAdminRoutes(s.mux)`: registra `/nexo/admin/*` |
| `internal/cloud/cloudserver/cloudserver.go:615` | NX-02 | `handlePullManifest`: actualiza la presencia (sin fila de detalle) |
| `internal/cloud/cloudserver/cloudserver.go:641` | NX-02 | `handlePullChunk`: registra `pull_chunk` |
| `internal/cloud/cloudserver/cloudserver.go:813-815` | NX-01 | `handlePushChunk`: comentario marcado + `nexoActor := nexoResolveActor(...)` + `WriteChunk` recibe el actor por contexto y `nexoActor.Name` como `created_by` (antes `req.CreatedBy`) |
| `internal/cloud/cloudserver/cloudserver.go:823` | NX-02 | `handlePushChunk`: registra `push_chunk` con lo declarado por la PC |
| `internal/cloud/cloudserver/mutations.go:205-207` | NX-01 | `handleMutationPush`: comentario marcado + `nexoActor := nexoResolveActor(...)` + `createdBy := nexoActor.Name` (antes `mutationPushCreatedBy`, que se conserva sin uso) |
| `internal/cloud/cloudserver/mutations.go:248` | NX-01 | `InsertMutationBatch` recibe el actor por contexto |
| `internal/cloud/cloudserver/mutations.go:261` | NX-02 | `handleMutationPush`: registra `push_mutations` |
| `internal/cloud/cloudserver/mutations.go:475` | NX-02 | `handleMutationPull`: registra `pull_mutations` (detalle solo si devuelve cambios) |
| `internal/cloud/cloudserver/dashboard_session.go:124` | NX-04 | `validateDashboardLoginToken` delega en la variante con contexto (`context.Background()`, misma semántica) |
| `internal/cloud/cloudserver/dashboard_session.go:127-129` | NX-04 | Nueva firma `validateDashboardLoginTokenContext(ctx, token)`: el cuerpo es el original de Engram; solo se quitó su `ctx := context.Background()` (ahora es parámetro) |
| `internal/cloud/cloudstore/cloudstore.go:262-264` | NX-01 | `WriteChunk`: `nexoApplyMutationActor(ctx, tx)` dentro de la transacción (bloque `if err` de 3 líneas) |
| `internal/cloud/cloudstore/cloudstore.go:878-880` | NX-01 / NX-02 | `migrate`: `cs.nexoMigrate(ctx)` al final (bloque de 3 líneas) |
| `internal/cloud/cloudstore/cloudstore.go:959-961` | NX-01 | `InsertMutationBatch`: `nexoApplyMutationActor(ctx, tx)` (bloque de 3 líneas) |
| `internal/cloud/cloudstore/identity.go:980` | NX-04 | `insertAuthAuditEvent`: `nexoAuditMetadata(ctx, ...)` agrega `client_ip` y `user_agent` a `metadata` (punto común de toda la auditoría de Engram) |
| `internal/cloud/dashboard/dashboard.go:57-58` | NX-04 | Campo opcional `NexoValidateLoginTokenRequest` en `MountConfig` |
| `internal/cloud/dashboard/dashboard.go:314` | NX-04 | `handleLoginSubmit` usa `nexoValidateLoginToken(h.cfg, r, token)` (cae en `ValidateLoginToken` si el campo no está) |
| `cmd/engram/cloud_runtime_e2e_test.go:332-336` | NX-01 | Test de Engram ajustado: `/sync/push` con token asignado guarda el dueño del token, no el `created_by` del cliente (RF-04) |
| `internal/cloud/cloudserver/admin_handlers.go:112-114` | NX-05 | `handleAdminCreateUser`: `nexoAllowCreateUserRole` (crear con rol admin exige `platform.admins.manage`, solo superadmin) |
| `internal/cloud/cloudserver/admin_handlers.go:149-151` | NX-05 | `handleAdminSetUserEnabled`: `nexoAllowSetUserEnabled` (sobre admin, superadmin o auditor exige `platform.admins.manage`; último superadmin, 409) |
| `internal/cloud/cloudserver/admin_handlers.go:207-209` | NX-05 | `handleAdminCreateToken`: `nexoAllowManageTargetTokens` (tokens de otro admin, superadmin o auditor solo por superadmin) |
| `internal/cloud/cloudserver/admin_handlers.go:289-291` | NX-05 | `handleAdminRevokeToken`: `nexoAllowRevokeToken` (misma regla, resolviendo el dueño del token) |
| `internal/cloud/cloudserver/admin_handlers.go:387-389` | NX-05 | `requireManagedAdmin`: `nexoRequireAdminCapability` exige la capacidad de plataforma de la ruta (admite al auditor en lecturas); sin almacén Nexo sigue la regla de Engram |
| `internal/cloud/cloudserver/dashboard_admin_users.go:148-150` | NX-05 | Panel, crear usuario: igual que la API |
| `internal/cloud/cloudserver/dashboard_admin_users.go:192-194` | NX-05 | Panel, habilitar y deshabilitar: igual que la API |
| `internal/cloud/cloudserver/dashboard_admin_users.go:288-290` | NX-05 | Panel, emitir token: igual que la API |
| `internal/cloud/cloudserver/dashboard_admin_users.go:382-384` | NX-05 | Panel, revocar token: igual que la API |

Otros archivos originales modificados sin código: `.gitignore` (bloque «Nexo: secretos y archivos locales»).

Archivos nuevos fuera de `internal/`: `deploy/nexo/Dockerfile`, `deploy/nexo/Dockerfile.dockerignore`, `docs/nexo/**`, este archivo.

## 5. Variables de entorno nuevas

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_TRUSTED_PROXIES` | vacío (ningún proxy confiable) | IP o CIDR separados por comas o espacios (por ejemplo la IP de Caddy). Solo si la conexión viene de uno de ellos se toma la IP real de `X-Forwarded-For` (recorrida de derecha a izquierda); si no, la cabecera se ignora. Entradas inválidas se ignoran con log |
| `NEXO_ACTIVITY_RETENTION_DAYS` | `180` | Días que se conserva `nexo_activity_log` (depuración al iniciar y cada 24 h). `0` = no depurar; valor inválido = 180. La presencia se conserva siempre |
| `NEXO_PERF_TEST` | — | Solo tests: `1` habilita la prueba de tiempos con 1 millón de filas |

Las variables de Engram no cambian. Recomendación operativa (RN-A3): `ENGRAM_CLOUD_TOKEN` con un valor aleatorio largo que no se entrega a nadie; todo lo que llegue con él figura como `no-identificado`.

## 6. Migraciones (`cloudstore/nexo_migrations.go`)

Se ejecutan solas al iniciar, al final de `migrate`, en una transacción con `pg_advisory_xact_lock`. Todas idempotentes.

| Objeto | Cambio |
|---|---|
| `cloud_mutations`, `cloud_chunks` | Columnas `actor`, `actor_email`, `actor_principal_id`, `actor_token_id`, `actor_verified BOOLEAN NOT NULL DEFAULT FALSE`, `actor_client_claim`. Filas anteriores: `actor` NULL, no verificado (RF-08) |
| Función + triggers `nexo_stamp_actor` | `BEFORE INSERT` en ambas tablas: copia el actor publicado en la transacción (y el email desde `cloud_human_users`) |
| Vista `nexo_mutation_history` | Historial por cambio con datos de la copia y actuales del usuario y del token |
| `nexo_activity_log` | Detalle de conexiones; triggers `nexo_activity_log_immutable` que rechazan `UPDATE`, `DELETE` y `TRUNCATE` (salvo la depuración con `nexo.allow_retention`) |
| `nexo_actor_presence` | Una fila por persona y token |
| Índices | `cloud_mutations (actor_principal_id, seq)`, `(project, entity_key, seq)`, `(entity_key, seq)`; `nexo_activity_log (occurred_at DESC)`, `(actor_principal_id, occurred_at DESC)`, `(actor_principal_id, id DESC)`, `(actor_token_id, id DESC)`; `nexo_actor_presence (last_seen_at DESC, presence_key DESC)` |
| `nexo_scopes` (NX-05) | Ámbitos: `(scope_type, scope_key)` PK, `visibility`, `default_role`, `status`, autor y fechas; fila `('platform', '*')`. Índice `(scope_type, visibility, status)`. Los proyectos de Engram sin fila son restringidos y activos |
| `nexo_role_bindings` (NX-05) | Un rol por persona y ámbito (PK `principal_id, scope_type, scope_key`), FK a `cloud_principals` (CASCADE) y a `nexo_scopes`. Índice `(scope_type, scope_key, role)`. Triggers `nexo_role_bindings_validate` (combinación rol/nivel y cuentas de servicio, R-10) y `nexo_role_bindings_guard` (último superadmin, R-8) |
| `nexo_authz_events` (NX-05) | Eventos de roles, solo inserción (triggers que rechazan `UPDATE`, `DELETE` y `TRUNCATE`). Índices por ámbito, actor, objetivo y acción (`id DESC`) |
| Trigger `nexo_authz_guard_principal` (NX-05) | En `cloud_principals` (`UPDATE OF enabled, role` y `DELETE`): rechaza deshabilitar, degradar o borrar al último superadmin habilitado por cualquier vía |
| Función `nexo_authz_bootstrap_candidate()` (NX-05) | Admin humano habilitado de menor id cuando no hay superadmin habilitado; la migración (y la primera petición si el admin se crea después) le asigna `superadmin` con evento de actor `sistema` |

Volver a la imagen oficial 3.3.2 con la misma base es posible: Engram ignora las columnas, tablas y la vista nuevas, y el trigger `nexo_stamp_actor` no modifica las filas cuando nadie publicó el actor en la transacción (quedan con `actor` NULL, como los datos anteriores al fork). Con NX-05, el trigger `nexo_authz_guard_principal` sigue activo en la base aunque se use la imagen oficial: impide deshabilitar al último superadmin habilitado (se puede quitar con `DROP TRIGGER nexo_authz_guard_principal ON cloud_principals` si se abandona el fork).

## 7. Compatibilidad con el cliente oficial

- Las rutas y respuestas de `/sync/*` no cambian; el autor no viaja a los clientes (`ListMutationsSince` sin cambios).
- Verificado de punta a punta con el **binario oficial Engram 3.3.2** (linux/amd64) contra la imagen `nexo-engram-cloud:3.3.2-nexo.1`, por las dos vías: paquetes (`engram sync --cloud`, `--import`) y autosync de mutaciones (`engram serve` con `ENGRAM_CLOUD_AUTOSYNC=1`), detrás de Caddy HTTPS. Resultados en `06-pruebas/trazabilidad/RESULTADOS.md` del repositorio de Nexo.
- Comportamiento del cliente a tener en cuenta (`design.md` §10.20): con token exige HTTPS en ambas vías; el autosync no envía `created_by` (lo declarado por la PC solo llega por paquetes); `engram sync --cloud` puede volver a subir observaciones importadas, que quedan atribuidas a quien las subió; un proyecto inscrito sin permiso traba el autosync de esa PC.

## 8. Construcción y despliegue

```powershell
# Desde la raíz del fork
docker build --platform linux/amd64 -f deploy/nexo/Dockerfile --build-arg VERSION=3.3.2-nexo.1 -t nexo-engram-cloud:3.3.2-nexo.1 .
docker run --rm nexo-engram-cloud:3.3.2-nexo.1 version   # engram 3.3.2-nexo.1
```

Misma plataforma, usuario, puerto (18080), `ENTRYPOINT` y `CMD` que la imagen oficial; se reemplaza la imagen del servicio `cloud` y se agregan `NEXO_TRUSTED_PROXIES` (IP del proxy) y, si se desea, `NEXO_ACTIVITY_RETENTION_DAYS`. Las migraciones se aplican solas.

## 9. Verificación

```powershell
$env:CLOUDSTORE_TEST_DSN = '<DSN de un Postgres de pruebas>'   # p. ej. un postgres:16-alpine local desechable
go build ./... ; go vet ./internal/cloud/... ./internal/nexo/...
go test -count=1 ./internal/cloud/... ./internal/nexo/...
go test -count=1 -run TestCloudRuntime ./cmd/engram/
go test -run '^$' -bench NexoOverhead -benchtime 200x ./internal/cloud/cloudserver/   # RNF-03
git grep -n "// NX-0" -- ':!docs'
```
