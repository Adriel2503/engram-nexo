# Tareas — Trazabilidad en Engram Cloud (NX-01, NX-02, NX-04, NX-03)

**Referencias:** `requirements.md`, `design.md` · **Rama:** `feat/trazabilidad` desde `develop`
**Reglas:** commits solo con la identidad del autor (sin coautores), nada se sube a GitHub sin revisión, cada tarea con sus tests.

## Fase 0 — Preparación

| ID | Tarea | Resultado | Depende de |
|---|---|---|---|
| T-01 | Crear la rama `feat/trazabilidad` desde `develop` | Rama local | — |
| T-02 | Levantar un Postgres de prueba en Docker y definir `CLOUDSTORE_TEST_DSN`; correr la suite `internal/cloud/...` sin cambios para tener la **línea base** de tests | Lista de tests que ya fallaban antes (si hay) | T-01 |
| T-03 | Verificar el formato real de `Principal.ID` y `Principal.TokenID` de un usuario administrado (instancia `panel-admin`) | Confirmar los `JOIN` de la vista de historial | — |

## Fase 1 — NX-01: autoría por cambio

| ID | Tarea | Archivos | Cubre | Depende de |
|---|---|---|---|---|
| T-04 | Migraciones Nexo: columnas `actor_*` (incluida la copia `actor_email`) en `cloud_mutations` y `cloud_chunks`, índices, vista `nexo_mutation_history`; hook de una línea en `migrate` | `cloudstore/nexo_migrations.go`, `cloudstore.go` (NX-01) | RF-03b, RF-08, RNF-02 | T-02, T-03 |
| T-05 | Tipo `MutationActor` y transporte por contexto (`WithMutationActor` / `MutationActorFromContext`) | `cloudstore/nexo_actor.go` | — | T-04 |
| T-06 | Escribir el actor en `InsertMutationBatch`, `insertMaterializedMutations` y `WriteChunk` (y en `cloud_chunks`) | `cloudstore.go` (NX-01) | RF-01, RF-05, RF-06 | T-05 |
| T-07 | `nexoResolveActor` (token asignado / token compartido / sin autenticación) + tests unitarios | `cloudserver/nexo_actor.go`, `_test.go` | RF-02, RF-03, RF-04 | T-05 |
| T-08 | Enganchar el resolvedor en `handlePushChunk` y `handleMutationPush` | `cloudserver.go`, `mutations.go` (NX-01) | RF-02, RF-05 | T-06, T-07 |
| T-09 | Tests de integración NX-01: ambos caminos, borrado, identificado/no identificado, dos tokens, historial, filas antiguas, copia del email | `cloudstore/nexo_actor_test.go`, `cloudserver/nexo_actor_handler_test.go` | CA-01 a CA-08 | T-08 |

## Fase 2 — NX-02: registro de conexiones

| ID | Tarea | Archivos | Cubre | Depende de |
|---|---|---|---|---|
| T-10 | Migraciones: `nexo_activity_log`, `nexo_actor_presence` (con `client_claim`), índices y trigger de inmutabilidad | `cloudstore/nexo_migrations.go` | RF-13 | T-04 |
| T-11 | `RecordActivity` (insert + upsert en una transacción) y `PurgeActivityOlderThan` | `cloudstore/nexo_activity.go` | RF-10 a RF-12, RF-15 | T-10 |
| T-12 | **Middleware de contexto de petición** (base de NX-04): IP con `NEXO_TRUSTED_PROXIES` y User-Agent; enganche de una línea sobre el mux | `cloudserver/nexo_clientctx.go` (NX-04) | RF-20, RF-21 | T-01 |
| T-13 | `nexoRecordActivity`: actor, contexto de petición, valor declarado por la PC; tolerante a fallos; enganche en los 5 handlers | `cloudserver/nexo_activity.go` + una línea por handler (NX-02) | RF-10, RF-11, RF-14, RF-16, RF-22 | T-08, T-11, T-12 |
| T-14 | Depuración periódica (`NEXO_ACTIVITY_RETENTION_DAYS`, al iniciar y cada 24 h) | `cloudserver/nexo_activity.go` (NX-02) | RF-15 | T-11 |
| T-15 | Tests NX-02: registro por operación, descargas vacías solo en presencia, rechazos no registrados, falla del registro sin romper la subida, trigger, depuración | `*_test.go` | CA-10 a CA-16 | T-13, T-14 |

## Fase 3 — NX-04: metadatos de la conexión

| ID | Tarea | Archivos | Cubre | Depende de |
|---|---|---|---|---|
| T-16 | Hook en `recordAuthAuditBestEffort`: agregar `client_ip` y `user_agent` a `metadata` de `cloud_auth_audit_log` | `cloudserver.go` (NX-04) | RF-24 | T-12 |
| T-17 | Tests NX-04: IP con y sin proxy confiable, cabecera falsa ignorada, valor declarado por la PC en presencia, login rechazado con IP | `cloudserver/nexo_clientctx_test.go` | CA-20 a CA-24 | T-13, T-16 |
| T-18 | Documentar la convención de nombres de token (`laptop-ana`, `ci-pipeline`) en el manual de administración | Docs de Nexo | RF-23, RN-A5 | — |

## Fase 4 — NX-03: auditoría consultable

| ID | Tarea | Archivos | Cubre | Depende de |
|---|---|---|---|---|
| T-19 | Consultas: `ListAuthAudit`, `ListActivity`, `ListPresence`, `MutationHistory`, con filtros y paginación por cursor | `cloudstore/nexo_queries.go` | RF-31 a RF-35 | T-09, T-15, T-16 |
| T-20 | Rutas `GET /nexo/admin/{audit,activity,presence,history/{entityKey}}` con `withAuth` + `requireManagedAdmin`; registro con una línea | `cloudserver/nexo_admin_routes.go`, `cloudserver.go` (NX-03) | RF-30, RF-34 | T-19 |
| T-21 | Auditar cada consulta (`nexo.*.read`) | `cloudserver/nexo_admin_routes.go` | RF-36 | T-20 |
| T-22 | Tests NX-03: 403 para `member` y token compartido, filtros, cursor, límite, `include_payload`, auditoría de la consulta; prueba de tiempos con datos sintéticos | `*_test.go` | CA-30 a CA-36, RNF-05 | T-21 |

## Fase 5 — Verificación y entrega

| ID | Tarea | Resultado | Depende de |
|---|---|---|---|
| T-23 | Suite completa `go test ./internal/cloud/...` + `go vet`; comparar con la línea base; medir el sobrecosto por petición (RNF-03) | Sin fallos nuevos; latencia medida | T-22 |
| T-24 | Construir la imagen `nexo-engram-cloud:3.3.2-nexo.1` desde la rama | Imagen local | T-23 |
| T-25 | Prueba E2E en `panel-admin`: Ana y Luis con tokens asignados y acceso a `nexo-transversal`; sincronizar con el **cliente oficial 3.3.2**; verificar CA-01 a CA-36 (base de datos y API); probar también el token compartido | Script y resultados en `06-pruebas/` | T-24 |
| T-26 | Recrear `NEXO_CHANGES.md` con NX-01 a NX-04 (motivo, archivos, puntos de enganche) | Registro del fork | T-25 |
| T-27 | Revisión del usuario; commit y PR hacia `develop` | PR | T-26 |

## Fuera de esta entrega

- Pantallas del panel para historial, actividad, presencia y auditoría (consumirán la API de NX-03).
- Datos que solo la PC puede enviar (correo de git, versión del cliente): requieren modificar el cliente.
- Roles por proyecto y validación en el servidor (cambios 5 y 6), junto con dos ideas tomadas de Utopia:
  - **caducidad de tokens** (por ejemplo, 90 días por defecto);
  - **scope por token** (solo lectura o escritura; permiso efectivo = rol del usuario ∩ scope del token).

## Estimación orientativa

| Fase | Esfuerzo |
|---|---|
| 0 · Preparación | 0,5 día |
| 1 · NX-01 Autoría | 1,5 días |
| 2 · NX-02 Conexiones | 1,5 días |
| 3 · NX-04 Metadatos | 0,5 día |
| 4 · NX-03 Auditoría consultable | 1,5 días |
| 5 · Verificación y entrega | 1 día |
| **Total** | **unos 6,5 días** |
