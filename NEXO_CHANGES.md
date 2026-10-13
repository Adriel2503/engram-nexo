# NEXO_CHANGES — Registro de cambios del fork engram-nexo

**Proyecto:** Nexo IA · **Base:** Engram v3.4.0 (commit `fc43849`, integrado el 2026-10-13 en `1c3aadb`; antes v3.3.2, commit `35d1452`) · **Próxima versión del fork:** `3.4.0-nexo.1`
**Especificación:** `docs/nexo/trazabilidad/` (`requirements.md`, `design.md`, `tasks.md`, `manual-admin.md`) · roles: `docs/nexo/roles/` (NX-05) · seguridad: `docs/nexo/seguridad/` (NX-07) · conocimiento: `docs/nexo/conocimiento/` (NX-10)

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
| Ediciones mínimas y marcadas | En archivos originales de Engram solo se agregan llamadas de una línea (o un bloque muy corto), marcadas con `// NX-01` a `// NX-10`. Listado completo en §4; se verifica con `git grep -n "// NX-"` |
| Módulos Nexo con almacenamiento propio | Paquetes en `internal/nexo/...` (NX-05: `internal/nexo/authz`), sin dependencias de `cloudserver`; reciben la base por `CloudStore.NexoDB()` y la identidad por `authz.Identity` |
| Migraciones | Aditivas e idempotentes (`ADD COLUMN IF NOT EXISTS`, `CREATE ... IF NOT EXISTS`, `CREATE OR REPLACE`), en `cloudstore/nexo_migrations.go`, al final de `migrate` |
| Ramas (desde el 2026-10-13) | `main`: versión estable de Nexo (se adelanta desde `develop` cuando algo está verificado). `develop`: integración y rama por defecto de GitHub (de ella instalan el plugin Claude Code y Codex). `engram-upstream`: copia exacta del Engram original, sin cambios propios (antes ese papel lo tenía `main`). `feat/<tema>` para cada cambio, con PR hacia `develop` tras revisión |
| Commits | Solo con la identidad del autor; nada se publica sin revisión |
| Integrar una versión nueva de Engram | Adelantar `engram-upstream` a `upstream/main`, fusionarla en `develop`, resolver conflictos solo en las líneas marcadas (§4), correr `go test ./internal/cloud/...` con `CLOUDSTORE_TEST_DSN` y la prueba E2E de `06-pruebas/trazabilidad` (repo de Nexo) |

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

### NX-07 · Endurecimiento de seguridad, fase S1 (base de datos)

**Especificación.** `docs/nexo/seguridad/` (`requirements.md` RS-01..RS-06, `design.md` §2 con inventario y desviaciones en §8, `tasks.md` S1-01..S1-06, `guia-despliegue.md`).

**Motivo.** La única cuenta de Postgres era dueña de todas las tablas: podía desactivar los triggers de inmutabilidad, borrar o truncar la auditoría, y la depuración del registro se habilitaba con una variable de sesión que cualquier conexión podía activar (`nexo.allow_retention`).

**Qué hace.**
- **Dos cuentas** (RS-01): con `NEXO_MIGRATION_DATABASE_URL` el servidor migra (Engram y Nexo), aplica privilegios y crea la función de retención con la cuenta dueña, y opera con `ENGRAM_DATABASE_URL` sin migrar. Al iniciar verifica que la cuenta de aplicación no sea superusuario, ni dueña (o miembro de la dueña) de objetos, ni tenga `CREATEROLE`, `CREATEDB` o `CREATE` en el esquema o la base; si lo es, no arranca. Sin la variable, todo sigue como antes y el log avisa `seguridad degradada: una sola cuenta de base de datos` (RS-04).
- **Privilegios mínimos** (RS-02): `security.ApplyGrants`, idempotente en cada arranque. Tablas de evidencia (`cloud_auth_audit_log`, `cloud_sync_audit_log`, `cloud_mutations`, `cloud_chunks`, `nexo_activity_log`, `nexo_authz_events` y, cuando existan, las de S4 y NX-10) solo `SELECT` e `INSERT`; el resto `SELECT/INSERT/UPDATE/DELETE`; vistas solo lectura; sin `CREATE` en el esquema; sin `CREATE`/`TEMPORARY` en la base para `PUBLIC` si la dueña es dueña de la base. Inventario de escrituras de Engram en `design.md` §2.2.1 (no hizo falta ninguna excepción).
- **Retención segura** (RS-03): función `nexo_purge_activity(days)` `SECURITY DEFINER` de la cuenta dueña (mínimo 30 días, evento `retention.purged` en `nexo_authz_events`). Se eliminó el bypass `nexo.allow_retention`: el trigger solo deja borrar a la dueña de la tabla, dentro de la función y filas con más de 30 días. `PurgeActivityOlderThan` llama a la función.
- **Despliegue** (RS-05, RS-06): `deploy/nexo/postgres-init/01-cuentas.sh` (crea `nexo_owner` y `nexo_app` desde variables de entorno), `deploy/nexo/compose.example.yaml` (dos cuentas + Caddy), `deploy/nexo/Caddyfile.example`, guía de TLS (`sslmode=verify-full`) en `docs/nexo/seguridad/guia-despliegue.md`.

**Archivos nuevos.**
- `internal/nexo/security/dbaccounts.go` — variables, `EvidenceTables`, `MigrateRetention`, `ApplyGrants`, `ResolveAppRole`, `VerifyAppAccount`.
- `internal/cloud/cloudstore/nexo_security.go` — `nexoMigrateOnStartup` (una o dos cuentas, verificación, avisos).
- `deploy/nexo/postgres-init/01-cuentas.sh`, `deploy/nexo/compose.example.yaml`, `deploy/nexo/Caddyfile.example`, `deploy/nexo/.gitattributes` (LF para `*.sh`).
- Tests: `internal/nexo/security/dbaccounts_test.go`, `cloudstore/nexo_security_test.go` (Postgres real con roles de prueba: CA-S01, CA-S02, CA-S03, cuenta no apta, idempotencia y tabla de evidencia nueva).

**Archivos Nexo modificados.** `cloudstore/nexo_migrations.go` (trigger de inmutabilidad sin bypass; llama a `security.MigrateRetention`), `cloudstore/nexo_activity.go` (`PurgeActivityOlderThan` usa la función), `cloudserver/nexo_activity.go` (`NEXO_ACTIVITY_RETENTION_DAYS` mínimo 30), `internal/nexo/authz/events.go` (acción `retention.purged`).

### NX-10 · Espacios de conocimiento, parte 2a (núcleo)

**Especificación.** `docs/nexo/conocimiento/` (`requirements.md`, `design.md` con ingesta y procedencia en §15 y desviaciones D-I1..D-I15 en §16, `tasks.md`).

**Motivo.** Lo oficial de la empresa (estándares, lineamientos, glosario) vivía como texto dentro de un proyecto de Engram (`nexo-transversal`), sin versiones, vigencia, secciones ni control de quién publica.

**Qué hace.** Una capa de conocimiento separada de los proyectos, en el mismo servidor y la misma base, con tablas propias `nexo_kb_*` (ninguna referencia tablas de proyectos). Espacios sobre el motor de roles de NX-05 (crear con responsable, abiertos por defecto con rol implícito, configurar, archivar y restaurar; miembros por `/nexo/authz/v1`). Publicación por lotes idempotente por hash, con versiones inmutables, secciones citables con procedencia (`source_location`, `extraction`), vigencia y bitemporalidad (`at`, `known_at`), poda por origen git solo con el lote completo y `dry-run`; retiro y reemplazo; eventos de solo inserción con actor, token, IP y cliente; feed `nexo_kb_changes` (se consume en 2b). Búsqueda `pgfts` en español detrás de la interfaz `Retriever`, con filtros comunes obligatorios de tiempo y de permisos que ningún motor puede saltarse; `no_results` explícito. API `/nexo/kb/v1`. Medición de la búsqueda (Recall@5 y MRR). CLI `engram nexo kb publish|search|spaces|eval`. Pendiente para 2b: propuestas, copia local, herramientas MCP, feed `/sync`.

**Archivos nuevos.**
- `internal/nexo/kb/blobstore.go` — interfaz `BlobStore` y `NoopBlobStore` (los originales no se guardan todavía, D-K11).
- `internal/nexo/kb/domain/` — tipos, constantes y errores de dominio.
- `internal/nexo/kb/ingest/` — `Converter` con bloques y procedencia, `Registry` por extensión y MIME con límites (entrada, tiempo, salida), conversores Markdown (front matter YAML) y texto, `Sectioner`, detector de secretos (RN-K4).
- `internal/nexo/kb/store/` — migraciones (`Migrate`) y SQL.
- `internal/nexo/kb/search/` — `Retriever`, `Searcher` (filtros comunes), `PGFTS`, citas.
- `internal/nexo/kb/service/` — casos de uso.
- `internal/nexo/kb/httpapi/` — rutas `/nexo/kb/v1`.
- `internal/nexo/kb/eval/` — formato `preguntas.yaml`, Recall@5, MRR.
- `internal/nexo/kb/cli/` — comando `engram nexo kb`.
- `internal/nexo/kb/kbtest/` — entornos de prueba (Postgres, una y dos cuentas).
- `internal/cloud/cloudserver/nexo_kb_bridge.go` — registra `/nexo/kb/v1` con `withAuth` y la identidad de NX-05.
- Tests: `ingest/ingest_test.go` (lineamientos de frontend reales en `ingest/testdata`), `search/search_test.go` (un `Retriever` no salta los filtros), `service/service_pg_test.go` (CA-K01..CA-K06, CA-K09, CA-K13, bitemporalidad, español con y sin `unaccent`, migración), `service/perf_test.go` (RNF-K1, `NEXO_KB_PERF_SECTIONS`), `store/store_pg_test.go` (inmutabilidad con dos cuentas), `eval/eval_test.go`, `cli/cli_test.go`, `cloudserver/nexo_kb_routes_test.go` (HTTP de punta a punta).

**Archivos Nexo modificados.** `cloudstore/nexo_migrations.go` (llama a `kbstore.Migrate` después de `authz.Migrate`, en la misma transacción), `cloudstore/nexo_security_test.go` (la prueba de tabla de evidencia futura usa `nexo_alerts` en lugar de `nexo_kb_events`, que ahora existe).

### NX-07 · Endurecimiento de seguridad, fases S2 (abuso y escalada) y S3 (tokens, NX-06)

**Especificación.** `docs/nexo/seguridad/` (`requirements.md` RS-10..RS-13 y RS-20..RS-24, `design.md` §3 y §4 con desviaciones en §8.2, `tasks.md` S2-01..S2-04 y S3-01..S3-04, `guia-despliegue.md` §6). Cubre la etapa R4 de roles.

**Motivo.** Sin límite de peticiones ni bloqueo por fallos, la API admitía fuerza bruta y abuso; un admin podía darse a sí mismo un permiso de proyecto de Engram (equivale a publicador); la administración de plataforma no tenía restricción por red; los tokens no vencían ni tenían alcance.

**Qué hace.**
- **Límite de peticiones** (S2-01): token bucket en memoria por IP (después de resolver la IP real de NX-04) y por token (después de autenticar), por grupo de rutas (`sync`, `admin`, `login`, `nexo`, `other`); 429 con `Retry-After` y `error_code: rate_limited`; un evento `nexo.rate_limited` por minuto y clave en `cloud_auth_audit_log`. Claves inactivas expiran solas; memoria acotada. `NEXO_RATE_LIMITS` (JSON u `off`).
- **Bloqueo por fallos** (S2-02): por IP cuentan los **tokens distintos** rechazados (desconocidos o mal formados, también en el login del panel; huella HMAC-SHA256 truncada con clave aleatoria del proceso, solo en memoria), no los intentos: el mismo token inválido repetido suma una vez y no bloquea a una oficina detrás de un NAT (parche D-S2-8, 2026-10-12). Por persona cuentan los intentos con token revocado o persona deshabilitada. 20 en 5 min bloquean 15 min con 429 `auth_locked` genérico y un evento `auth.lockout` (`counts`: `distinct_tokens` o `attempts`). Un token vencido no suma; queda auditado. `NEXO_AUTH_LOCKOUT`.
- **Sin auto-asignación** (S2-03): `/admin/*` y panel rechazan darse un permiso de proyecto a uno mismo (403 `self_grant_forbidden`, evento `authz.denied`) y exigen `platform.users.manage`; la API Nexo ya lo impedía (R-11, `self_promotion`).
- **Redes de superadmin** (S2-04): `authz.Can` niega `platform.admins.manage` y `platform.settings` fuera de `NEXO_SUPERADMIN_ALLOWED_CIDRS` (`reason=network`, auditado); vacía = sin restricción con aviso al iniciar.
- **Tokens** (S3): columnas `nexo_expires_at` y `nexo_scope`; la consulta que resuelve el token rechaza uno vencido (401 `token_expired`, auditado) y deja su alcance en `Identity.TokenScope`; un token `read` solo lee: en las rutas de Engram, negar por defecto (solo `GET`/`HEAD` y la lista blanca `POST /sync/prompt-source-attestations/verify`; parche D-S3-8), y no administra (`authz.Can`); una sesión del panel hereda el alcance de su token y con `read` toda petición que no sea `GET`/`HEAD` recibe 403 (parche D-S3-9, cubre también pausar la sincronización de un proyecto); las sesiones del panel caen si su token se revoca o vence. Emisión con `expires_in_days` (90 por defecto, máximo `NEXO_TOKEN_MAX_DAYS`) y `scope` en `/admin/*`, panel, `POST /nexo/authz/v1/tokens` y bootstrap por CLI; "sin caducidad" solo cuentas de servicio y solo superadmin (también lo exige un trigger). `/me`, `/admin/users/{id}/tokens`, `GET /nexo/authz/v1/tokens` y el panel muestran caducidad, alcance y último uso. Los tokens anteriores quedan sin caducidad y siguen funcionando.

**Archivos nuevos.**
- `internal/nexo/security/ratelimit.go` — grupos, configuración, `Limiter` (token bucket fragmentado), `RejectionLog` (resumen por minuto y clave).
- `internal/nexo/security/lockout.go` — configuración, `Lockout`, clasificación de motivos.
- `internal/nexo/security/network.go` — `NEXO_SUPERADMIN_ALLOWED_CIDRS`.
- `internal/nexo/security/tokens.go` — `ErrTokenExpired`, datos del token en el contexto, emisión (`ResolveIssue`, `ApplyTokenIssue`), `MigrateTokens`, `LoadTokenFacts`, `LoadTokenInfo`.
- `internal/nexo/authz/network.go` — `NetworkPolicy`, `WithNetworkPolicy`.
- `internal/cloud/cloudserver/nexo_security.go` — middleware, 429, eventos, bloqueo, alcance `read` (rutas de Engram y guardia del panel `nexoDashboardScopeGuard`), sin auto-asignación, política de red.
- `internal/cloud/cloudserver/nexo_tokens.go` — emisión con caducidad y alcance, revalidación de sesiones del panel, `/nexo/authz/v1/tokens`, `/me` con datos del token.
- `internal/cloud/cloudstore/nexo_tokens.go` — `nexoCheckResolvedToken`, `nexoApplyTokenIssue`, `nexoFillTokenFacts`.
- `internal/cloud/dashboard/nexo_tokens.go` — caducidad, alcance y estado del token en el panel.
- Tests: `internal/nexo/security/ratelimit_test.go`, `lockout_test.go` (unidad, concurrencia con `-race`, sobrecosto), `internal/nexo/authz/network_test.go`, `cloudstore/nexo_tokens_test.go` (Postgres), `cloudserver/nexo_security_test.go` (HTTP con Postgres y resolución real de tokens: CA-S04..CA-S08, RNF-S1 y, del parche, D-S2-8, D-S3-8 y D-S3-9).

**Archivos Nexo modificados.** `internal/nexo/authz/engine.go` y `identity.go` (política de red, `ReasonNetwork`), `cloudserver/nexo_authz_bridge.go` (política de red y `TokenScope`), `cloudserver/nexo_authz_routes.go` (`/me` con token y rutas de tokens), `cloudserver/nexo_clientctx.go` (comentario), `cloudstore/nexo_migrations.go` (llama a `security.MigrateTokens`).

### NX-07 · Endurecimiento de seguridad, fase S4 (evidencia y monitoreo) y arreglos A1/A2

**Especificación.** `docs/nexo/seguridad/` (`requirements.md` RS-30..RS-34, CA-S09..CA-S12, `design.md` §5 con desviaciones en §8.4, `tasks.md` S4-01..S4-05, `guia-despliegue.md` §7).

**Motivo.** La auditoría se podía alterar o borrar sin que se notara (la cuenta dueña, o cualquiera con una sola cuenta), no salía de la base, nadie se enteraba de una ráfaga de denegaciones o de un token revocado en uso, no había revisión de accesos registrada ni retención por tabla. Además (revisión del 2026-10-12): un token revocado en PCs viejas bloqueaba el token nuevo de la persona (A1) y un token mal pegado dejaba ~2 880 filas de auditoría por día (A2).

**Qué hace.**
- **A1**: sin bloqueo por persona por defecto (`user_max_failures: 0`); los intentos con un token revocado o de una persona deshabilitada quedan auditados con `nexo_principal_id` y `credential_id` y disparan la alerta `revoked_token_used`.
- **A2**: en `insertAuthAuditEvent` los rechazos `sync.auth`/`dashboard.login` repetidos (misma huella HMAC del token, IP y motivo) dejan una fila completa y un resumen por ventana con el conteo (`NEXO_AUDIT_DEDUP_SECONDS`, 600).
- **Cadena de hashes** (S4-01): `nexo_chain_seq`, `nexo_prev_hash`, `nexo_hash` en `cloud_auth_audit_log`, `nexo_authz_events` y `nexo_kb_events`; trigger con advisory lock por tabla y representación canónica estable; ancla inicial para las filas previas; puntos de control; guardas de solo inserción. Verificación `GET /nexo/admin/audit/verify` y `engram nexo audit verify`.
- **Exportación firmada** (S4-02): JSONL diario por tabla con `.sig` HMAC-SHA256 (`NEXO_AUDIT_EXPORT_DIR`, `NEXO_AUDIT_EXPORT_KEY`), registro en `nexo_audit_exports`, `engram nexo audit export --since` y `verify-export`.
- **Alertas** (S4-03): `nexo_alerts` y `nexo_alert_acks` (solo inserción), evaluador por minuto en una réplica, reglas del diseño más `revoked_token_used`, `invalid_token_repeated`, `inactive_account_disabled` y `audit_export_failed`, capa de cada alerta, webhook opcional, `GET /nexo/admin/alerts` y `POST /nexo/admin/alerts/{id}:ack`.
- **Revisión de accesos** (S4-04): `GET /nexo/admin/access-review`, `POST /nexo/admin/access-review:complete` (`nexo_access_reviews`), `NEXO_DISABLE_INACTIVE_DAYS`.
- **Retención** (S4-05): `nexo_purge_evidence(tabla, días)` `SECURITY DEFINER` con mínimos de 365 días, variables `NEXO_RETENTION_*` (vacías = no depurar), depuración del prefijo de la cadena con ancla.

**Archivos nuevos.**
- `internal/nexo/security/dedup.go` — `AuditDedup`, `CredentialFingerprint`, huella en el contexto (A2).
- `internal/nexo/security/evidence/chain.go` — tablas encadenadas, migración de la cadena, `Checkpoint`, `VerifyChain`, `VerifyAll`.
- `internal/nexo/security/evidence/retention.go` — guardas, `nexo_purge_evidence`, `Purge`, `RetentionFromEnv`.
- `internal/nexo/security/evidence/alerts.go` — tablas, reglas (`NEXO_ALERT_RULES`), `EvaluateOnce`, `ChainAlerts`, `InsertAlert`, `ListAlerts`, `AckAlert`.
- `internal/nexo/security/evidence/webhook.go` — `NEXO_ALERT_WEBHOOK_URL`, cuerpo Slack/Teams, reintentos.
- `internal/nexo/security/evidence/export.go` — `ExportDay`, `ExportRange`, `VerifyExport`.
- `internal/nexo/security/evidence/review.go` — `BuildAccessReview`, `CompleteReview`, `DisableInactive`.
- `internal/nexo/security/evidence/migrate.go` — `Migrate` (al final de las migraciones Nexo).
- `internal/nexo/security/auditcli/auditcli.go` — `engram nexo audit verify|export|verify-export`.
- `internal/nexo/nexocli/nexocli.go` — despachador de `engram nexo kb|audit`.
- `internal/cloud/cloudstore/nexo_audit_dedup.go` — `nexoAuditPrepare` (enganche A2), `NexoAuditSummaryEvent`, `NexoFlushAuditDedup`.
- `internal/cloud/cloudserver/nexo_evidence.go` — rutas de S4 y monitor.
- Tests: `internal/nexo/security/dedup_test.go`, `internal/nexo/security/evidence/{chain,alerts,export,review}_test.go` (Postgres: CA-S09..CA-S12, retención, dos cuentas, RNF-S2 y RNF-S3 con `NEXO_CHAIN_PERF_EVENTS`), `auditcli/auditcli_test.go`, `cloudserver/nexo_evidence_test.go` (HTTP: A1, A2, rutas, monitor con webhook y exportación).

**Archivos Nexo modificados.** `internal/nexo/security/lockout.go` (A1), `tokens.go` (huella en el contenedor del contexto), `dbaccounts.go` (`nexo_audit_chain_checkpoints` en `EvidenceTables`, `OwnerManagedTables`, `DefinerFunctions`), `internal/nexo/authz/migrate.go` y `internal/nexo/kb/store/migrate.go` (los triggers de solo inserción de `nexo_authz_events` y `nexo_kb_events` admiten el `DELETE` de la depuración segura), `internal/nexo/authz/events.go` (acción `principal.disabled_inactive`), `cloudstore/nexo_migrations.go` (llama a `evidence.Migrate`), `cloudserver/nexo_security.go` (agrupador en el contexto, huella del rechazo, estado del monitor), `cloudserver/nexo_admin_routes.go` (registra las rutas de S4), `cloudserver/nexo_activity.go` (arranca el monitor). Tests ajustados: `cloudserver/nexo_security_test.go` (D-S2-8 cuenta filas y resúmenes), `cloudstore/nexo_security_test.go` (tabla de evidencia ficticia), `internal/nexo/security/lockout_test.go`, `internal/nexo/authz/pg_test.go` (`TestPGAuthorizationCostWith100kBindings` con `NEXO_AUTHZ_PERF=1`).

### NX-07 · Endurecimiento de seguridad, fase S5 (respuestas y pruebas)

**Especificación.** `docs/nexo/seguridad/` (`requirements.md` RS-40..RS-43, CA-S13, CA-S14, `design.md` §6 con desviaciones en §8.5, `tasks.md` S5-01..S5-04). Backlog SG-01.

**Motivo.** Nada impedía que una ruta Nexo nueva saliera sin capacidad declarada, que una ruta nueva de Engram entrara sin revisión al integrar una versión, que un campo nuevo de una estructura saliera en una respuesta o que un token terminara en un log; y las pruebas de autorización no corrían como puerta en CI.

**Qué hace.**
- **Respuestas** (S5-01): revisión estática de los tipos de respuesta de `/admin/*`, `/nexo/*` y `/nexo/kb/v1` (etiqueta `json` explícita, sin nombres de secreto ni tipos de `database/sql`), contrato de campos de `/admin/*` de Engram y revisión de todas las respuestas reales de una corrida en el proceso. Corrige: los 500 de las rutas Nexo ya no devuelven el detalle del error (SQL, tablas; queda en el log) y `source_blob_ref` deja de salir en la API de conocimiento.
- **Registro de rutas** (S5-02): `internal/nexo/routes` y `s.nexoRoute(mux, método, ruta, capacidad, handler)`; todas las rutas `/nexo/*` y `/dashboard/kb*` declaran su capacidad (validada al registrar). `TestNexoRouteRegistryCoversMux` recorre el mux y falla con una ruta Nexo fuera del registro o una ruta de Engram fuera de `cloudserver/testdata/nexo_engram_routes.txt`.
- **Secretos en registros** (S5-03): corrida en el proceso (panel, emisión de tokens, sync, conocimiento, tokens inválidos, revocados y compartido, monitor) que revisa logs (`log`, `log/slog`), todas las filas del esquema, el webhook y las exportaciones.
- **CI** (S5-04): `.github/workflows/nexo-security.yml` con Postgres de servicio (`pgvector/pgvector:pg16`) corre las pruebas de autorización y seguridad como puerta (solo en `Adriel2503/engram-nexo`).

**Archivos nuevos.**
- `internal/nexo/routes/routes.go` — `Registry`, `Validate`, `Func`, marcadores `public`, `identity`, `token.write`.
- `internal/cloud/cloudserver/nexo_routes.go` — `nexoRoute` y cómo agregar una ruta.
- `internal/cloud/cloudserver/nexo_responses.go` — `nexoInternalError` (500 sin detalle).
- `internal/cloud/cloudserver/testdata/nexo_engram_routes.txt` — rutas de Engram revisadas.
- `.github/workflows/nexo-security.yml`.
- Tests: `internal/nexo/routes/routes_test.go`, `cloudserver/nexo_response_fields_test.go`, `nexo_leakscan_test.go`, `nexo_flow_test.go`, `nexo_routes_test.go`, `nexo_secrets_test.go`.

**Archivos Nexo modificados.** `cloudserver/nexo_admin_routes.go`, `nexo_authz_routes.go`, `nexo_tokens.go`, `nexo_evidence.go`, `nexo_health.go`, `nexo_kb_dashboard.go` (rutas con `nexoRoute` y 500 sin detalle), `nexo_kb_bridge.go` (`httpapi.RegisterRoutes`), `nexo_security.go` (campo `routes` en `nexoState`), `nexo_authz_bridge.go` (500 sin detalle), `internal/nexo/kb/httpapi/httpapi.go` y `routes2b.go` (capacidad por ruta; `Register` pasa a `RegisterRoutes`), `internal/nexo/kb/domain/domain.go` (`source_blob_ref` fuera de la API), `internal/nexo/kb/kbtest/http.go` y `kb/cli/cli_test.go` (registrador de prueba). Sin cambios en archivos de Engram.

### NX-10 · Espacios de conocimiento, entrega 2b tanda 1 (subida, formatos, gobierno, R0 y R1)

**Especificación.** `docs/nexo/conocimiento/` (`design.md` §17 y desviaciones D-K2b-1..D-K2b-21 en §18, `tasks.md`, `requirements.md` ajustado por las decisiones del 2026-10-13). Requisitos de producto: `nexo_ia/01-requisitos/Nexo_IA_Requisitos_e_Historias.md` (HU-29, HU-30, HU-33, HU-35..HU-39, RN-14, RN-15).

**Motivo.** El flujo real es que el responsable de un espacio sube archivos sueltos o una carpeta (md, Word, wiki exportada) desde el panel o con un comando; git no es requisito. La calidad de la búsqueda se mide con un conjunto de preguntas (R0) antes de mejorarla sin modelos (R1).

**Qué hace.**
- **Subida** (HU-29): `POST /nexo/kb/v1/spaces/{slug}/uploads` (multipart: archivos sueltos, carpeta con rutas relativas o un zip) con `space.publish` y token write, reutilizando la publicación por lotes (idempotencia, versiones, poda, dry-run). Defensas: tamaño por archivo, por subida y descomprimido; número de entradas; tipo por contenido; zip-slip; zip bomb; nombres saneados; límite de peticiones existente. CLI `engram nexo kb upload` y página del panel.
- **Fuentes y conversores**: `internal/nexo/kb/source` (fuentes `dir` y `azure-devops-wiki` por `.order`: títulos desde el nombre decodificado, ruta de carpetas, orden, páginas vacías saltadas, `[TO_REMOVE]` retiradas, `≤-AAAA` vencidas, `[TO_TIDY]` con etiqueta, adjuntos no indexados); conversor `ado-md` (sin `[[_TOC_]]`, `:::` como código, tablas HTML a Markdown); conversor Word `.docx` en Go puro (`archive/zip` + `encoding/xml`, sin dependencias nuevas).
- **Gobierno y temporalidad**: propuestas y revisión con conflicto de versión y fecha de entrada en vigencia (C-07, KP-08); feed `/sync` con vigentes y programadas (C-09, KP-07); "qué cambió desde X" `/changes` (RC-27); avisos `kb_doc_expiring` y `kb_doc_review_due` en `nexo_alerts` (KP-09); tope de la poda (KP-01); `nexo_kb_publish_runs` (KP-02); enlace permanente y `source_url` en cada resultado (RC-08).
- **R0**: `eval` con clases, Hit@5, Recall@5, MRR, nDCG@3/10, MAP y acierto de "no hay respuesta" (métricas portadas de WeKnora, MIT, ver `THIRD_PARTY_NOTICES.md`); `nexo_kb_eval_runs` y `POST/GET /eval-runs`; prueba de calidad en CI sobre un corpus sintético (`eval/quality_test.go`).
- **R1** (desactivable por `NEXO_KB_R1`): título y ruta en el texto indexado (RC-03, con reindexado seguro por versión de índice), tablas por filas (RC-04), hash por sección (RC-05), sinónimos y siglas (RC-06), umbral de cobertura (RC-07), diversidad (RC-09), vecinas (RC-10), modo `exact` (RC-22), `quote` (IN-07). Detección de secretos ampliada (SG-09: cadenas clave=valor, `Bearer`, `api-key`); la política ante una detección no cambia (se rechaza).

**Archivos nuevos.** `internal/nexo/kb/source/{source,ado,zip}.go`; `internal/nexo/kb/ingest/{ado,htmltable}.go`; `internal/nexo/kb/ingest/docx/docx.go` (+ `testdata/lineamiento-ejemplo.docx`); `internal/nexo/kb/store/{migrate2b,runs,changes,proposals,reindex}.go`; `internal/nexo/kb/search/{exact,synonyms}.go`; `internal/nexo/kb/service/{upload,proposals,changes,evalrun,r1,spaces2b}.go`; `internal/nexo/kb/httpapi/routes2b.go`; `internal/nexo/kb/cli/{eval,upload}.go`; `internal/nexo/kb/eval/metrics.go` (portado de WeKnora, aviso MIT en la cabecera); `internal/nexo/security/evidence/alerts_kb.go`; `internal/cloud/cloudserver/nexo_kb_dashboard.go`; `THIRD_PARTY_NOTICES.md`. Tests: `source/source_test.go`, `ingest/ingest2b_test.go`, `ingest/docx/docx_test.go`, `search/search2b_test.go`, `service/service2b_pg_test.go`, `service/wiki_eval_test.go` (medición sobre la wiki, solo con `NEXO_KB_WIKI_DIR`), `eval/quality_test.go` (+ `eval/testdata/mini/`, `preguntas-mini.yaml`), `cloudserver/nexo_kb_dashboard_test.go`.

**Archivos Nexo modificados.** `kb/domain/domain.go` (origen con colección, ruta de títulos, `source_url`, orden; acciones y códigos nuevos), `kb/ingest/{converter,sectioner,secrets}.go` (límite de entrada binaria, `ado-md` registrado, tablas por filas, anclas sin numeración, SG-09), `kb/store/{migrate,store}.go` (llama a `migrate2b`; `body_sha256`, texto indexado con contexto y tope de 256 KB por sección para el `tsvector`), `kb/search/{search,pgfts}.go` (umbral, diversidad, modo, enlace, cobertura, sinónimos, cita), `kb/service/{service,publish,documents}.go` (publicación común a lotes y subidas, retiro y vencimiento sugeridos por la fuente, tope de poda, vecinas), `kb/httpapi/httpapi.go` (registra las rutas nuevas; `mode`, `neighbors` y enlace absoluto en `/search`), `kb/cli/cli.go` (comandos `upload`, `changes`, `reindex`; `--mode`), `kb/eval/eval.go` (clases, métricas, marcas `@base`, comparación con la última corrida aceptada), `security/dbaccounts.go` (`nexo_kb_publish_runs` y `nexo_kb_eval_runs` como evidencia), `security/evidence/alerts.go` (reglas de la capa conocimiento), `cloudserver/nexo_kb_bridge.go` (registra también las páginas del panel). **Sin enganches nuevos en archivos de Engram** (la página del panel se registra desde `nexo_kb_bridge.go`; la barra de navegación no se tocó para no regenerar `components_templ.go`: el panel se abre en `/dashboard/kb` y desde los enlaces de las citas).

**Paso de actualización.** Con R1 por defecto (`doc_context` activo), una base con secciones de 2a necesita un reindexado: `engram nexo kb reindex` (superadmin) o `POST /nexo/kb/v1/admin/index:reindex`. Las secciones nuevas ya se indexan con la composición nueva.

### NX-10 · Espacios de conocimiento, entrega 2b tanda 2 (copia local, MCP, CLI y kit)

**Especificación.** `docs/nexo/conocimiento/design.md` §19 (desviaciones D-K2b-22..D-K2b-30), `tasks.md` (D-01, D-02, D-03, D-04, F-04).

**Motivo.** Que el dev consulte el conocimiento oficial desde su asistente (Claude Code, Codex, Cursor) o la terminal, con cita y enlace, sin inventar cuando no hay estándar, respetando vigencias y sin red.

**Qué hace.**
- **Copia local** (D-01, KP-07): SQLite propio con FTS5 (BM25) en `NEXO_KB_LOCAL_DB`, `$NEXO_HOME/kb.db` o `~/.nexo/kb.db`, alimentado por `/nexo/kb/v1/sync`; guarda vigentes y programadas con su vigencia y filtra al consultar con el reloj local; borra los espacios que dejan de ser visibles; relee una ventana de solape (D-K2b-22); se reconstruye si cambia la persona o el servidor. Nunca toca `engram.db`.
- **Herramientas MCP** (D-02): `nexo_search`, `nexo_get`, `nexo_spaces`, `nexo_changes`, `nexo_propose`, `nexo_proposals`, `nexo_review`, `nexo_whoami`, con `at`/`known_at` donde aplica; cita, enlace permanente y `source_url` en cada resultado; "no existe un estándar oficial" explícito; sin servidor responden desde la copia local marcada con su fecha. Se registran con `--tools` que las nombre o incluya `nexo`, y solo con servidor y token configurados.
- **CLI** (D-03): `engram nexo kb get`, `sync`, `search --local`, `eval --retriever local-bm25`, `import-transversal` (migra `nexo-transversal` al espacio `general`: Oficial -> documentos, Propuesta -> propuestas; idempotente, `--dry-run`, base de Engram en solo lectura o `--from` un export).
- **Servidor** (aditivo, sin migraciones): `/sync` informa `public_url` y `source_url` por sección; la lectura de una sección informa `link` y `source_url`.
- **Plantilla de GitHub Actions** (D-04, opcional): `docs/nexo/conocimiento/github-actions/`.

**Archivos nuevos.** `internal/nexo/kb/client/{api,local,sync,kb,mcp}.go`; `internal/nexo/kb/cli/{local,importtransversal}.go`; `internal/nexo/kb/kbtest/http.go` (servidor de prueba de la API); `docs/nexo/conocimiento/github-actions/{publicar-conocimiento.yml,LEEME.md}`. Tests: `client/local_test.go` (copia local sin servidor), `client/client_pg_test.go` (contrato de las 8 herramientas por un cliente MCP en proceso, sincronización, vigencias, servidor caído, acceso quitado, propuesta y revisión), `cli/tanda2_test.go` (`sync`, `get`, `search --local`, `import-transversal` y lectura de `engram.db` sin modificarla).

**Archivos Nexo modificados.** `kb/cli/{cli,upload,eval}.go` (cliente HTTP movido a `client`, comandos nuevos, `--local`, `local-bm25`), `kb/cli/cli_test.go` (el motor `local-bm25` ya existe; se prueba `hybrid` como pendiente), `kb/store/changes.go` (`source_url` en el feed), `kb/service/{changes,documents}.go` (`public_url`, `link` y `source_url`), `kb/httpapi/{httpapi,routes2b}.go` (enlaces absolutos).

**Kit (repositorio de Nexo, `02-nexo-kit`, v0.3.0; hoy histórico, ver abajo).** Skill `nexo-protocol` v0.3 (lo transversal por `nexo_*`), plugin con las herramientas `nexo_*` en `--tools`, hook de inicio con `engram nexo kb sync` tolerante, instalador y `Sync-Nexo.ps1` que también actualizan la copia local, README.

**Cambio de alcance de F-04 (decisión del usuario, 2026-10-13; `design.md` §19.4).** La skill y la instalación de Nexo viven en el fork; el kit queda como histórico.
- `engram nexo connect --server <url> (--token-stdin | --token <t> | NEXO_TOKEN)`: valida con `/nexo/authz/v1/me`, guarda servidor y token en `cloud.json` (0600), escribe el marcador `nexo.json` (sin token) y sincroniza la copia local; `engram nexo status`, `engram nexo disconnect [--keep-copy]`, `engram nexo setup <agente>`. Multiplataforma.
- `engram setup <agente>` con la PC conectada instala además el protocolo Nexo: skill `nexo-protocol` en opencode (claude-code y codex la reciben dentro del plugin del fork, ver abajo); bloque administrado `<!-- BEGIN NEXO PROTOCOL (NX-10) ... -->` en el archivo de reglas de pi, gemini-cli, antigravity-cli, windsurf, qwen, kiro, kilocode, commandcode y kimi; archivo propio en cursor (`nexo-protocol.md`) y vscode-copilot (`nexo.instructions.md`). Sin conexión, `engram setup` no cambia.
- Con la PC conectada, `engram mcp --tools=agent` (el que escribe `engram setup`) registra las herramientas `nexo_*`.
- Fuente única de la skill: `internal/nexo/setup/SKILL.md` (embebida); `go generate ./internal/nexo/setup/` copia a `plugin/claude-code/skills/nexo-protocol/SKILL.md` y `plugin/codex/skills/nexo-protocol/SKILL.md` (archivos nuevos); una prueba verifica que coincidan.
- Marketplace del plugin hacia `Adriel2503/engram-nexo` (D-K2b-35, opción A, decisión del usuario 2026-10-13): Claude Code (`claude plugin marketplace add Adriel2503/engram-nexo`, toma la rama por defecto de GitHub) y Codex (`--ref`) instalan. Desde el 2026-10-13 ambos usan **`main`**, la rama estable: la rama por defecto de GitHub pasó de `develop` a `main` y `codexMarketplaceRef = "main"`. Un cambio de la skill o del plugin llega a los devs solo cuando se pasa a `main`; conviene además subir la versión de `plugin.json` (y de `marketplace.json`) para que las PCs que ya lo tienen lo actualicen; esquema de versión en «Correcciones de los hallazgos de los manuales». Ambos el plugin `engram` del fork, que ya trae `skills/nexo-protocol`. Para esos dos agentes `engram setup` ya no copia la skill aparte y borra copias directas anteriores (`removeDirectNexoSkill`). Requisito de la demo: cada persona necesita acceso de lectura al repositorio privado y credenciales de git para GitHub (HTTPS). Verificado con `CLAUDE_CONFIG_DIR` y `CODEX_HOME` temporales: el plugin instalado trae `memory` y `nexo-protocol`. Para producción queda la opción B (plugin embebido en el binario como marketplace local, sin acceso al repositorio), junto con OP-01.
- Archivos nuevos: `internal/nexo/setup/{nexosetup.go,SKILL.md}`, `internal/nexo/kb/client/connect.go`, `internal/nexo/nexocli/{connection.go,connection_test.go}`, `internal/setup/{nexo_setup.go,nexo_setup_test.go}`. Archivo Nexo modificado: `internal/nexo/nexocli/nexocli.go` (comandos nuevos).

### NX-10 · Panel web de Nexo IA para la presentación (marca, conocimiento, buscador)

**Especificación.** `docs/nexo/panel/design.md` (audiencia, wireframes, tokens, principios y revisión contra clichés).

**Motivo.** El panel mostraba la marca y los textos de Engram en inglés, no tenía pestaña de conocimiento, la lista de documentos se desbordaba, los documentos se veían como Markdown crudo y no se podía buscar desde el panel.

**Qué hace.**
- **Marca y textos** (P1): con el servidor de Nexo, el panel se llama "Nexo IA" ("Memoria y estándares del equipo"), en español (`lang="es"`), con tema claro propio (`static/nexo_theme.css`, cargado solo con la marca activa) y pestaña "Conocimiento". `dashboard.EnableNexoBrand()` lo activa al registrar las páginas de conocimiento; sin esa llamada (pruebas de Engram, Engram original) el panel queda igual que en Engram. Los textos del marco, menú, login e inicio pasan por `nx()` (mapa en `nexo_brand.go`). El pie y el login dicen "Basado en Engram Cloud (código abierto)". El error del login conserva el código de Engram en `data-error-code` y muestra el texto en español.
- **`/dashboard/kb`** (P2): buscador en todos los espacios visibles, "Cómo funciona" con 4 pasos y su estado para quien mira (Hecho, Pendiente, No aplica a tu rol; el paso 4 dice "Disponible"), espacios como tarjetas (visibilidad, tu rol, vigentes, retirados, última actualización) y formulario de creación con etiquetas.
- **Espacio** (P3): buscador (`?q=&at=`) con el mismo `Service.Search` que `/nexo/kb/v1/search` (permisos, vigencia, R1): cita (ruta de títulos), estado, vigencia, fragmento con términos resaltados, enlace a la sección y a la fuente; sin resultados, "No existe un estándar oficial publicado sobre este tema." Documentos en árbol plegable por carpeta (`<details>`), título como texto principal, ruta en gris, estados como chips (Vigente, Programado, Vencido, Retirado), filtros como botones, sin tablas anchas. Formulario de subida para responsables y publicadores (archivos, carpeta o zip; "Probar sin publicar" y "Publicar"); últimas subidas solo con `space.audit.read` (antes se pedían siempre y generaban denegaciones en la auditoría).
- **Documento** (P4): Markdown renderizado a HTML seguro con `internal/nexo/kb/mdhtml` (propio, sin dependencias: títulos, listas anidadas, código, tablas, citas, negrita, cursiva, enlaces; todo escapado, sin HTML del documento, enlaces solo http, https, mailto o relativos; imágenes como enlace), índice de secciones, versiones con "Qué cambió" (`&changes=1`: secciones nuevas, quitadas y cambiadas entre la versión y la anterior). `splitSections` ahora corta por títulos y asigna las anclas indexadas en orden (antes cortaba por líneas de procedencia y perdía el final de algunas secciones).

**Archivos nuevos.** `internal/cloud/dashboard/nexo_brand.go`, `internal/cloud/dashboard/nexo_brand.templ` (+ `nexo_brand_templ.go` generado), `internal/cloud/dashboard/static/nexo_theme.css`, `internal/cloud/cloudserver/nexo_kb_dashboard_view.go`, `internal/nexo/kb/mdhtml/mdhtml.go`, `docs/nexo/panel/design.md`. Tests: `internal/nexo/kb/mdhtml/mdhtml_test.go` (render, XSS, esquemas de enlace), `internal/cloud/cloudserver/nexo_kb_panel_test.go` (buscador, mensaje sin estándar, fecha, árbol, Markdown seguro, qué cambió, espacio restringido invisible para quien no es miembro, login en español).

**Archivos Nexo modificados.** `cloudserver/nexo_kb_dashboard.go` (páginas rediseñadas, activa la marca), `cloudserver/nexo_kb_dashboard_test.go` (texto "Resultado de la subida").

### NX-10 · R2: búsqueda por significado (híbrida palabras + vectores)

**Especificación.** `docs/nexo/conocimiento/design.md` §20 (diseño, desviaciones D-R2-1..D-R2-12 y medición R0), `tasks.md` (R2), backlog `docs/nexo/mejoras-y-referencias.md` (RC-05, RC-07, RC-11, RC-12, RC-16, RC-19, RC-20, RC-21, RC-23, RC-29, RC-30).

**Motivo.** Tras R1, la medición R0 sobre la wiki dejaba las paráfrasis en 0 % y 3 falsos "no hay estándar": las preguntas formuladas con otras palabras no se encuentran por texto. Decisión del usuario (2026-10-13): proveedores externos de embeddings intercambiables por configuración, Postgres oficial con pgvector, y que todo se degrade a la búsqueda por texto si falta algo.

**Qué hace.**
- **Proveedores** (`internal/nexo/kb/embed`): interfaz `Embedder` (proveedor, modelo, dimensiones, `Embed(ctx, textos, documento|consulta)`) con una capa común que resuelve lotes, reintentos con espera exponencial (respeta `Retry-After` y `retryDelay`), límites por minuto (`RPM`, `TPM`), tiempo máximo por llamada, comprobación de cantidad y dimensión de cada respuesta, normalización y borrado de la clave en los errores. Adaptadores: Gemini (`batchEmbedContents`, `RETRIEVAL_DOCUMENT`/`RETRIEVAL_QUERY`, `outputDimensionality`, clave en `x-goog-api-key`), OpenAI (`text-embedding-3-small`, `-3-large`, `ada-002`), Azure OpenAI (despliegue, `api-version`, `api-key`) y compatible con OpenAI (Ollama, vLLM, TEI, URL propia). Tabla de modelos con dimensión nativa, rango, reducción, lote, tokens y normalización; la configuración se valida al arrancar. Prueba de contrato común con un servidor HTTP simulado.
- **Datos** (`store/vectors.go`, migración aditiva): extensión `vector` tolerante (sin ella, solo texto), `nexo_kb_section_vectors` (vector por sección y versión de índice, con hash del texto para reutilizar), índices HNSW parciales por dimensión sobre `embedding::vector(D)` (768, 1024, 1536 y la configurada) y `embedding::halfvec(D)` por encima de 2000 (3072), cola `nexo_kb_index_jobs` (`FOR UPDATE SKIP LOCKED`, deduplicada por versión y sección, `fail_count`) y cartas muertas `nexo_kb_index_dead` con la alerta `kb_index_dead_letter`. Versiones de índice `vector` en `nexo_kb_index_versions` (proveedor, modelo, dimensiones, composición del texto) con `feed_seq`.
- **Indexador** (`service/r2.go`): en segundo plano dentro de `engram cloud serve`; se alimenta de `nexo_kb_changes`; vectoriza solo el texto con encabezado de contexto (RC-03); reutiliza vectores de secciones idénticas (RC-05); cambio de proveedor, modelo o dimensión = versión nueva construida en segundo plano y activada al terminar (RC-16); el trabajo de una versión retirada se descarta.
- **Búsqueda** (`search/{hybrid,vector,rrf}.go`): modo `hybrid` con la misma consulta y los mismos filtros (permisos, espacio, `at`/`known_at`, retirados) por texto (pgfts con R1) y por vectores (HNSW con `ef_search` e `iterative_scan`, o recorrido exacto con pocas secciones y en consultas históricas, RC-19), fusión RRF ponderada y normalizada (portada de WeKnora, MIT), un solo modelo por consulta (RC-23), umbral de "no hay estándar" recalibrado con cobertura y similitud, diversidad y vecinas de R1. Si el proveedor falla, la respuesta es solo por texto con `vector.status` y `notice`, sin error. Por defecto `hybrid` con índice activo y `fts` sin él.
- **Superficies**: `mode=hybrid` en la API, `nexo_search` (MCP), panel (selector "Cómo buscar"), `engram nexo kb search --mode hybrid`, `eval --retriever hybrid [--wait-index]`, `reindex --vectors [--requeue-dead]`; `GET /admin/index` informa la cola. La copia local de la PC sigue solo con BM25 (RC-30).

**Archivos nuevos.** `internal/nexo/kb/embed/{embed,models,resilient,gemini,openai}.go` (+ `embed_test.go`); `internal/nexo/kb/store/vectors.go`; `internal/nexo/kb/search/{hybrid,vector}.go`, `search/rrf.go` (derivado de WeKnora, aviso MIT en la cabecera); `internal/nexo/kb/service/r2.go` (+ `r2_pg_test.go`); `internal/nexo/kb/kbtest/embedder.go` (embedder simulado determinista para pruebas sin red).

**Archivos Nexo modificados.** `kb/store/{migrate,store}.go` (llama a `migrateR2`; estado de pgvector), `kb/search/{search,pgfts}.go` (modo `hybrid`, similitud, umbral combinado, aviso; consulta de texto partida para armar los resultados que solo encontró el vector), `kb/service/{service,r1,evalrun}.go` (motor híbrido si hay proveedor; umbral; modo y configuración en las corridas), `kb/httpapi/routes2b.go` (estado y reconstrucción del índice vectorial), `kb/cli/{cli,eval,upload,local}.go`, `kb/client/{kb,mcp}.go`, `kb/eval/quality_test.go` (prueba de calidad del modo hybrid con el embedder simulado), `kb/service/wiki_eval_test.go` (medición con proveedor real y caché de vectores), `cloudserver/nexo_kb_bridge.go` (arranca el indexador), `cloudserver/nexo_activity.go` (llamada al arranque, sin enganche nuevo en `Start()`), `cloudserver/nexo_kb_dashboard{,_view}.go` (selector de modo y aviso). **Sin enganches nuevos en archivos de Engram.**

**Paso de actualización.** Postgres con la extensión `vector` (imagen `pgvector/pgvector:pg16` o `CREATE EXTENSION vector` hecho por un DBA, pgvector 0.8 o superior para `iterative_scan`), `NEXO_KB_EMBEDDER` y su clave en el entorno del servidor. Al arrancar se crea la versión de índice y el indexador vectoriza todo en segundo plano; mientras tanto la búsqueda sigue por texto. Las secciones y las preguntas se envían al proveedor: informar a Real Plaza antes de producción.

### OP-01 · Publicación de versiones del fork (workflows)

Motivo: al subir la etiqueta `v3.4.0` de Engram al fork, los workflows de Engram crearon un release con los binarios originales e intentaron actualizar el Homebrew de Gentleman Programming (falló por credenciales). Desde el 2026-10-13:
- **Etiquetas.** Solo las propias `v<engram>-nexo.<n>` (por ejemplo `v3.4.0-nexo.1`) publican, y solo en `Adriel2503/engram-nexo`. Las etiquetas de Engram ya no disparan nada; aun así, no se suben etiquetas de Engram al fork.
- **Binarios** (`release.yml`): exige que la etiqueta esté en `main`, usa `.goreleaser.nexo.yaml` (la de Engram sin `brews`, título "Nexo IA <etiqueta>", no se marca como prerelease aunque lleve `-nexo.N`). La ejecución manual (`gh workflow run release.yml`) compila con `--snapshot` sin publicar.
- **Imagen del servidor** (`cloud-image.yml`): construye `deploy/nexo/Dockerfile` para amd64 y arm64 y la sube como `ghcr.io/adriel2503/nexo-engram-cloud:<versión>` (y `latest` si la etiqueta está en `main`). La ejecución manual solo construye.
- **Paquete npm de Pi** (`publish-pi.yml`): solo corre en el repositorio de Engram.
- Cómo publicar una versión: pasar a `main`, esperar el CI verde, `git tag v3.4.0-nexo.1 && git push origin v3.4.0-nexo.1`.

### Documentación · Manuales de usuario

- `docs/nexo/manuales/` (2026-10-13): índice `README.md` y manuales `instalacion-dev.md` (dev en Windows, macOS y Linux), `servidor-local.md` (servidor en la propia PC con Docker), `servidor-produccion.md` y `administracion.md`. Solo documentación; no cambia código.

### NX-10 · Correcciones de los hallazgos de los manuales (2026-10-13)

**Motivo.** Al escribir los manuales aparecieron fallas de instalación, operación y ayuda.

**Qué hace.**
- **Versión propia del plugin.** El plugin del fork se sigue llamando `engram` (el nombre fija las herramientas `mcp__plugin_engram_engram__*` y el catálogo `engram@engram`), pero con versión propia para que Claude Code y Codex detecten la actualización: Claude Code `0.1.1201` (`.claude-plugin/marketplace.json` y `plugin/claude-code/.claude-plugin/plugin.json`, siempre iguales) y Codex `0.1.501` (`plugin/codex/.codex-plugin/plugin.json`). **Esquema:** `<mayor>.<menor>.<parche de Engram * 100 + n>`, con `n` de 1 a 99 = número de publicación de Nexo sobre esa versión de Engram. La siguiente publicación sobre Engram 0.1.12 es `0.1.1202`; si Engram pasa a `0.1.13`, el fork usa `0.1.1301`; si pasa a `0.2.0`, `0.2.1`. Es SemVer sin prerelease (un `-nexo.1` ordenaría **antes** que la versión de Engram), ordena después de la versión de Engram de la que sale y cumple el piso de `meetsClaudeCodeSlimPluginFloor`. La descripción del plugin dice que es el fork con la skill nexo-protocol. Prueba: `internal/setup/nexo_marketplace_test.go` (`TestNexoPluginVersionScheme`).
- **Catálogo `engram` ajeno.** `engram setup claude-code` y `engram setup codex` leen el catálogo `engram` ya registrado (Claude Code: `plugins/known_marketplaces.json` bajo `CLAUDE_CONFIG_DIR` o `~/.claude`; Codex: tabla `[marketplaces.engram]` de `config.toml`). Si apunta a otro origen (por ejemplo `Gentleman-Programming/engram`) o, en Codex, a otra rama que `main`, lo quitan (`plugin marketplace remove engram`) antes de agregar el del fork; si no pueden, avisan con los comandos manuales y siguen. Sin catálogo o con el del fork, los comandos son los de antes. Archivo nuevo `internal/setup/nexo_marketplace.go` (+ prueba con `claude` y `codex` simulados).
- **Enlaces con https detrás del proxy.** `BaseURL` (`internal/nexo/kb/httpapi`) usa `X-Forwarded-Proto` y `X-Forwarded-Host` solo si la conexión viene de un proxy de `NEXO_TRUSTED_PROXIES` (el middleware de NX-04 los captura en `NexoClientContext`; el puente del conocimiento los pasa con `httpapi.WithForwardedOrigin`). `NEXO_KB_PUBLIC_URL` sigue mandando. Archivos: `httpapi/publicurl.go` (nuevo), `httpapi/routes2b.go`, `cloudserver/nexo_clientctx.go`, `cloudserver/nexo_kb_bridge.go`. Pruebas: `httpapi/publicurl_test.go`, `cloudserver/nexo_publicurl_test.go`.
- **`engram nexo disconnect`** solo borra servidor y token de `cloud.json` si coinciden con la conexión de `nexo.json`; sin `nexo.json` o con otro servidor, no lo toca e informa (`engram cloud config --clear` para quitarlo). `client.Disconnect` devuelve `DisconnectResult`. Prueba: `client/disconnect_test.go`.
- **Skill** `nexo-protocol` v0.3.1: recomienda `engram nexo connect --server <url> --token-stdin` y no escribir el token en el comando ni en el chat (copias del plugin regeneradas).
- **Disponibilidad real.** `GET /nexo/health/ready` (sin autenticación): `SELECT 1` en la base con 2 s de espera; 200 `{"status":"ready"}` o 503 `{"status":"unavailable"}`, sin detalles. `/health` de Engram no cambia (sigue respondiendo `ok` siempre). Grupo de límite `nexo` (300 por minuto por IP). Se registra desde `registerNexoAdminRoutes` (sin enganche nuevo). Archivo nuevo `cloudserver/nexo_health.go` (+ prueba con base caída, sin base y con Postgres).
- **CLI.** `engram help` lista `nexo`; `engram nexo help` lista `reindex` entre los comandos de `kb`; la ayuda de `kb sync` nombra `NEXO_KB_LOCAL_DB` y `NEXO_HOME`; `engram nexo kb search` sin `--local` cae a la copia local si el servidor no responde, con el aviso "El servidor de Nexo no respondió (...)" (con `--known-at` no cae: la copia no guarda el historial). `client.SearchParams.LocalReason` para el aviso. Pruebas: `nexocli/help_test.go`, `cmd/engram/nexo_help_test.go`, `kb/cli/tanda2_test.go`.
- **Documentación.** `conocimiento/design.md` §19.4 (D-K2b-33), `seguridad/guia-despliegue.md` §6 (log real), `deploy/nexo/Dockerfile` (comentarios y descripción), §6 de este archivo (volver a la imagen oficial) y los manuales.
- **Pendiente: textos del panel en inglés** ("Create User", "Create Token", "Expires in days (default 90)"). Pasarlos por `nx()` exige regenerar `components_templ.go`, y templ renumera sus variables: el diff fue de unas 800 líneas en un archivo de Engram. Se dejó como está; anotado en `docs/nexo/mejoras-y-referencias.md`.

### SG-10 · Cuentas de servicio para CI y bots

**Especificación.** `docs/nexo/seguridad/cuentas-servicio.md` (nota de diseño), backlog SG-10 en `docs/nexo/mejoras-y-referencias.md`, manual `docs/nexo/manuales/administracion.md` §3.8. Reglas de base: RS-20..RS-24, DS-2, D-S3-5, D-S3-6 (seguridad) y R-10 (roles).

**Motivo.** Los pipelines de CI y los bots publicaban con el token de una persona (o de un usuario dedicado que se hacía pasar por persona): sin responsable, sin rotación ordenada y sin forma de ver desde dónde se usaba el token.

**Qué hace.**
- **Cuenta de servicio** = `cloud_principals` con `kind = 'service_account'` (ya admitido por Engram) + tabla lateral `nexo_service_accounts` (slug, descripción, persona responsable obligatoria, quién la creó). Trigger: la fila es de una cuenta de servicio y el responsable es una persona. `ResolvePrincipal` acepta el slug donde acepta un usuario (API de roles, `POST /nexo/authz/v1/tokens`); los listados de miembros muestran el slug como usuario.
- **API** `/nexo/authz/v1/service-accounts`: listado y detalle (`platform.directory.read`), alta, cambio de descripción o responsable, `:disable` y `:enable` (`platform.users.manage`), emisión, `:rotate` (gracia opcional de 0 a 10080 minutos: el anterior se revoca o vence a la gracia) y `:revoke` (`platform.tokens.manage`). Sin caducidad solo un superadmin (mismo `nexoTokenIssueContext`). Deshabilitada, todos sus tokens se rechazan en la siguiente petición.
- **Roles**: con la API de roles de siempre (`PUT /nexo/authz/v1/scopes/{type}/{key}/members/{slug}`); R-10 la hacen cumplir el servicio y el trigger existente (responsable o rol de plataforma: 422).
- **Último uso** por token: `last_used_at` y la última acción registrada (hora, IP, cliente y fuente: `sync`, `kb` o `authz`) de `nexo_actor_presence`, `nexo_kb_events` y `nexo_authz_events` (`security.LoadTokensLastSeen`), sin escrituras nuevas por petición.
- **Auditoría**: eventos `service_account.created`, `.updated`, `.disabled`, `.enabled`, `.token_issued`, `.token_rotated`, `.token_revoked` en `nexo_authz_events` (filtro `?action=service_account.*`); las denegaciones dejan `authz.denied`. El token en claro solo va en la respuesta de emisión o rotación; nunca en logs ni eventos (id y prefijo).
- **CLI** `engram nexo sa` (`list`, `show`, `create`, `update`, `disable`, `enable`, `grant`, `ungrant`, `token issue|rotate|revoke`): el token nuevo es lo único que se imprime en la salida estándar, o va a `--out` con permisos 0600.
- **Panel** `/dashboard/admin/service-accounts` (pestaña "Cuentas de servicio" para admins): listado, alta, detalle con tokens y último uso, deshabilitar y habilitar, emitir, rotar y revocar; CSRF del panel de conocimiento; el token se muestra una vez (`Cache-Control: no-store`).

**Archivos nuevos.** `internal/nexo/authz/serviceaccounts.go`, `internal/nexo/security/tokenusage.go`, `internal/nexo/nexocli/serviceaccounts.go` (+ `_test.go`), `internal/cloud/cloudserver/nexo_service_accounts.go`, `nexo_service_accounts_dashboard.go`, `nexo_service_accounts_test.go` (Postgres: ciclo completo con publicación en un espacio con el token de la cuenta, rotación con y sin gracia, revocación, deshabilitar, R-10, denegaciones, un evento por acción, el token nunca en logs, eventos ni respuestas; panel), `internal/cloud/dashboard/nexo_service_accounts.go`, `nexo_service_accounts.templ` y `nexo_service_accounts_templ.go` (templ v0.3.1001), `docs/nexo/seguridad/cuentas-servicio.md`.

**Archivos Nexo modificados.** `internal/nexo/authz/service.go` (slug en `resolvePrincipal`), `migrate.go` (tabla lateral), `events.go` (acciones), `pgstore.go` (slug en los listados de miembros); `cloudserver/nexo_tokens.go` (emisión extraída a `nexoCreateManagedToken`; evento al emitir para una cuenta de servicio), `cloudserver/nexo_authz_routes.go` (registro de las rutas); `dashboard/nexo_brand.templ` y `nexo_brand_templ.go` (pestaña, +12/-4 líneas generadas); `internal/nexo/nexocli/nexocli.go` (área `sa`); manual de administración y `docs/nexo/conocimiento/github-actions/LEEME.md`.

### SG-10 · Cuentas de servicio sin uso: aviso, nunca deshabilitar

**Especificación.** `docs/nexo/seguridad/cuentas-servicio.md` §"Inactividad", manual `docs/nexo/manuales/administracion.md` §3.8.

**Motivo.** `NEXO_DISABLE_INACTIVE_DAYS` (S4-04) no miraba el tipo de cuenta: un pipeline de CI que publica cada pocos meses quedaba deshabilitado en silencio y el CI fallaba sin aviso.

**Qué cambia.**
- **`NEXO_DISABLE_INACTIVE_DAYS` solo deshabilita personas** (`kind = 'human'`). Las cuentas de servicio (y las `legacy`) nunca se deshabilitan por inactividad.
- **`NEXO_SA_STALE_DAYS`** (por defecto 180, `0` = desactivado): una cuenta de servicio habilitada sin uso en ese plazo deja la alerta `service_account_stale` (capa plataforma, severidad `warning`) en `nexo_alerts`, con su nombre corto y su responsable. No se deshabilita. La regla se apaga o cambia de severidad con `NEXO_ALERT_RULES` como las demás. La corre la pasada diaria del monitor (`nexoDailyJobs`, una réplica a la vez).
- **Último uso** = función SQL `nexo_principal_last_use(bigint)`: el último `last_used_at` de sus tokens o la última acción registrada con su identidad (`nexo_actor_presence`, `nexo_kb_events`, `nexo_authz_events`), lo mismo que muestra el detalle de la cuenta. Nunca usada: cuenta desde el alta. Emitir o rotar un token no cuenta como uso.
- **Sin repetición**: la clave de deduplicación es `service_account_stale|<principal>|<último uso o alta>`; la pasada siguiente no repite y reconocer la alerta la cierra. Si la cuenta se vuelve a usar y otra vez queda sin uso, avisa de nuevo (otra clave).
- **Revisión de accesos** (`GET /nexo/admin/access-review`): las cuentas de servicio usan el mismo último uso, muestran su slug como usuario, llevan la marca `stale` (no `inactive`) y el resumen suma `service_accounts_stale`; el reporte informa `sa_stale_days`.
- **API, CLI y panel**: las cuentas de servicio informan `last_activity_at`, `idle_days`, `stale` y `stale_after_days`; `engram nexo sa list` agrega la columna `ULTIMO USO` y la marca `SIN USO`; `sa show` y el panel (columna "Último uso", marca "Sin uso" y aviso en el detalle) dicen "sin uso desde hace N días" o "nunca usada (creada hace N días)".

**Archivos nuevos.** `internal/nexo/security/evidence/serviceaccounts.go` (+ `_test.go`, Postgres: `DisableInactive` salta cuentas de servicio y sigue deshabilitando personas; la alerta sale una vez para una cuenta vieja sin uso, no sale para una usada hace poco ni con `NEXO_SA_STALE_DAYS=0`; revisión de accesos; lectura de la variable), `internal/cloud/cloudserver/nexo_service_accounts_stale_test.go` (API y panel), `internal/nexo/nexocli/serviceaccounts_stale_test.go`.

**Archivos Nexo modificados.** `internal/nexo/authz/serviceaccounts.go` (función SQL, campos y marca), `service.go` (`WithServiceAccountStaleDays`); `internal/nexo/security/evidence/review.go` (filtro `kind = 'human'`, marca `stale`), `alerts.go` (regla `service_account_stale`); `cloudserver/nexo_authz_bridge.go` (días del aviso), `nexo_evidence.go` (pasada diaria y log de arranque), `nexo_service_accounts.go` (campos del detalle), `nexo_service_accounts_dashboard.go`; `dashboard/nexo_service_accounts.go`, `.templ` y `_templ.go` (regenerado solo ese archivo con templ v0.3.1001); `internal/nexo/nexocli/serviceaccounts.go`. Ningún archivo de Engram cambia.

### NX-10 · Panel, fase K6 (miembros, propuestas y panel en español)

**Especificación.** `docs/nexo/panel/design.md` §9 (diseño), `docs/nexo/mejoras-y-referencias.md` (KP-12, KP-13, SG-07, OP-06) y `docs/nexo/manuales/administracion.md` (4.3 y 6.1).

**Motivo.** Los miembros de un espacio y las propuestas solo se manejaban con la API, la CLI o las herramientas `nexo_*`, y varias páginas del panel de Engram seguían en inglés (OP-06).

**Qué hace.**
- **Miembros del espacio** (KP-12): `GET /dashboard/kb/spaces/{slug}/members` (`space.read` o `platform.directory.read`), `POST .../members` y `POST .../members/remove` (`space.members.manage`, con CSRF). Lista por rol con tipo (persona o cuenta de servicio) y fecha; el responsable agrega, cambia y quita; los demás ven solo la lista. Usa `authz.Service.ListScopeMembers` y `SetScopeMember`, así que las reglas (R-1, R-9, R-10, R-11) y los eventos de `nexo_authz_events` son los de la API; los rechazos salen en español en la misma página. Un administrador de plataforma sin rol de responsable en el espacio no cambia miembros (lo decide el motor, R-3), solo lee.
- **Propuestas** (KP-13, SG-07): `GET /dashboard/kb/proposals` (todas las visibles), `GET /dashboard/kb/proposals/badge` (fragmento htmx de la pestaña), `GET /dashboard/kb/spaces/{slug}/proposals[/{id}]` (`space.read`), `POST .../proposals/{id}/approve` y `.../discard` (`space.review`, con CSRF). Detalle con la versión vigente y el texto propuesto lado a lado (`internal/nexo/kb/mdhtml`), aviso si el documento cambió, casilla de `force` solo cuando hace falta, vigencia opcional y motivo obligatorio al descartar. Usa `kbservice.ApproveProposal` y `DiscardProposal` (mismos eventos `proposal.approved` y `proposal.discarded`). Pestaña "Propuestas" en el menú con insignia para quien revisa.
- **Panel en español** (OP-06): las cadenas fijas de `components.templ` (usuarios, tokens, permisos de proyecto, salud, auditoría, memorias, sesiones, proyectos y colaboradores) pasan por `nx()`; la traducción vive en `dashboard/nexo_text_es.go`. Sin la marca de Nexo el panel de Engram queda igual. `components_templ.go` se regeneró con `go tool templ generate -path ./internal/cloud/dashboard` (templ v0.3.1001): templ renumera sus variables (unas 4 500 líneas cambiadas, aceptado en OP-06).
- Los errores internos de estas páginas responden 500 con un texto genérico en español; el detalle va solo al log (`nexoLogInternal`, la misma salida que `nexoInternalError`).

**Archivos nuevos.** `cloudserver/nexo_kb_members.go`, `cloudserver/nexo_kb_proposals.go`, `dashboard/nexo_kb_members.go` y `.templ`, `dashboard/nexo_kb_proposals.templ`, `dashboard/nexo_text_es.go`, sus `*_templ.go` generados y las pruebas `nexo_kb_members_test.go`, `nexo_kb_proposals_test.go`, `nexo_panel_es_test.go`, `dashboard/nexo_text_es_test.go`.

**Archivos Nexo modificados.** `cloudserver/nexo_kb_dashboard.go` (registro de las rutas nuevas, fila de secciones del espacio, `kbRenderComponent` y `kbFail`), `cloudserver/nexo_responses.go` (`nexoLogInternal`), `dashboard/nexo_brand.templ` (pestaña Propuestas), `dashboard/nexo_brand.go` (`nx()` consulta `nexoTextPanel`), `dashboard/nexo_tokens.go` (textos de caducidad, alcance y estado por `nx()`), `dashboard/static/nexo_theme.css` (lista de miembros, comparación, insignia).
### SG-07 · Pantallas del panel para el auditor

**Especificación.** Backlog SG-07 en `docs/nexo/mejoras-y-referencias.md`, diseño en `docs/nexo/panel/design.md` §10, manual `docs/nexo/manuales/administracion.md` §9.5. Reglas de base: RS-30..RS-34, CA-S09 (seguridad S4) y el rol auditor (R1).

**Motivo.** La auditoría, la verificación de la cadena, las alertas y la revisión de accesos solo se podían usar con la API JSON. El auditor necesita hacerlo desde el panel y dejar constancia de la revisión trimestral.

**Qué hace.** Pestaña "Auditoría" (para quien tiene `platform.audit.read`) con seis pantallas server-rendered (templ, sin JavaScript propio):
- **Registro** `/dashboard/admin/audit`: eventos de seguridad (`cloud_auth_audit_log`) o, con `?src=roles`, de roles (`nexo_authz_events`). Filtros de actor, acción, fechas, resultado y ámbito; páginas de 50 por cursor; enlace "Ver historial" cuando el evento trae una clave de elemento.
- **Actividad y presencia** `/dashboard/admin/audit/activity`: quién se conectó (IP y cliente) y qué sincronizó.
- **Historial** `/dashboard/admin/audit/history?key=`: versiones de un elemento (sin su contenido).
- **Integridad** `/dashboard/admin/audit/integrity[?run=1&table=]`: ejecuta `evidence.VerifyAll`; muestra "La cadena está íntegra" o la primera fila alterada, el total y los huecos. Es un GET (como la API), por eso sirve también con un token `read`.
- **Alertas** `/dashboard/admin/audit/alerts` (pendientes o todas; incluye `service_account_stale`) y `POST .../alerts/{id}/ack` (`platform.audit.read` + token write): `evidence.AckAlert`.
- **Revisión de accesos** `/dashboard/admin/audit/access-review` y `POST .../access-review/complete` (`platform.audit.read` + token write): `evidence.BuildAccessReview` y `CompleteReview`; muestra fecha y autor de la última revisión.
- Mismas funciones y mismas acciones de auditoría que la API (`nexo.audit.read`, `nexo.alerts.read`, `nexo.alert.ack`, `nexo.access_review.complete`, ...); si no se puede registrar la lectura, no se entregan datos. Sesión del panel con la identidad del token (un token `read` lee y no cambia nada), CSRF del panel de conocimiento y POST con redirección (303). Los errores internos pasan por `nexoInternalError`. El detalle de un evento omite claves con nombre de secreto y valores con forma de token, hash o cabecera Authorization.

**Archivos nuevos.** `internal/cloud/cloudserver/nexo_audit_dashboard.go` (rutas, acceso, registro, actividad, historial), `nexo_audit_dashboard_evidence.go` (integridad, alertas, revisión), `nexo_audit_dashboard_test.go` (Postgres: acceso del auditor, el admin y el superadmin, 403 para un miembro, filtros y paginación, detalle sin secretos, integridad íntegra y rota por alteración y borrado, reconocer y registrar con CSRF, token read y mismos eventos que la API, sin tokens ni hashes en el HTML), `internal/cloud/dashboard/nexo_audit.go`, `nexo_audit.templ` y `nexo_audit_templ.go` (templ v0.3.1001).

**Archivos Nexo modificados.** `cloudserver/nexo_admin_routes.go` (registro, +1 línea), `nexo_routes_test.go` (prefijo `/dashboard/admin/audit` en `nexoIsNexoPath`), `cloudstore/nexo_queries.go` (filtro `Project` de `NexoAuditQuery`; la API no lo usa), `dashboard/nexo_brand.templ` y `nexo_brand_templ.go` (pestaña "Auditoría", +3 líneas), `dashboard/static/nexo_theme.css` (bloque `nxa-` al final). Ningún archivo de Engram cambia.

### NX-10 · Panel: estado de la búsqueda, selectores y textos de Go en español (feat/panel-mejoras)

**Especificación.** `docs/nexo/panel/design.md` §11 y §12, `docs/nexo/mejoras-y-referencias.md` (OP-06 resto y OP-08) y `docs/nexo/manuales/administracion.md` §5.12.

**Motivo.** Quien administra no podía ver el estado de la búsqueda (índice por significado, cola, cartas muertas) sin la terminal; los selectores del tema parecían campos de texto (sin flecha); y quedaban textos en inglés que salen de código Go y no de plantillas.

**Qué hace.**
- **Pantalla "Estado de la búsqueda"** `GET /dashboard/kb/search-status` (solo lectura, `platform.settings`, la misma capacidad de `GET /nexo/kb/v1/admin/index`): modo por defecto, proveedor, modelo y dimensiones, clave "configurada" o "no configurada" y de qué variable sale (nunca el valor), pgvector y su versión, versiones del índice (activa, en construcción, retiradas) con fechas, avance (secciones con vector sobre el total, pendientes, retraso del feed), cartas muertas con sus últimos mensajes (limpiados otra vez) y el comando `engram nexo kb reindex --vectors --requeue-dead`, últimas corridas de evaluación y pesos y umbrales en uso. Bloque "Reordenamiento (R3)" reservado ("No configurado"). Pestaña nueva en el menú para administradores. Un administrador sin rol de superadmin recibe 403 con el motivo.
- **Servicio**: `kbservice.SearchStatus` (`internal/nexo/kb/service/r2_status.go`) con tres lecturas nuevas en `store` (`VectorVersionHistory`, `CountIndexableSections`, `RecentDeadLetters`) y `embed.KeyStatus` (origen de la clave sin su valor). Sin tablas ni migraciones.
- **Selectores con flecha** en todo el tema (`nexo_theme.css`, bloque final): apariencia propia con flecha SVG, foco visible, alto táctil y 16 px en móvil, contraste alto del sistema. El menú pasa a dos filas desde 721 px (con diez pestañas las últimas quedaban fuera de la vista).
- **Textos de Go en español** (OP-06, resto): "Never" de las fechas, "No content captured.", `EmptyState`, páginas de error de `renderStoreError` y los `http.Error` de `/dashboard/`. Estos últimos sin editar los handlers de Engram: el middleware `nexo_panel_errors.go` retiene el cuerpo de un 4xx o 5xx de texto plano bajo `/dashboard/`, solo con la marca de Nexo, y lo pasa por `dashboard.NexoErrorText`; un 5xx nunca muestra el error de la base (queda en el log). Los mensajes que solo existen en el panel de Nexo se escribieron en español en su código.

**Archivos nuevos.** `internal/nexo/kb/store/status.go`, `embed/status.go` (+ `status_test.go`), `service/r2_status.go` (+ `r2_status_test.go`), `cloudserver/nexo_search_status.go` (+ `_test.go`, Postgres), `cloudserver/nexo_panel_errors.go` (+ `_test.go`), `dashboard/nexo_search_status.go` y `.templ` (+ `_templ.go`), `dashboard/nexo_errors_es.go`, `dashboard/nexo_text_go_es.go`, `dashboard/nexo_text_go_test.go`.

**Archivos Nexo modificados.** `cloudserver/nexo_kb_dashboard.go` (+1 línea de registro; mensajes en español), `nexo_kb_members.go`, `nexo_service_accounts_dashboard.go`, `nexo_security.go` (el middleware en `nexoHandler`; mensaje del token de solo lectura), `nexo_tokens.go` (mensaje), `dashboard/nexo_brand.templ` y `nexo_brand_templ.go` (una pestaña), `dashboard/nexo_brand.go` (`nx()` consulta `nexoTextGo` y los patrones), `dashboard/nexo_text_es_test.go`, `static/nexo_theme.css` (bloque final) y `docs/nexo/**`.

**Archivos de Engram.** Líneas marcadas `// NX-10 (panel, OP-06)` en `helpers.go` y `dashboard.go`, y dos expresiones en `components.templ` (ver §4).

**Verificación.** Pruebas contra Postgres: la pantalla con y sin proveedor, con la clave por variable y por archivo (el buscador de secretos de S5 más la clave de la prueba no encuentra nada), con una configuración inválida, 403 para el administrador y los miembros y sin sesión a login; armado del veredicto, el avance y los parámetros sin base; cada `http.Error` y cada cadena de Go de los archivos de Engram tiene traducción.

## 4. Líneas marcadas en archivos originales de Engram

Verificado con `git grep -n "// NX-"` (fuera de `docs/` e `internal/nexo/`). Las líneas sin marca propia dentro de un bloque se indican como continuación del bloque marcado.

| Archivo:línea | Marca | Propósito |
|---|---|---|
| `internal/cloud/cloudserver/cloudserver.go:19` | NX-07 | Import de `internal/nexo/security` |
| `internal/cloud/cloudserver/cloudserver.go:89` | NX-07 | Campo `nexo nexoState` en `CloudServer`: límites y bloqueo en memoria (uno por servidor) |
| `internal/cloud/cloudserver/cloudserver.go:221` | NX-02 | `s.nexoStartActivityRetention()` en `Start()`: depuración del registro al iniciar y cada 24 h |
| `internal/cloud/cloudserver/cloudserver.go:229` | NX-04 / NX-07 | `Handler()` devuelve `s.nexoHandler(s.mux)`: middleware de IP y User-Agent (NX-04), límite de peticiones por IP y guardia del panel para sesiones con token `read` (NX-07) |
| `internal/cloud/cloudserver/cloudserver.go:303-305` | NX-04 / NX-07 | Campo `NexoValidateLoginTokenRequest` en `dashboard.MountConfig` (bloque de 3 líneas): el login del panel se valida con el contexto de la petición; desde NX-07 llama a `s.nexoValidateDashboardLogin` (bloqueo por fallos) |
| `internal/cloud/cloudserver/cloudserver.go:343` | NX-03 | `s.registerNexoAdminRoutes(s.mux)`: registra `/nexo/admin/*` |
| `internal/cloud/cloudserver/cloudserver.go:404-406` | NX-07 | `authenticateRequest`: `nexoAuthPrecheck` (IP bloqueada: 429 sin resolver el token) |
| `internal/cloud/cloudserver/cloudserver.go:420` | NX-07 | `authenticateRequest`: el retorno pasa por `nexoAfterAuth` (persona bloqueada, límite por token, token `read` solo lee en rutas de Engram: negar por defecto) |
| `internal/cloud/cloudserver/cloudserver.go:455-456` | NX-07 | `requestAuthDenyReason`: `security.ErrTokenExpired` → `token_expired` |
| `internal/cloud/cloudserver/cloudserver.go:468` | NX-07 | `recordRequestAuthDeniedAudit`: `nexoNoteAuthFailure` (contador de fallos por IP o persona) |
| `internal/cloud/cloudserver/cloudserver.go:623` | NX-02 | `handlePullManifest`: actualiza la presencia (sin fila de detalle) |
| `internal/cloud/cloudserver/cloudserver.go:649` | NX-02 | `handlePullChunk`: registra `pull_chunk` |
| `internal/cloud/cloudserver/cloudserver.go:821-823` | NX-01 | `handlePushChunk`: comentario marcado + `nexoActor := nexoResolveActor(...)` + `WriteChunk` recibe el actor por contexto y `nexoActor.Name` como `created_by` (antes `req.CreatedBy`) |
| `internal/cloud/cloudserver/cloudserver.go:831` | NX-02 | `handlePushChunk`: registra `push_chunk` con lo declarado por la PC |
| `internal/cloud/cloudserver/mutations.go:205-207` | NX-01 | `handleMutationPush`: comentario marcado + `nexoActor := nexoResolveActor(...)` + `createdBy := nexoActor.Name` (antes `mutationPushCreatedBy`, que se conserva sin uso para no ampliar el diff con Engram; está en `.deadcode-baseline.txt`) |
| `internal/cloud/cloudserver/mutations.go:248` | NX-01 | `InsertMutationBatch` recibe el actor por contexto |
| `internal/cloud/cloudserver/mutations.go:261` | NX-02 | `handleMutationPush`: registra `push_mutations` |
| `internal/cloud/cloudserver/mutations.go:475` | NX-02 | `handleMutationPull`: registra `pull_mutations` (detalle solo si devuelve cambios) |
| `internal/cloud/cloudserver/dashboard_session.go:124` | NX-04 | `validateDashboardLoginToken` delega en la variante con contexto (`context.Background()`, misma semántica) |
| `internal/cloud/cloudserver/dashboard_session.go:127-129` | NX-04 | Nueva firma `validateDashboardLoginTokenContext(ctx, token)`: el cuerpo es el original de Engram; solo se quitó su `ctx := context.Background()` (ahora es parámetro) |
| `internal/cloud/cloudserver/dashboard_session.go:370-372` | NX-07 | `revalidateDashboardPrincipal`: `nexoRevalidateSessionToken` (la sesión del panel cae si su token se revoca o vence; alcance del token en el contexto) |
| `internal/cloud/cloudstore/cloudstore.go:262-264` | NX-01 | `WriteChunk`: `nexoApplyMutationActor(ctx, tx)` dentro de la transacción (bloque `if err` de 3 líneas) |
| `internal/cloud/cloudstore/cloudstore.go:51` | NX-07 | `New`: `store.nexoMigrateOnStartup(...)` en lugar de `store.migrate(...)` (misma línea): con `NEXO_MIGRATION_DATABASE_URL` migra con la cuenta dueña, aplica privilegios y verifica la cuenta de aplicación; sin ella, `migrate` como antes con aviso |
| `internal/cloud/cloudstore/cloudstore.go:878-880` | NX-01 / NX-02 | `migrate`: `cs.nexoMigrate(ctx)` al final (bloque de 3 líneas) |
| `internal/cloud/cloudstore/cloudstore.go:959-961` | NX-01 | `InsertMutationBatch`: `nexoApplyMutationActor(ctx, tx)` (bloque de 3 líneas) |
| `internal/cloud/cloudstore/identity.go:94-95` | NX-07 | Campos `NexoExpiresAt` y `NexoScope` en `PrincipalToken` |
| `internal/cloud/cloudstore/identity.go:497-499` | NX-07 | `CreatePrincipalTokenWithAudit`: `nexoApplyTokenIssue` (caducidad y alcance en la misma transacción; `expires_at` y `scope` en la auditoría) |
| `internal/cloud/cloudstore/identity.go:739` | NX-07 | `ListPrincipalTokens`: el retorno pasa por `nexoFillTokenFacts` (una consulta) |
| `internal/cloud/cloudstore/identity.go:772-793` | NX-07 | `FindPrincipalTokenByHash`: la consulta trae `nexo_expires_at`, `nexo_scope` y si venció (línea SQL marcada `-- NX-07`), escaneo marcado y `nexoCheckResolvedToken` (token vencido = `security.ErrTokenExpired`; datos del token en el contexto) |
| `internal/cloud/cloudstore/identity.go:991-994` | NX-07 | `insertAuthAuditEvent`: `nexoAuditPrepare` (A2, bloque de 4 líneas): agrupa rechazos de autenticación repetidos (escribe antes el resumen pendiente; si es un repetido, no escribe la fila) y agrega `credential_fp` y, con un token revocado o vencido, `nexo_principal_id` y `credential_id` (`cloudstore/nexo_audit_dedup.go`) |
| `internal/cloud/cloudstore/identity.go:995` | NX-04 | `insertAuthAuditEvent`: `nexoAuditMetadata(ctx, ...)` agrega `client_ip` y `user_agent` a `metadata` (punto común de toda la auditoría de Engram) |
| `internal/cloud/dashboard/dashboard.go:57-58` | NX-04 | Campo opcional `NexoValidateLoginTokenRequest` en `MountConfig` |
| `internal/cloud/dashboard/dashboard.go:314` | NX-04 | `handleLoginSubmit` usa `nexoValidateLoginToken(h.cfg, r, token)` (cae en `ValidateLoginToken` si el campo no está) |
| `cmd/engram/cloud_runtime_e2e_test.go:332-336` | NX-01 | Test de Engram ajustado: `/sync/push` con token asignado guarda el dueño del token, no el `created_by` del cliente (RF-04) |
| `internal/cloud/cloudserver/admin_handlers.go:16` | NX-07 | Import de `internal/nexo/security` |
| `internal/cloud/cloudserver/admin_handlers.go:56-57` | NX-07 | Campos `expires_in_days` y `scope` en el cuerpo de emisión de token |
| `internal/cloud/cloudserver/admin_handlers.go:79-80` | NX-07 | Campos `expires_at` y `scope` en la respuesta de token (`sanitizeToken`, líneas 478-479) |
| `internal/cloud/cloudserver/admin_handlers.go:117-119` | NX-05 | `handleAdminCreateUser`: `nexoAllowCreateUserRole` (crear con rol admin exige `platform.admins.manage`, solo superadmin) |
| `internal/cloud/cloudserver/admin_handlers.go:154-156` | NX-05 | `handleAdminSetUserEnabled`: `nexoAllowSetUserEnabled` (sobre admin, superadmin o auditor exige `platform.admins.manage`; último superadmin, 409) |
| `internal/cloud/cloudserver/admin_handlers.go:212-214` | NX-05 | `handleAdminCreateToken`: `nexoAllowManageTargetTokens` (tokens de otro admin, superadmin o auditor solo por superadmin) |
| `internal/cloud/cloudserver/admin_handlers.go:215-218` | NX-07 | `handleAdminCreateToken`: `nexoTokenIssueContext` valida caducidad y alcance; la línea 239 crea el token con ese contexto |
| `internal/cloud/cloudserver/admin_handlers.go:298-300` | NX-05 | `handleAdminRevokeToken`: `nexoAllowRevokeToken` (misma regla, resolviendo el dueño del token) |
| `internal/cloud/cloudserver/admin_handlers.go:357-359` | NX-07 | `handleAdminCreateGrant`: `nexoAllowProjectGrant` (exige `platform.users.manage`; a uno mismo, 403 `self_grant_forbidden`) |
| `internal/cloud/cloudserver/admin_handlers.go:399-401` | NX-05 | `requireManagedAdmin`: `nexoRequireAdminCapability` exige la capacidad de plataforma de la ruta (admite al auditor en lecturas); sin almacén Nexo sigue la regla de Engram |
| `internal/cloud/cloudserver/dashboard_admin_users.go:148-150` | NX-05 | Panel, crear usuario: igual que la API |
| `internal/cloud/cloudserver/dashboard_admin_users.go:192-194` | NX-05 | Panel, habilitar y deshabilitar: igual que la API |
| `internal/cloud/cloudserver/dashboard_admin_users.go:288-290` | NX-05 | Panel, emitir token: igual que la API |
| `internal/cloud/cloudserver/dashboard_admin_users.go:291-294` | NX-07 | Panel, emitir token: `nexoTokenIssueFromForm` (campos `expires_in_days` y `scope`); la línea 318 crea el token con ese contexto |
| `internal/cloud/cloudserver/dashboard_admin_users.go:386-388` | NX-05 | Panel, revocar token: igual que la API |
| `internal/cloud/cloudserver/dashboard_admin_users.go:427-429` | NX-07 | Panel, permiso de proyecto: `nexoAllowProjectGrant` (igual que la API) |
| `internal/cloud/dashboard/components.templ:1030, 1067, 1072, 1162` | NX-07 | Panel (sin marca: es HTML de templ): campos de caducidad y alcance en el formulario de token, columna "Last Used / Expires / Scope", estado "Expired" (`nexoTokenStatus`) y caducidad y alcance en la página del token creado. Se reutilizan expresiones existentes sin agregar nodos ni líneas; `components_templ.go` regenerado con `go tool templ generate -path ./internal/cloud/dashboard` (templ v0.3.1001 de `go.mod`): 7 líneas cambiadas, mismos finales de línea |
| `internal/cloud/cloudserver/cloudserver.go:344` | NX-10 | `routes()`: `s.registerNexoKBRoutes(s.mux)` registra `/nexo/kb/v1` (`nexo_kb_bridge.go`) |
| `cmd/engram/main.go:39` | NX-10 / NX-07 | Import `nexocli` (`internal/nexo/nexocli`, despachador de `engram nexo`; antes `nexokbcli`) |
| `cmd/engram/main.go:825-826` | NX-10 / NX-07 | `case "nexo"`: `engram nexo kb ...` y `engram nexo audit verify|export|verify-export` (bloque de 2 líneas; misma forma que en NX-10) |
| `internal/mcp/mcp.go:32` | NX-10 | Import `nexokbclient` (`internal/nexo/kb/client`) |
| `internal/mcp/mcp.go:1165` | NX-10 | `registerTools`: `nexokbclient.RegisterTools(srv, s, allowlist)` registra las herramientas `nexo_*` (tanda 2; toda la lógica en `internal/nexo/kb/client/mcp.go`) |
| `internal/setup/setup.go:297-299` | NX-10 | `Install`: `res, err := installFromAdapter(a)` + `nexoAfterInstall(a.slug)` (marcada) + `return res, err` (bloque de 3 líneas; antes `return installFromAdapter(a)`): protocolo Nexo con la PC conectada (`internal/setup/nexo_setup.go`) |
| `internal/setup/setup.go:92-94` | NX-10 | `claudeCodeMarketplace` y `codexMarketplace` = `Adriel2503/engram-nexo`; constante nueva `codexMarketplaceRef = "main"` (rama estable; fue `develop` hasta el 2026-10-13) (D-K2b-35, opción A) |
| `internal/setup/setup.go:1716, 1724` | NX-10 | Codex: `--ref codexMarketplaceRef` en el mensaje de recuperación y en `codex plugin marketplace add` |
| `internal/setup/setup.go:1068` | NX-10 | `installClaudeCode`: `nexoReplaceForeignClaudeMarketplace(claudeBin)` antes de `marketplace add` (catálogo `engram` con otro origen; `internal/setup/nexo_marketplace.go`) |
| `internal/setup/setup.go:1723` | NX-10 | `installCodex`: `nexoReplaceForeignCodexMarketplace(codexBin, path)` antes de `marketplace add` (otro origen u otra rama) |
| `cmd/engram/main.go:3818` + 2 líneas del texto de `printUsage` | NX-10 | `engram help` lista `nexo <subcommand>` (las 2 líneas están dentro de la cadena, sin marca propia; la marca va sobre la función) |
| `plugin/codex_windows_session_end_test.go:113-114` | NX-10 | Prueba de Engram ajustada a la versión del plugin de Codex del fork (`0.1.501`) |
| `.claude-plugin/marketplace.json`, `plugin/claude-code/.claude-plugin/plugin.json`, `plugin/codex/.codex-plugin/plugin.json` | NX-10 (sin marca: JSON) | `version` propia del fork (`0.1.1201` y `0.1.501`, esquema en §3) y descripción que nombra el fork |
| `internal/setup/agents.go:61` | NX-10 | Nota de instalación manual de Codex con el marketplace del fork y `--ref main` |
| `internal/cloud/dashboard/layout.templ:6, 9, 13, 16-18, 21, 24-25, 30-31, 37, 43, 49` | NX-10 (panel) | Comentario marcado en la línea 6; `lang`, `data-theme` y clase del cuerpo por función, textos por `nx()`, hoja `nexo_theme.css` solo con la marca y menú por `nexoNavTabs` (delega en `NavTabs` sin la marca). `layout_templ.go` regenerado (+160/-10 líneas). `components.templ` y `components_templ.go` sin cambios |
| `internal/cloud/dashboard/login.templ:6-67` | NX-10 (panel) | Comentario marcado en la línea 6; con la marca, columna `nexoLoginPanel` y textos por `nx()`; sin la marca, el HTML de Engram; `data-error-code` con el código del error. `login_templ.go` regenerado (+233/-15 líneas) |
| `internal/cloud/dashboard/components.templ:10` y todo el archivo | NX-10 (panel, OP-06) | Comentario marcado en la línea 10. Todas las cadenas fijas visibles (textos de nodos, `placeholder`, argumentos de `EmptyState` y `StatusBadge`, el formato de paginación) pasan por `nx()`: unas 270 líneas con solo el envoltorio `{ nx("...") }`; sin la marca de Nexo `nx()` devuelve el original. Una línea (`:1140`, aviso del administrador) y otra (`:1162`, token creado) se partieron en varios `nx()` y la opción de rol `Admin` usa la clave `Administrator` (para no chocar con la pestaña "Administración"). Estructura, rutas y comportamiento sin cambios. `components_templ.go` regenerado con `go tool templ generate -path ./internal/cloud/dashboard` (templ v0.3.1001; templ renumera sus variables: unas 4 500 líneas cambiadas). Las traducciones están en `nexo_text_es.go` y `TestNexoPanelTextsAreTranslated` falla si una cadena envuelta no tiene traducción |
| `internal/cloud/cloudserver/cloudserver_test.go:735, 815`, `dashboard_admin_users_test.go:216-219, 739, 768, 790, 848, 870-873`, `dashboard_session_test.go:120`, `nexo_security_test.go:609, 614` | NX-10 (panel, OP-06) | Pruebas de Engram ajustadas a los textos en español (el panel de Nexo siempre tiene la marca activa): `ADMIN SURFACE`, `PROJECT DETAIL`, `Create User`, `Create Token`, `Disable User`, `Grant Project`, `Revoke`/`Revoked`, el aviso de administración de usuarios, el de token deshabilitado y `write`/`read` del alcance |
| `internal/cloud/dashboard/dashboard.go:341` | NX-10 (panel) | `handleDashboardHome`: `nexoHome(...)` en lugar de `DashboardHome(...)` (delega en `DashboardHome` sin la marca) |
| `internal/cloud/dashboard/helpers.go:285, 386, 547` | NX-10 (panel, OP-06) | `formatTimestampStr` y `formatTimePtr` devuelven `nx("Never")`; `renderInlineStructuredPreview` devuelve `nx("No content captured.")` (sin la marca de Nexo, el mismo texto de antes) |
| `internal/cloud/dashboard/components.templ:46-47` | NX-10 (panel, OP-06) | `EmptyState`: `{ nx(title) }` y `{ nx(message) }` (antes `{ title }` y `{ message }`); cubre todas las páginas de "no encontrado" y "no disponible" de Engram. `components_templ.go` regenerado con `go tool templ generate -path ./internal/cloud/dashboard`: 4 líneas cambiadas, mismos finales de línea |
| `internal/cloud/dashboard/dashboard.go:1005, 1015` | NX-10 (panel, OP-06) | `renderStoreError`: `nx()` sobre el título y el mensaje de `classifyStoreError` y sobre el rótulo `DEGRADED` y el contexto (la línea 1015 pasa de un literal a `%s` con `nx("DEGRADED")`) |
| `internal/setup/setup_test.go:599, 698, 2088` | NX-10 | Pruebas de Engram ajustadas al marketplace del fork (`codexMarketplaceRef` y lista de plugins de Claude con `Adriel2503/engram-nexo`) |
| `.github/workflows/release.yml` | NX-OP01 | Disparo `v*-nexo.*` + ejecución manual, guarda de repositorio, paso "la etiqueta está en main", `--config .goreleaser.nexo.yaml` y `--snapshot` sin etiqueta, sin `HOMEBREW_TAP_TOKEN` |
| `.github/workflows/cloud-image.yml` | NX-OP01 | Disparo `v*-nexo.*`, guarda de repositorio, imagen `ghcr.io/adriel2503/nexo-engram-cloud` desde `deploy/nexo/Dockerfile`, etiquetas `latest` y versión exacta, sin subir en la ejecución manual |
| `.github/workflows/publish-pi.yml` | NX-OP01 | Guarda: solo en `Gentleman-Programming/engram` |
| `release_config_test.go:11-44` | NX-OP01 | El job esperado de goreleaser es el de Nexo; además verifica que `.goreleaser.nexo.yaml` no ejecute `go mod tidy` ni publique en Homebrew |

Archivo nuevo de configuración: `.goreleaser.nexo.yaml` (OP-01). `.goreleaser.yaml` de Engram queda sin cambios.

Otros archivos originales modificados sin código: `.gitignore` (bloque «Nexo: secretos y archivos locales»).

NX-10 R2 no agrega líneas en archivos de Engram: el indexador arranca desde `nexo_activity.go` (archivo propio, llamado por la línea NX-02 de `Start()`) y el resto vive en `internal/nexo/kb/` y en los archivos `nexo_kb_*` de `cloudserver`.

NX-07 S5 no agrega líneas en archivos de Engram: las rutas Nexo se registran con `nexoRoute` dentro de los `register*` de archivos Nexo (llamados por las líneas NX-03 y NX-10 de `routes()`), y las rutas de Engram quedan en la lista revisada `internal/cloud/cloudserver/testdata/nexo_engram_routes.txt` (al integrar una versión nueva de Engram, `TestNexoRouteRegistryCoversMux` señala cada ruta nueva para revisarla).

Archivos nuevos fuera de `internal/`: `.github/workflows/nexo-security.yml` (NX-07 S5), `plugin/claude-code/skills/nexo-protocol/SKILL.md` y `plugin/codex/skills/nexo-protocol/SKILL.md` (NX-10, copias generadas de `internal/nexo/setup/SKILL.md`), `deploy/nexo/Dockerfile`, `deploy/nexo/Dockerfile.dockerignore`, `deploy/nexo/postgres-init/01-cuentas.sh`, `deploy/nexo/compose.example.yaml`, `deploy/nexo/Caddyfile.example`, `deploy/nexo/.gitattributes` (NX-07), `docs/nexo/**`, este archivo.

SG-10 (cuentas de servicio) no agrega líneas en archivos de Engram: las rutas se registran con `nexoRoute` desde `registerNexoServiceAccountRoutes` (llamada por `registerNexoAuthzRoutes`, archivo propio, desde `registerNexoAdminRoutes`, línea NX-03 de `routes()`), la página del panel usa una plantilla nueva (`nexo_service_accounts.templ`) y la pestaña va en `nexo_brand.templ`; `components.templ` y `components_templ.go` no cambian.

K6 (miembros, propuestas y panel en español) solo toca un archivo original de Engram: `components.templ` (y su `components_templ.go` generado), por OP-06, más las pruebas listadas arriba. Las páginas nuevas son plantillas y archivos Go propios (`nexo_kb_members*`, `nexo_kb_proposals*`), registradas con `nexoRoute` desde `registerNexoKBDashboard`; la pestaña va en `nexo_brand.templ`.

## 5. Variables de entorno nuevas

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_TRUSTED_PROXIES` | vacío (ningún proxy confiable) | IP o CIDR separados por comas o espacios (por ejemplo la IP de Caddy). Solo si la conexión viene de uno de ellos se toma la IP real de `X-Forwarded-For` (recorrida de derecha a izquierda); si no, la cabecera se ignora. Entradas inválidas se ignoran con log |
| `NEXO_ACTIVITY_RETENTION_DAYS` | `180` | Días que se conserva `nexo_activity_log` (depuración al iniciar y cada 24 h). `0` = no depurar; valor inválido = 180; menor que 30 = 30 (NX-07). La presencia se conserva siempre |
| `NEXO_MIGRATION_DATABASE_URL` (NX-07) | vacío (una sola cuenta, aviso de seguridad degradada) | URL de la cuenta dueña del esquema. Con ella el servidor migra, aplica privilegios y crea la función de retención con esa cuenta, y opera con `ENGRAM_DATABASE_URL` (cuenta de aplicación) sin migrar; si la cuenta de aplicación es superusuario, dueña de objetos o puede crear objetos, no arranca |
| `NEXO_DB_APP_ROLE` (NX-07) | usuario de `ENGRAM_DATABASE_URL` | Rol al que se otorgan los privilegios de aplicación (solo si difiere del usuario de la URL, por ejemplo detrás de un pooler) |
| `NEXO_RATE_LIMITS` (NX-07) | vacía: sync 600/600, admin 120/120, login 20/-, nexo 300/300, other 600/- (IP/token por minuto) | JSON por grupo (`{"sync":{"ip":1200}}`, `0` = sin límite) u `off`. JSON inválido: valores por defecto con aviso |
| `NEXO_AUTH_LOCKOUT` (NX-07) | vacía: 20 tokens distintos por IP en 300 s bloquean 900 s; sin bloqueo por persona (A1, S4) | JSON `{"max_failures","user_max_failures","window_seconds","lockout_seconds"}` u `off`. `max_failures` (1 a 10 000) = tokens inválidos **distintos** por IP en la ventana (el mismo token repetido suma una vez); `user_max_failures` (por defecto `0`) = intentos por persona con token revocado o persona deshabilitada (no recomendado) |
| `NEXO_SUPERADMIN_ALLOWED_CIDRS` (NX-07) | vacía (sin restricción, aviso al iniciar) | CIDR o IP separados por comas desde los que se usan `platform.admins.manage` y `platform.settings`; con valor y sin redes válidas, se niega todo |
| `NEXO_TOKEN_MAX_DAYS` (NX-07) | `365` | Caducidad máxima al emitir; por defecto 90 días (o el máximo si es menor) |
| `NEXO_PERF_TEST` | — | Solo tests: `1` habilita la prueba de tiempos con 1 millón de filas |
| `NEXO_KB_MAX_DOC_BYTES` (NX-10) | `2097152` (2 MB) | Tamaño máximo de un documento (entrada del conversor y Markdown resultante) |
| `NEXO_KB_MAX_BATCH` (NX-10) | `200` | Documentos por pedido de publicación |
| `NEXO_KB_MAX_REQUEST_BYTES` (NX-10) | `33554432` (32 MB) | Cuerpo máximo de una petición a `/nexo/kb/v1` (413 si se supera) |
| `NEXO_KB_RANK_CANDIDATES` (NX-10) | `20000` | Tope de secciones candidatas por búsqueda antes de rankear (mínimo 100) |
| `NEXO_KB_DISABLE_UNACCENT` (NX-10) | vacío | `1` al crear la configuración de búsqueda: no usar la extensión `unaccent` (las tildes se pliegan en la aplicación) |
| `NEXO_KB_PERF_SECTIONS` | — | Solo tests: activa la prueba de rendimiento de búsqueda con esa cantidad de secciones |
| `NEXO_KB_UPLOAD_MAX_BYTES` (NX-10 2b) | `268435456` (256 MB) | Cuerpo máximo de una subida multipart (los zip pasan a un archivo temporal; nunca se descomprimen a disco) |
| `NEXO_KB_UPLOAD_MAX_FILES` (NX-10 2b) | `2000` | Archivos indexables por subida |
| `NEXO_KB_UPLOAD_MAX_FILE_BYTES` (NX-10 2b) | `20971520` (20 MB) | Tamaño máximo por archivo (y entrada de los conversores binarios, docx) |
| `NEXO_KB_UPLOAD_MAX_UNZIPPED` (NX-10 2b) | `67108864` (64 MB) | Suma de lo descomprimido indexable de un zip (zip bomb: además, relación máxima 100:1 en entradas de más de 1 MB) |
| `NEXO_KB_UPLOAD_MAX_ENTRIES` (NX-10 2b) | `10000` | Entradas de un zip |
| `NEXO_KB_PRUNE_MAX_PERCENT`, `NEXO_KB_PRUNE_MIN_DOCS` (NX-10 2b, KP-01) | `25`, `5` | La poda no retira más de ese porcentaje de los documentos activos del origen (por encima de esa cantidad mínima) sin `prune.confirm` / `--confirm-prune` |
| `NEXO_KB_R1` (NX-10 2b) | vacía: todas las mejoras de R1 | `off`, `all` o lista (`doc_context,split_tables,synonyms,threshold,diversity,quote`). Cambiar `doc_context` exige reindexar |
| `NEXO_KB_MIN_COVERAGE` (NX-10 2b, RC-07) | `0.3` | Umbral de cobertura ponderada para responder "no existe un estándar oficial" |
| `NEXO_KB_MAX_PER_DOC` (NX-10 2b, RC-09) | `2` | Máximo de secciones de un mismo documento por respuesta |
| `NEXO_KB_PUBLIC_URL` (NX-10 2b, RC-08) | vacía: esquema y host de la petición (detrás de un proxy de `NEXO_TRUSTED_PROXIES`, los de `X-Forwarded-Proto` y `X-Forwarded-Host`) | URL pública de Nexo para los enlaces permanentes de las citas; si está, manda sobre lo anterior |
| `NEXO_KB_EMBEDDER` (NX-10 R2, RC-12) | `off` | Proveedor de embeddings: `gemini`, `openai`, `azure-openai`, `openai-compatible` u `off`. Sin proveedor (o sin pgvector) la búsqueda es solo por texto, como en R1 |
| `NEXO_KB_EMBED_MODEL` (NX-10 R2) | `gemini-embedding-001` (gemini), `text-embedding-3-small` (openai, azure) | Modelo (en Azure, el modelo detrás del despliegue: fija las capacidades). Matriz de modelos en `design.md` §20.2 |
| `NEXO_KB_EMBED_DIMS` (NX-10 R2) | 768 (gemini), nativa del modelo (openai, azure); obligatoria en `openai-compatible` y modelos desconocidos | Dimensión de los vectores (1 a 4000; hasta 2000 se indexa como `vector`, de 2001 a 4000 como `halfvec`). Cambiarla = versión de índice nueva |
| `NEXO_KB_EMBED_URL` (NX-10 R2) | URL del proveedor (gemini, openai); obligatoria en `azure-openai` (`https://<recurso>.openai.azure.com`) y `openai-compatible` | Raíz de la API |
| `NEXO_KB_EMBED_API_KEY`, `NEXO_KB_EMBED_API_KEY_FILE` (NX-10 R2) | vacías; se aceptan también `GEMINI_API_KEY`/`GOOGLE_API_KEY`, `OPENAI_API_KEY`, `AZURE_OPENAI_API_KEY` | Clave del proveedor (o archivo con la clave, para secretos de Docker). Nunca se imprime, ni se guarda en la base, en logs ni en las corridas |
| `NEXO_KB_EMBED_DEPLOYMENT`, `NEXO_KB_EMBED_API_VERSION` (NX-10 R2, Azure) | —, `2024-10-21` | Despliegue de Azure OpenAI y versión de la API |
| `NEXO_KB_EMBED_AUTH` (NX-10 R2) | `api-key` (azure), `bearer` (compatible con clave), `none` (compatible sin clave) | Encabezado de autenticación |
| `NEXO_KB_EMBED_SEND_DIMS` (NX-10 R2) | vacía | `1`: enviar `dimensions` a un modelo que no está en la tabla (por defecto no se envía) |
| `NEXO_KB_EMBED_BATCH`, `NEXO_KB_EMBED_RPM`, `NEXO_KB_EMBED_TPM` (NX-10 R2) | máximo del modelo, sin límite, sin límite | Textos por llamada y límites propios por minuto (llamadas y tokens estimados); sin límite propio un 429 se reintenta con la espera que indica el proveedor |
| `NEXO_KB_EMBED_TIMEOUT_SECONDS`, `NEXO_KB_EMBED_RETRIES`, `NEXO_KB_EMBED_MAX_CHARS` (NX-10 R2) | `30`, `5`, según el modelo | Tiempo por llamada, reintentos ante errores transitorios y recorte de cada texto |
| `NEXO_KB_EMBED_QUERY_PREFIX`, `NEXO_KB_EMBED_DOC_PREFIX` (NX-10 R2) | vacías | Prefijos para modelos que los esperan (multilingual-e5: `query: `, `passage: `), solo `openai-compatible` |
| `NEXO_KB_RRF_K`, `NEXO_KB_RRF_VECTOR_WEIGHT`, `NEXO_KB_RRF_TEXT_WEIGHT` (NX-10 R2, RC-11) | `60`, `0.7`, `0.3` | Fusión RRF del modo `hybrid` (medido en R0, `design.md` §20.6) |
| `NEXO_KB_MIN_SIMILARITY`, `NEXO_KB_MIN_TEXT_SIMILARITY` (NX-10 R2) | ver `design.md` §20.5 | Umbral de "no hay estándar" del modo `hybrid`: un resultado pasa con similitud >= `MIN_SIMILARITY`, o con cobertura >= `NEXO_KB_MIN_COVERAGE` y similitud >= `MIN_TEXT_SIMILARITY`. Depende del modelo: recalibrar al cambiarlo |
| `NEXO_KB_VECTOR_EXACT_MAX`, `NEXO_KB_VECTOR_CANDIDATES`, `NEXO_KB_VECTOR_ITERATIVE_SCAN` (NX-10 R2, RC-19) | `20000`, `200`, `relaxed_order` | Recorrido exacto si los espacios visibles tienen hasta esa cantidad de secciones vigentes; candidatos de la subconsulta HNSW (y `ef_search`); `strict_order`, `relaxed_order` u `off` |
| `NEXO_KB_VECTOR_WORKER`, `NEXO_KB_VECTOR_BATCH`, `NEXO_KB_VECTOR_POLL_SECONDS` (NX-10 R2, RC-20) | activo, `100`, `5` | Indexador en segundo plano (`off` lo apaga en esa réplica), trabajos por pasada y espera entre pasadas |
| `NEXO_KB_EMBED_CACHE`, `NEXO_KB_EVAL_CONFIGS`, `NEXO_KB_EMBED_LIVE` | — | Solo tests: caché de vectores de la medición sobre la wiki, configuraciones del modo `hybrid` a comparar y prueba con la API real de Gemini |
| `NEXO_KB_LOCAL_DB` (NX-10 2b tanda 2, cliente) | `$NEXO_HOME/kb.db` o `~/.nexo/kb.db` | Ruta de la copia local de los espacios (SQLite propio, separado de `engram.db`) |
| `NEXO_TOKEN` (cliente, `engram nexo connect`) | vacía | Token para `connect` sin pasarlo como argumento (alternativa a `--token-stdin`); no se guarda en ningún archivo salvo `cloud.json` |
| `NEXO_HOME` (cliente, kit) | `~/.nexo` | Carpeta de Nexo en la PC (la copia local va en `kb.db` dentro de ella) |
| `NEXO_KB_MCP` (cliente) | vacía | `off`: no registra las herramientas `nexo_*` en `engram mcp` |
| `NEXO_KB_TIMEOUT_SECONDS` (cliente) | `8` | Espera del servidor antes de responder desde la copia local |
| `NEXO_KB_SYNC_MINUTES` (cliente) | `10` | Cada cuántos minutos se actualiza la copia mientras el MCP corre |
| `NEXO_KB_SYNC_OVERLAP_SECONDS` (cliente) | `120` | Ventana de solape de la sincronización (D-K2b-22) |
| `NEXO_KB_MIN_COVERAGE`, `NEXO_KB_MAX_PER_DOC` (cliente) | `0.3`, `2` | También los usa la búsqueda en la copia local |
| `NEXO_KB_WIKI_DIR`, `NEXO_KB_EVAL_SET`, `NEXO_KB_EVAL_OVERLAY`, `NEXO_KB_EVAL_OUT`, `NEXO_KB_EVAL_DUMP`, `NEXO_KB_EVAL_BASELINE` | — | Solo tests: medición R0/R1 sobre un corpus real (`TestEvalWikiCorpus`); el corpus nunca se versiona |
| `NEXO_AUDIT_DEDUP_SECONDS` (NX-07 S4, A2) | `600` | Ventana del resumen de rechazos de autenticación repetidos (misma huella, IP y motivo); `0` = una fila por rechazo |
| `NEXO_AUDIT_VERIFY_MINUTES` (NX-07 S4) | `60` | Verificación periódica de la cadena de hashes (alerta `audit_chain_broken`); `0` = solo a pedido |
| `NEXO_ALERT_RULES` (NX-07 S4) | vacía: reglas por defecto | JSON por regla (`enabled`, `threshold`, `window_seconds`, `cooldown_seconds`, `severity`) u `off` |
| `NEXO_ALERT_WEBHOOK_URL` (NX-07 S4) | vacía (alertas solo en la base y la API) | Webhook de Slack o Teams (JSON `text`, `summary`, `nexo_alert`), 3 intentos |
| `NEXO_AUDIT_EXPORT_DIR` (NX-07 S4) | vacía: exportación desactivada (aviso) | Carpeta de la exportación diaria firmada (`<tabla>/<día>.jsonl` y `.sig`) |
| `NEXO_AUDIT_EXPORT_KEY` (NX-07 S4) | — (sin ella, exportación desactivada) | Clave HMAC-SHA256 de las firmas (32 caracteres o más) |
| `NEXO_DISABLE_INACTIVE_DAYS` (NX-07 S4; solo personas desde SG-10) | vacía (desactivado) | Deshabilita **personas** (`kind = 'human'`) sin uso por esos días (mínimo 7; nunca el último superadmin). Las cuentas de servicio nunca se deshabilitan por inactividad: ver `NEXO_SA_STALE_DAYS` |
| `NEXO_SA_STALE_DAYS` (SG-10) | `180` | Días sin uso tras los cuales una cuenta de servicio habilitada deja la alerta `service_account_stale` (una por período sin uso, con slug y responsable) y la marca "sin uso" en la API, la CLI, el panel y la revisión de accesos; no la deshabilita. `0` = desactivado; un valor inválido usa 180 con aviso en el log |
| `NEXO_RETENTION_AUDIT_DAYS`, `NEXO_RETENTION_AUTHZ_EVENTS_DAYS`, `NEXO_RETENTION_KB_EVENTS_DAYS`, `NEXO_RETENTION_ALERTS_DAYS` (NX-07 S4) | vacías (no se depura) | Retención de `cloud_auth_audit_log`, `nexo_authz_events`, `nexo_kb_events` y `nexo_alerts`; mínimo 365 (menos se eleva con aviso) |
| `NEXO_CHAIN_PERF_EVENTS` | — | Solo tests: RNF-S3, carga y verifica esa cantidad de eventos encadenados (por ejemplo `1000000`) |
| `NEXO_AUTHZ_PERF` | — | Solo tests: `1` activa `TestPGAuthorizationCostWith100kBindings` (prueba de tiempos de NX-05; correr sola) |

SG-10 (cuentas de servicio) agrega solo `NEXO_SA_STALE_DAYS` (aviso de cuentas sin uso); por lo demás usa `NEXO_TOKEN_MAX_DAYS` y, en el CI, `ENGRAM_CLOUD_SERVER` y `ENGRAM_CLOUD_TOKEN` como cualquier cliente.

Las variables de Engram no cambian. Recomendación operativa (RN-A3): `ENGRAM_CLOUD_TOKEN` con un valor aleatorio largo que no se entrega a nadie; todo lo que llegue con él figura como `no-identificado`.

## 6. Migraciones (`cloudstore/nexo_migrations.go`)

Se ejecutan solas al iniciar, al final de `migrate`, en una transacción con `pg_advisory_xact_lock`. Todas idempotentes.

| Objeto | Cambio |
|---|---|
| `cloud_mutations`, `cloud_chunks` | Columnas `actor`, `actor_email`, `actor_principal_id`, `actor_token_id`, `actor_verified BOOLEAN NOT NULL DEFAULT FALSE`, `actor_client_claim`. Filas anteriores: `actor` NULL, no verificado (RF-08) |
| Función + triggers `nexo_stamp_actor` | `BEFORE INSERT` en ambas tablas: copia el actor publicado en la transacción (y el email desde `cloud_human_users`) |
| Vista `nexo_mutation_history` | Historial por cambio con datos de la copia y actuales del usuario y del token |
| `nexo_activity_log` | Detalle de conexiones; triggers `nexo_activity_log_immutable` que rechazan `UPDATE`, `DELETE` y `TRUNCATE` (NX-07: el único `DELETE` admitido es el de la dueña de la tabla dentro de `nexo_purge_activity` y sobre filas con más de 30 días; se eliminó el bypass `nexo.allow_retention`) |
| Función `nexo_purge_activity(integer)` (NX-07) | `SECURITY DEFINER`, propiedad de la cuenta que migra, `search_path` fijo; mínimo 30 días; evento `retention.purged` en `nexo_authz_events`. `EXECUTE` revocado a `PUBLIC` y otorgado a la cuenta de aplicación |
| Privilegios (NX-07, solo con dos cuentas) | `security.ApplyGrants` en cada arranque, fuera de la transacción de migración y con su propio advisory lock (ver `docs/nexo/seguridad/design.md` §2.2.2) |
| `cloud_principal_tokens` (NX-07 S3) | Columnas `nexo_expires_at TIMESTAMPTZ` (NULL = sin caducidad; las filas existentes quedan NULL; valor por defecto para filas nuevas `now() + 90 días`, o el máximo si es menor) y `nexo_scope TEXT NOT NULL DEFAULT 'write'` con CHECK `read`/`write`. Trigger `nexo_token_expiry_guard` (`BEFORE INSERT OR UPDATE OF nexo_expires_at`): solo una cuenta de servicio puede quedar sin caducidad |
| `nexo_actor_presence` | Una fila por persona y token |
| Índices | `cloud_mutations (actor_principal_id, seq)`, `(project, entity_key, seq)`, `(entity_key, seq)`; `nexo_activity_log (occurred_at DESC)`, `(actor_principal_id, occurred_at DESC)`, `(actor_principal_id, id DESC)`, `(actor_token_id, id DESC)`; `nexo_actor_presence (last_seen_at DESC, presence_key DESC)` |
| `nexo_scopes` (NX-05) | Ámbitos: `(scope_type, scope_key)` PK, `visibility`, `default_role`, `status`, autor y fechas; fila `('platform', '*')`. Índice `(scope_type, visibility, status)`. Los proyectos de Engram sin fila son restringidos y activos |
| `nexo_role_bindings` (NX-05) | Un rol por persona y ámbito (PK `principal_id, scope_type, scope_key`), FK a `cloud_principals` (CASCADE) y a `nexo_scopes`. Índice `(scope_type, scope_key, role)`. Triggers `nexo_role_bindings_validate` (combinación rol/nivel y cuentas de servicio, R-10) y `nexo_role_bindings_guard` (último superadmin, R-8) |
| `nexo_authz_events` (NX-05) | Eventos de roles, solo inserción (triggers que rechazan `UPDATE`, `DELETE` y `TRUNCATE`). Índices por ámbito, actor, objetivo y acción (`id DESC`) |
| Trigger `nexo_authz_guard_principal` (NX-05) | En `cloud_principals` (`UPDATE OF enabled, role` y `DELETE`): rechaza deshabilitar, degradar o borrar al último superadmin habilitado por cualquier vía |
| Función `nexo_authz_bootstrap_candidate()` (NX-05) | Admin humano habilitado de menor id cuando no hay superadmin habilitado; la migración (y la primera petición si el admin se crea después) le asigna `superadmin` con evento de actor `sistema` |
| `nexo_kb_spaces` (NX-10) | Espacio (`slug` = `nexo_scopes.scope_key`, FK a `nexo_scopes`), nombre, descripción, categorías, `settings` |
| `nexo_kb_documents` (NX-10) | Documento (`UNIQUE (space_id, doc_key)`), estado, vigencia de la versión actual, reemplazo, origen (índice parcial por origen para la poda) |
| `nexo_kb_versions` (NX-10) | Versiones de solo inserción (triggers `nexo_kb_versions_no_update_delete` y `_no_truncate`; tabla de evidencia de NX-07): contenido, hashes, `source_format`, `source_mime`, `source_sha256`, `source_size_bytes`, `source_blob_ref` (NULL), origen, autoría del token |
| `nexo_kb_sections` (NX-10) | Secciones con `heading_path`, `source_location`, `extraction`, vigencia, `recorded_from/until`, `is_current`, `tsv`. GIN parcial (vigentes) y GIN completo; `(space_id, is_current)`, `(document_id, version_id, ordinal)` |
| `nexo_kb_events` (NX-10) | Eventos de solo inserción (triggers; tabla de evidencia) |
| `nexo_kb_changes` (NX-10) | Feed de cambios por `seq` (lo consume la copia local en 2b) |
| `nexo_kb_proposals` (NX-10) | Tabla de propuestas (la lógica llega en 2b) |
| `nexo_kb_index_versions` (NX-10) | Índices derivados y su configuración (`pgfts` v1, modo de tildes) |
| `nexo_kb_sections.body_sha256` (NX-10 2b, RC-05) | Hash del cuerpo de cada sección (filas de 2a: NULL, se calcula al vuelo); índices `(space_id, recorded_from)`, `(space_id, valid_from)` y `(document_id) WHERE recorded_until IS NULL` |
| `nexo_kb_proposals` (NX-10 2b) | Columnas `valid_from`, `valid_until` (KP-08), `tags`, `reviewed_by_email`, `proposed_by_ip` |
| `nexo_kb_publish_runs` (NX-10 2b, KP-02) | Una fila por lote, subida o aprobación (quién, origen, resumen, rechazados, poda); solo inserción (trigger y tabla de evidencia de NX-07) |
| `nexo_kb_eval_runs` (NX-10 2b, RC-28) | Corridas de evaluación (motor, configuración y su hash, conjunto, métricas totales y por clase, resultado por pregunta, aceptada, commit, autor); solo inserción |
| `nexo_kb_index_versions` (NX-10 2b) | El reindexado registra `pgfts` v2 (`building` -> `active`, la anterior `retired`) con `doc_context` en su configuración |
| `nexo_kb_index_versions.feed_seq` (NX-10 R2) | Último `seq` de `nexo_kb_changes` encolado por cada versión del índice vectorial (`index_kind = 'vector'`, configuración con proveedor, modelo, dimensiones, firma y composición del texto) |
| Extensión `vector` (NX-10 R2) | `CREATE EXTENSION IF NOT EXISTS vector` en `public` dentro de un savepoint: si no está instalada o la cuenta no tiene permiso, la migración sigue y la búsqueda queda solo por texto (aviso en el log) |
| `nexo_kb_section_vectors` (NX-10 R2) | Solo con la extensión. Vector por `(index_version, section_id)`, `space_id`, `text_sha256`, `dims`, `embedding vector` sin dimensión fija; FK a `nexo_kb_sections`; índice `(index_version, text_sha256)`; índices HNSW parciales `idx_nexo_kb_section_vectors_hnsw_<D>` sobre `embedding::vector(D)` (768, 1024, 1536 y la configurada hasta 2000) y `embedding::halfvec(D)` (3072 y la configurada de 2001 a 4000), `WHERE dims = D`, `m = 16`, `ef_construction = 64` |
| `nexo_kb_index_jobs`, `nexo_kb_index_dead` (NX-10 R2, RC-20) | Cola de vectorización (`UNIQUE (index_version, section_id)`, prioridad, `claimed_at`, `fail_count`, `last_error`) y cartas muertas con la carga intacta |
| Configuraciones de búsqueda (NX-10) | `nexo_es` (copia de `spanish`) y, si la extensión `unaccent` existe o se puede crear (en `public`), `nexo_es_unaccent`. Si no se puede crear, la migración sigue sin ella (aviso en el log) |
| `cloud_auth_audit_log`, `nexo_authz_events`, `nexo_kb_events` (NX-07 S4) | Columnas `nexo_chain_seq BIGINT`, `nexo_prev_hash BYTEA`, `nexo_hash BYTEA` (filas previas: NULL); índice único parcial `uq_<tabla>_nexo_chain_seq`; funciones `nexo_chain_canon_<tabla>` y `nexo_chain_link_<tabla>`; trigger `zz_nexo_chain_<tabla>` (`BEFORE INSERT`); función `nexo_chain_ts` |
| `nexo_audit_chain_anchors` (NX-07 S4) | Anclas de la cadena (`genesis` al migrar, `purge` en cada depuración); solo de la dueña (la aplicación lee) |
| `nexo_audit_chain_checkpoints` (NX-07 S4) | Puntos de control de la cadena (posición y hash); solo inserción |
| `nexo_alerts`, `nexo_alert_acks` (NX-07 S4) | Alertas (`rule`, `severity`, `layer`, `subject`, `dedup_key` único, `summary`, `details`) y su reconocimiento (uno por alerta); solo inserción |
| `nexo_audit_exports`, `nexo_access_reviews` (NX-07 S4) | Registro de exportaciones firmadas (`UNIQUE (table_name, day)`) y de revisiones de acceso; solo inserción |
| Guardas (NX-07 S4) | Función `nexo_evidence_guard()` y triggers `nexo_guard_<tabla>` / `nexo_guard_trunc_<tabla>` en `cloud_auth_audit_log` y las tablas anteriores; `nexo_evidence_purge_allowed(tabla, oid, hora)`; los triggers de NX-05 y NX-10 en `nexo_authz_events` y `nexo_kb_events` la usan para el `DELETE` de la depuración |
| Función `nexo_purge_evidence(text, integer)` (NX-07 S4) | `SECURITY DEFINER` de la dueña, `search_path` fijo, mínimos de 365 días; depura el prefijo de la cadena con ancla, eventos `retention.purged`; `EXECUTE` solo para la cuenta de aplicación |
| `nexo_service_accounts` (SG-10, desde `authz.Migrate`) | Tabla lateral de las cuentas de servicio: `principal_id` (PK, FK `cloud_principals` ON DELETE CASCADE), `slug` único (`^[a-z0-9][a-z0-9-]{1,62}$`), `description` (hasta 500), `owner_principal_id` (FK, obligatorio), `created_by_principal_id`, `created_at`, `updated_at`; índice por responsable; función y trigger `nexo_service_accounts_validate` (la fila es de una cuenta de servicio y el responsable es una persona). Sin cambios en `cloud_principals` ni en `cloud_principal_tokens` |
| Función `nexo_principal_last_use(bigint)` (SG-10, desde `authz.Migrate`) | `CREATE OR REPLACE`, PL/pgSQL `STABLE`, sin `SECURITY DEFINER`: último uso de un principal (máximo `last_used_at` de sus tokens y última acción en `nexo_actor_presence`, `nexo_kb_events` si existe y `nexo_authz_events`; NULL = nunca usado). La usan el listado de cuentas de servicio, la revisión de accesos y la alerta `service_account_stale`. Sin tablas ni columnas nuevas |

**Volver a la imagen oficial de Engram.** Con la base del fork sobre Engram 3.3.2 (NX-01 a NX-05), volver a la imagen oficial 3.3.2 era posible. Con la base actual (Engram 3.4.0, que puede traer migraciones propias que 3.3.2 no conoce, más las guardas de evidencia de NX-07 y las tablas de NX-10) **no está verificado**: volver a 3.3.2 no es una opción soportada, y volver a la imagen oficial 3.4.0 queda pendiente de probar en una copia de la base. Lo que sigue describe el comportamiento con 3.3.2: Engram ignora las columnas, tablas y la vista nuevas, y el trigger `nexo_stamp_actor` no modifica las filas cuando nadie publicó el actor en la transacción (quedan con `actor` NULL, como los datos anteriores al fork). Con NX-05, el trigger `nexo_authz_guard_principal` sigue activo en la base aunque se use la imagen oficial: impide deshabilitar al último superadmin habilitado (se puede quitar con `DROP TRIGGER nexo_authz_guard_principal ON cloud_principals` si se abandona el fork).

## 7. Compatibilidad con el cliente oficial

**Integración de Engram v3.4.0 (2026-10-13).** `main` del fork (entonces la copia de Engram; hoy esa copia es la rama `engram-upstream`) se adelantó a `upstream/main` (`fc43849`, 30 commits: consolidación de memorias, revisiones programadas, prompts fuera de los chunks de Git salvo que se compartan, robustez del SQLite local, mejoras de plugins y hooks, CI de Engram) y se mezcló en `develop` (`1c3aadb`) sin conflictos; los enganches `NX-` quedaron intactos. Verificado: build, `go vet`, pruebas de `internal/nexo`, `internal/cloud`, `internal/setup` y `TestCloudRuntime`. En Windows fallan pruebas propias de Engram (`internal/store`: enlaces simbólicos sin privilegio y limpieza de carpetas temporales) que no dependen de Nexo. Las pruebas E2E con el cliente oficial descritas abajo se hicieron con 3.3.2; repetirlas con 3.4.0 queda pendiente. Procedimiento para las próximas versiones: adelantar `engram-upstream` a `upstream/main`, probar la mezcla en una carpeta aparte, llevarla a `develop` y, verificada, a `main`.

- Las rutas y respuestas de `/sync/*` no cambian; el autor no viaja a los clientes (`ListMutationsSince` sin cambios).
- Verificado de punta a punta con el **binario oficial Engram 3.3.2** (linux/amd64) contra la imagen `nexo-engram-cloud:3.3.2-nexo.1`, por las dos vías: paquetes (`engram sync --cloud`, `--import`) y autosync de mutaciones (`engram serve` con `ENGRAM_CLOUD_AUTOSYNC=1`), detrás de Caddy HTTPS. Resultados en `06-pruebas/trazabilidad/RESULTADOS.md` del repositorio de Nexo.
- Comportamiento del cliente a tener en cuenta (`design.md` §10.20): con token exige HTTPS en ambas vías; el autosync no envía `created_by` (lo declarado por la PC solo llega por paquetes); `engram sync --cloud` puede volver a subir observaciones importadas, que quedan atribuidas a quien las subió; un proyecto inscrito sin permiso traba el autosync de esa PC.

## 8. Construcción y despliegue

```powershell
# Desde la raíz del fork
docker build --platform linux/amd64 -f deploy/nexo/Dockerfile --build-arg VERSION=3.4.0-nexo.1 -t nexo-engram-cloud:3.4.0-nexo.1 .
docker run --rm nexo-engram-cloud:3.4.0-nexo.1 version   # engram 3.4.0-nexo.1
```

Misma plataforma, usuario, puerto (18080), `ENTRYPOINT` y `CMD` que la imagen oficial; se reemplaza la imagen del servicio `cloud` y se agregan `NEXO_TRUSTED_PROXIES` (IP del proxy) y, si se desea, `NEXO_ACTIVITY_RETENTION_DAYS`. Las migraciones se aplican solas.

## 9. Verificación

```powershell
$env:CLOUDSTORE_TEST_DSN = '<DSN de un Postgres de pruebas>'   # p. ej. un postgres:16-alpine local desechable
go build ./... ; go vet ./internal/cloud/... ./internal/nexo/...
go test -count=1 ./internal/cloud/... ./internal/nexo/...
go test -count=1 -run TestCloudRuntime ./cmd/engram/
# NX-07 S5: la puerta de seguridad de CI (.github/workflows/nexo-security.yml), en local
go test -count=1 -p 1 ./internal/nexo/authz/... ./internal/nexo/security/... ./internal/nexo/routes/...
go test -count=1 -p 1 -run TestNexo ./internal/cloud/cloudserver/ ./internal/cloud/cloudstore/
$env:NEXO_KB_PERF_SECTIONS = "200000"; go test -count=1 -v -timeout 40m -run TestPerfSearch ./internal/nexo/kb/service/   # RNF-K1 (NX-10)
go test -run '^$' -bench NexoOverhead -benchtime 200x ./internal/cloud/cloudserver/   # RNF-03
git grep -n "// NX-" -- ':!docs' ':!internal/nexo'
# NX-07: concurrencia del limitador y del agrupador A2 (requiere cgo; en Windows sin gcc, en un contenedor golang:1.25)
go test -race -count=1 ./internal/nexo/security/
# NX-07 S4: RNF-S3 (1 millón de eventos encadenados) y prueba de tiempos de NX-05, cada una sola
$env:NEXO_CHAIN_PERF_EVENTS = "1000000"; go test -count=1 -v -timeout 20m -run TestChainVerifyPerf ./internal/nexo/security/evidence/
$env:NEXO_AUTHZ_PERF = "1"; go test -count=1 -v -run TestPGAuthorizationCostWith100kBindings ./internal/nexo/authz/
# NX-10 2b: medición R0/R1 sobre la wiki real (fuera del fork; el corpus no se versiona)
$env:NEXO_KB_WIKI_DIR = '<carpeta de la wiki>'; $env:NEXO_KB_EVAL_SET = '<preguntas-wiki.yaml>'; $env:NEXO_KB_EVAL_OVERLAY = '<eval/overlay>'
go test -count=1 -v -run TestEvalWikiCorpus ./internal/nexo/kb/service/   # NEXO_KB_EVAL_BASELINE=1 y NEXO_KB_R1=off: línea base
# NX-10 R2: las pruebas de pgvector necesitan un CLOUDSTORE_TEST_DSN con la extensión (imagen pgvector/pgvector:pg16); sin ella se saltan
# Prueba de humo con la API real del proveedor (Gemini por defecto; OpenAI o Azure con sus variables NEXO_KB_EMBED_*):
NEXO_KB_EMBED_LIVE=1 go test -count=1 -v -run TestEmbedderLive ./internal/nexo/kb/embed/
# Medición R2 sobre la wiki: además NEXO_KB_EMBEDDER, la clave, NEXO_KB_EMBED_CACHE (vectoriza una sola vez), NEXO_KB_EVAL_CONFIGS y NEXO_KB_EVAL_DEBUG=1 (design.md §20.6)
# En Windows, si Defender bloquea un binario de prueba ("Acceso denegado"): go test -c -o <carpeta>\x.exe <paquete> y ejecutarlo desde la carpeta del paquete
# Panel: regenerar las plantillas siempre con la versión fijada en go.mod (templ v0.3.1001)
go tool templ generate -path ./internal/cloud/dashboard
```

E2E de seguridad (repositorio de Nexo): `pwsh -NoProfile -File 06-pruebas/seguridad/Run-Seguridad.ps1` con la imagen `nexo-engram-cloud:3.3.2-nexo.2-dev` construida desde la rama. Para su prueba de volumen CA-35 (1 000 consultas de administración en paralelo) sube el límite del grupo `admin` con `NEXO_RATE_LIMITS={"admin":{"ip":3000,"token":3000}}`; el resto de los grupos queda con los valores por defecto. Desde el parche del 2026-10-12 incluye D-S2-8 (servicio `pc-malpegado`, que comparte la red de `pc-ana`), D-S3-8 y D-S3-9: 44 casos. Desde S4 (servicio `webhook` de prueba, exportación en un tmpfs `/export`, `NEXO_AUDIT_DEDUP_SECONDS=60` y `NEXO_AUDIT_VERIFY_MINUTES=1` para no esperar 10 y 60 minutos) agrega S4-PRIV, A1, A2 y CA-S09..CA-S12: 51 casos.
