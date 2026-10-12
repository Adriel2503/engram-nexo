# Diseño técnico — Trazabilidad en Engram Cloud (NX-01, NX-02, NX-04, NX-03)

**Base:** Engram v3.3.2 · **Componente:** `engram cloud serve` + Postgres · **Requerimientos:** `requirements.md`
**Orden de construcción:** NX-01 → NX-02 → NX-04 → NX-03
**Estado:** borrador para aprobación · **Fecha:** 2026-10-12

---

## 1. Principios

1. **Solo servidor.** No cambia el SQLite local ni el cliente; el cliente oficial 3.3.2 sigue funcionando (RNF-01).
2. **Cambios aislados.** La lógica nueva va en archivos `nexo_*.go`. En el código de Engram solo se agregan llamadas, marcadas con `// NX-01` a `// NX-04` (RNF-04).
3. **Migraciones aditivas e idempotentes.** Solo `ADD COLUMN IF NOT EXISTS` y `CREATE ... IF NOT EXISTS`. Nada se renombra ni se borra (RNF-02).
4. **El registro nunca rompe la sincronización.** Los errores al registrar actividad se loguean y la petición continúa (RF-16).

## 2. Contexto del código (v3.3.2)

```
POST /sync/push            ─┐                                    ┌─ WriteChunk ── cloud_chunks
POST /sync/mutations/push  ─┤  withAuth → authenticateRequest ──►├─ InsertMutationBatch ── cloud_mutations
GET  /sync/pull[/chunkID]  ─┤  (cloudserver.go:376-410)         ├─ manifest / chunk
GET  /sync/mutations/pull  ─┘  deja Principal en el contexto     └─ ListMutationsSince
```

| Pieza | Ubicación | Qué aporta |
|---|---|---|
| `authenticateRequest` | `cloudserver/cloudserver.go:396-410` | Resuelve el token y guarda `cloudauth.Principal` en el contexto (`WithPrincipal`) |
| `cloudauth.Principal` | `cloud/auth/foundation.go:68-76` | `ID`, `Kind` (`human`, `service_account`, `legacy`), `DisplayName`, `Source`, **`TokenID`** |
| Subida de paquetes | `cloudserver/cloudserver.go:806` → `cloudstore.WriteChunk` (`cloudstore.go:206-290`) | Hoy usa `req.CreatedBy` **declarado por el cliente** |
| Subida de cambios | `cloudserver/mutations.go:205` → `InsertMutationBatch` (`cloudstore.go:931`) | Calcula `mutationPushCreatedBy` pero **no lo guarda** en `cloud_mutations` |
| Inserción de cambios de un paquete | `cloudstore.insertMaterializedMutations` (`cloudstore.go:499-515`) | Inserta en `cloud_mutations` sin autor |
| Migraciones | `cloudstore.migrate` (`cloudstore.go:638-876`) | Lista de sentencias ejecutadas al iniciar |
| Autor del cliente | `internal/sync/sync.go:2808-2820` (`GetUsername`) | `USER`, luego `USERNAME`, luego hostname. Solo informativo |

## 3. NX-01 — Autoría por cambio

### 3.1 Modelo de datos

**`cloud_mutations`** (columnas nuevas):

La **fuente de verdad** es el token: el servidor busca en Postgres a quién pertenece (`cloud_principal_tokens` → `cloud_principals` / `cloud_human_users`; ver `auth/foundation.go:255-298`). En cada cambio se guardan las **referencias** (IDs) y una **copia** del nombre y del email en ese momento (patrón `actor_label` de Utopia).

| Columna | Tipo | Contenido | Tipo de dato |
|---|---|---|---|
| `actor_token_id` | `TEXT NULL` | `Principal.TokenID` (id de `cloud_principal_tokens`) | Referencia (verdad) |
| `actor_principal_id` | `TEXT NULL` | `Principal.ID` (id de `cloud_principals`) | Referencia (verdad) |
| `actor` | `TEXT NULL` | Copia del nombre mostrado (`DisplayName`); `no-identificado` sin token asignado | Copia histórica |
| `actor_email` | `TEXT NULL` | Copia del email de `cloud_human_users` en el momento del cambio | Copia histórica |
| `actor_verified` | `BOOLEAN NOT NULL DEFAULT FALSE` | `TRUE` solo si la petición usó un token asignado | — |
| `actor_client_claim` | `TEXT NULL` | Lo que declaró la PC (`created_by` del cliente), solo referencia | Informativo |

**`cloud_chunks`:** las mismas seis columnas. `created_by` se mantiene (lo usa el panel de Engram para «Contributors») y pasa a contener el **actor resuelto**.

**Obtención del email sin consultas extra:** `Principal` no trae el email. Se resuelve dentro del mismo `INSERT` con una subconsulta: `(SELECT email FROM cloud_human_users WHERE principal_id::text = $n)`. Para cuentas de servicio (`service_account`) queda `NULL`.

```sql
-- nexo_migrations (se ejecutan al final de migrate)
ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor TEXT;
ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_email TEXT;
ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_principal_id TEXT;
ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_token_id TEXT;
ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_verified BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE cloud_mutations ADD COLUMN IF NOT EXISTS actor_client_claim TEXT;
CREATE INDEX IF NOT EXISTS idx_cloud_mutations_actor_principal ON cloud_mutations (actor_principal_id, seq);
CREATE INDEX IF NOT EXISTS idx_cloud_mutations_entity_key ON cloud_mutations (project, entity_key, seq);
-- (mismas columnas en cloud_chunks)
```

Las filas existentes quedan con `actor` en `NULL` y `actor_verified = FALSE` (RF-08).

### 3.2 Regla de resolución del autor

Tipo nuevo en `cloudstore` (sin dependencias de `cloudserver`):

```go
// Nexo IA · NX-01
type MutationActor struct {
    Name        string // actor (copia del nombre)
    // El email no viaja aquí: se copia en el INSERT con una subconsulta a cloud_human_users.
    PrincipalID string // actor_principal_id
    TokenID     string // actor_token_id
    Verified    bool   // actor_verified
    ClientClaim string // actor_client_claim
}
```

Función en `cloudserver/nexo_actor.go`:

```go
func nexoResolveActor(ctx context.Context, clientClaim string) cloudstore.MutationActor
```

La identidad sale **solo del token asignado** (RF-02, RN-A1):

| Situación | `Name` | `PrincipalID` / `TokenID` | `Verified` |
|---|---|---|---|
| Principal `human` o `service_account` (token asignado) | `DisplayName` (si vacío, `ID`) | `ID` / `TokenID` | `TRUE` |
| Principal `legacy` (token compartido) | `no-identificado` | vacíos | `FALSE` |
| Sin principal (modo sin autenticación) | `no-identificado` | vacíos | `FALSE` |

`ClientClaim` guarda lo que declaró la PC (recortado a 128 caracteres) **solo como referencia**; nunca se usa como `Name`. En los cambios no identificados, `cloud_chunks.created_by` también queda como `no-identificado`.

**Token compartido en el servidor del equipo (RN-A3).** Engram exige definir `ENGRAM_CLOUD_TOKEN` para arrancar. En producción se le asigna un valor aleatorio largo que **no se entrega a nadie**, de modo que en la práctica todas las conexiones usan tokens asignados. Opcional, en una iteración posterior: variable `NEXO_REJECT_LEGACY_SYNC=1` para rechazar directamente la sincronización con el token compartido.

### 3.3 Flujo

**Subida de paquetes** (`/sync/push`):

```
handlePushChunk
  └─ actor := nexoResolveActor(ctx, req.CreatedBy)            // NX-01
  └─ store.WriteChunk(ctx, project, id, actor.Name, ...)       // created_by = actor resuelto
       └─ nexo: guardar actor en cloud_chunks                  // NX-01
       └─ mutations[i].Actor = actor                           // NX-01
       └─ insertMaterializedMutations(... con columnas actor_*)
```

Para no cambiar la firma de `WriteChunk`, que usan interfaces y fakes de tests, el actor completo viaja en el **contexto**:

```go
ctx = cloudstore.WithMutationActor(ctx, actor)   // nexo_actor.go (cloudstore)
// dentro de WriteChunk / InsertMutationBatch:
actor, _ := cloudstore.MutationActorFromContext(ctx)
```

**Subida de cambios** (`/sync/mutations/push`): igual. `mutations.go:205` pasa a `actor := nexoResolveActor(ctx, req.CreatedBy)`, se agrega al contexto, e `InsertMutationBatch` lo escribe en cada fila y en el paquete materializado.

**Lectura:** `ListMutationsSince` **no cambia** su respuesta en esta iteración. El autor no viaja a los clientes (fuera de alcance; evita cualquier riesgo con el cliente oficial).

### 3.4 Consulta del historial (RF-07)

Vista de solo lectura, creada en las migraciones de Nexo:

```sql
CREATE OR REPLACE VIEW nexo_mutation_history AS
SELECT m.seq, m.project, m.entity, m.entity_key, m.op, m.occurred_at,
       m.actor, m.actor_email,              -- copia al momento del cambio
       m.actor_verified, m.actor_principal_id,
       u.username, u.email AS current_email, -- datos actuales del dueño del token
       t.token_prefix, t.name AS token_name,
       m.actor_client_claim,
       m.payload->>'title' AS title
FROM cloud_mutations m
LEFT JOIN cloud_human_users u ON u.principal_id::text = m.actor_principal_id
LEFT JOIN cloud_principal_tokens t ON t.id::text = m.actor_token_id;
```

Ejemplo: `SELECT * FROM nexo_mutation_history WHERE entity_key = 'obs-…' ORDER BY seq;`

> A verificar en implementación: el formato de `Principal.ID` y `TokenID` para usuarios administrados (se espera el id numérico de `cloud_principals` y `cloud_principal_tokens` como texto). Si difiere, se ajustan los `JOIN`.

## 4. NX-02 — Registro de conexiones

### 4.1 Modelo de datos

**`nexo_activity_log`**: detalle, solo inserción.

```sql
CREATE TABLE IF NOT EXISTS nexo_activity_log (
    id                 BIGSERIAL PRIMARY KEY,
    occurred_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor              TEXT NOT NULL,
    actor_email        TEXT,
    actor_principal_id TEXT,
    actor_token_id     TEXT,
    actor_verified     BOOLEAN NOT NULL DEFAULT FALSE,
    operation          TEXT NOT NULL,   -- push_chunk | push_mutations | pull_chunk | pull_mutations
    project            TEXT,
    item_count         INTEGER NOT NULL DEFAULT 0,
    client_ip          TEXT,
    user_agent         TEXT,
    client_claim       TEXT              -- lo que declaró la PC (NX-04), solo referencia
);
CREATE INDEX IF NOT EXISTS idx_nexo_activity_occurred ON nexo_activity_log (occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_nexo_activity_principal ON nexo_activity_log (actor_principal_id, occurred_at DESC);
```

**`nexo_actor_presence`**: una fila por persona y token, que se actualiza (RF-12).

```sql
CREATE TABLE IF NOT EXISTS nexo_actor_presence (
    presence_key   TEXT PRIMARY KEY,      -- principal_id|token_id; 'no-identificado' para conexiones sin token asignado
    actor          TEXT NOT NULL,
    actor_email    TEXT,
    actor_principal_id TEXT,
    actor_token_id TEXT,
    actor_verified BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_ip        TEXT,
    last_user_agent TEXT,
    last_client_claim TEXT,               -- NX-04
    last_project   TEXT,
    push_count     BIGINT NOT NULL DEFAULT 0,
    pull_count     BIGINT NOT NULL DEFAULT 0
);
```

**Inmutabilidad (RF-13):** trigger que rechaza `UPDATE` y `DELETE` en `nexo_activity_log`, con la misma técnica que `audit_events` de Utopia. La depuración por antigüedad (RF-15) desactiva el trigger dentro de una transacción con `SET LOCAL nexo.allow_retention = 'on'`, y el trigger lo permite solo en ese caso.

### 4.2 Qué se registra

| Operación | Handler | `item_count` | ¿Fila en `nexo_activity_log`? | ¿Actualiza presencia? |
|---|---|---|---|---|
| `push_chunk` | `handlePushChunk`, tras `WriteChunk` correcto | observaciones + sesiones + prompts del paquete | Siempre | Sí (`push_count + 1`) |
| `push_mutations` | `handleMutationPush`, tras `InsertMutationBatch` correcto | cambios aceptados | Siempre | Sí |
| `pull_chunk` | `handlePullChunk` (`GET /sync/pull/{id}`) correcto | conteos del paquete | Siempre (entrega datos) | Sí (`pull_count + 1`) |
| `pull_mutations` | `handleMutationPull` correcto | cambios devueltos | **Solo si `item_count > 0`** | Siempre |
| `pull_manifest` | `handlePullManifest` | — | No | Sí (actualiza `last_seen_at`) |

Los rechazos (401, 403, 409) **no** se registran aquí; Engram ya los audita en `cloud_auth_audit_log` y `cloud_sync_audit_log` (CA-14).

### 4.3 Captura de IP y cliente

La IP y el cliente los aporta el **contexto de petición de NX-04** (§5). NX-02 se construye antes, así que el middleware de NX-04 se implementa al inicio de NX-02 (tarea T-12) y NX-04 lo extiende a la auditoría de seguridad.

### 4.4 Componentes

```
cloudserver/nexo_activity.go
  type nexoActivityRecorder interface {
      RecordActivity(ctx, cloudstore.ActivityEvent) error
  }
  func (s *CloudServer) nexoRecordActivity(r *http.Request, op, project string, count int)
      // resuelve actor (NX-01), IP y cliente; llama al store; si falla → log, nunca error HTTP

cloudstore/nexo_activity.go
  type ActivityEvent struct { Actor MutationActor; Operation, Project string; ItemCount int; ClientIP, UserAgent string; LogDetail bool }
  func (cs *CloudStore) RecordActivity(ctx, ActivityEvent) error
      // una transacción: INSERT en nexo_activity_log (si LogDetail) + UPSERT en nexo_actor_presence
  func (cs *CloudStore) PurgeActivityOlderThan(ctx, days int) (int64, error)
```

Igual que con `InsertAuditEntry`, el store se detecta por *type assertion* (`s.store.(nexoActivityRecorder)`) para no ampliar la interfaz de Engram ni romper los fakes de sus tests.

**Puntos de enganche** (una línea cada uno, marcada `// NX-02`): al final del camino exitoso de `handlePushChunk` (`cloudserver.go:~814`), `handleMutationPush` (`mutations.go:~259`), `handlePullChunk`, `handleMutationPull` (`mutations.go:~470`) y `handlePullManifest`.

**Conservación (RF-15):** variable `NEXO_ACTIVITY_RETENTION_DAYS` (por defecto 180). Al iniciar y luego cada 24 h, el servidor ejecuta `PurgeActivityOlderThan`. `0` = no depurar.

## 5. NX-04 — Metadatos de la conexión (enfoque Utopia)

Patrón tomado de Utopia (`crates/utopia-server/src/client_ctx.rs`): un middleware captura el origen de la petición **una vez** y lo deja en el contexto; los registros lo leen sin cambiar las firmas de las funciones de negocio. No se modifica el cliente.

### 5.1 Middleware de contexto

```go
// cloudserver/nexo_clientctx.go
type ClientContext struct {
    IP          string // IP real del cliente
    UserAgent   string // recortado a 256 caracteres
}
func nexoClientContextMiddleware(trusted []netip.Prefix, next http.Handler) http.Handler
func ClientContextFrom(ctx context.Context) (ClientContext, bool)
```

- **IP:** se toma `r.RemoteAddr` (sin puerto). Si esa IP pertenece a `NEXO_TRUSTED_PROXIES` (lista de IP o CIDR, por ejemplo la red Docker del proxy Caddy), se usa el primer valor de `X-Forwarded-For`; si no, la cabecera se ignora (RF-21, CA-22).
- **Cliente:** `User-Agent`. El cliente oficial envía el genérico de Go (`Go-http-client/1.1`); sirve para distinguirlo del navegador (panel) y de scripts.
- **Dónde se engancha:** envolviendo el `http.Handler` del servidor una sola vez, donde `CloudServer` expone su mux (una línea, `// NX-04`). Aplica a todas las rutas: sync, admin y panel.

### 5.2 Lo que la PC ya envía (RF-22)

- `req.CreatedBy` de `/sync/push` y `/sync/mutations/push` ya llega del cliente oficial (`USER`, `USERNAME` o hostname; `internal/sync/sync.go:2808-2820`).
- Se guarda en `actor_client_claim` (NX-01) y, en NX-02, en dos columnas nuevas: `nexo_activity_log.client_claim` y `nexo_actor_presence.last_client_claim`.

### 5.3 Auditoría de seguridad con origen (RF-24)

- Engram registra sus eventos con `recordAuthAuditBestEffort` (`cloudserver.go:491`) en `cloud_auth_audit_log`, cuyo campo `metadata` es `JSONB`.
- Hook de una línea (`// NX-04`) en esa función: si hay `ClientContext`, agrega `client_ip` y `user_agent` al mapa `Metadata` antes de insertar. **No** cambia el esquema de Engram.
- Cubre los eventos que Engram ya emite: `dashboard.login`, `bootstrap.*`, `user.*`, `token.*`, `grant.*`, `sync.auth` y `sync.authorize` (rechazos).

### 5.4 Nombre del token (RF-23)

Convención operativa, sin código: al emitir un token en el panel, el campo *name* se completa con la máquina o el uso (`laptop-ana`, `ci-pipeline`). Las vistas de NX-01 y NX-03 lo muestran mediante `cloud_principal_tokens.name`.

## 6. NX-03 — Auditoría consultable (API JSON)

### 6.1 Rutas

Todas con `withAuth` + `requireManagedAdmin` (el mismo control que `/admin/*` de Engram, `admin_handlers.go:374-385`: rol `admin` y token asignado). Se registran en una función propia `registerNexoAdminRoutes(mux)`, llamada con una línea `// NX-03` donde Engram registra sus rutas (`cloudserver.go:~337`).

| Ruta | Fuente | Filtros |
|---|---|---|
| `GET /nexo/admin/audit` | `cloud_auth_audit_log` (Engram) | `from`, `to`, `action`, `actor`, `outcome`, `cursor`, `limit` |
| `GET /nexo/admin/activity` | `nexo_activity_log` | `from`, `to`, `actor`, `token`, `project`, `operation`, `cursor`, `limit` |
| `GET /nexo/admin/presence` | `nexo_actor_presence` | `actor`, `since` |
| `GET /nexo/admin/history/{entityKey}` | vista `nexo_mutation_history` | `project`, `include_payload=true` (opcional) |

### 6.2 Respuesta y paginación

```json
{
  "items": [ { "...": "..." } ],
  "next_cursor": "123456",
  "limit": 100
}
```

- Orden de lo más reciente a lo más antiguo; cursor = último `id` o `seq` devuelto (paginación por llave, no por `OFFSET`).
- `limit` por defecto 100, máximo 200 (RF-35).
- Los registros incluyen, cuando corresponde: `actor`, `actor_email`, `token_prefix`, `token_name`, `client_ip`, `user_agent`, `client_claim`.
- `history` devuelve solo `title` y metadatos; el contenido completo (`payload`) solo con `include_payload=true` (RF-34).

### 6.3 Componentes

```
cloudserver/nexo_admin_routes.go   handlers + validación de filtros
cloudstore/nexo_queries.go         ListAuthAudit (reusa ListAuthAuditEvents si sus filtros alcanzan),
                                   ListActivity, ListPresence, MutationHistory
```

- **Auditoría de la consulta (RF-36):** cada handler registra un evento `nexo.audit.read`, `nexo.activity.read`, `nexo.presence.read` o `nexo.history.read` en `cloud_auth_audit_log`, con el admin como actor.
- **Índices (RNF-05):** los de NX-02 (`occurred_at`, `actor_principal_id`) más `idx_cloud_auth_audit_log_occurred_at`, que ya existe en Engram.

## 7. Compatibilidad y riesgos

| Riesgo | Mitigación |
|---|---|
| El cliente oficial deja de funcionar | Las respuestas HTTP no cambian; prueba e2e con el cliente oficial 3.3.2 (tarea T-14) |
| Conflictos al actualizar Engram | Lógica en `nexo_*.go`; ediciones de una línea marcadas; lista en `NEXO_CHANGES.md` |
| Volumen por la sincronización automática | Las descargas vacías solo tocan la presencia (1 fila por persona y token) |
| Latencia | Un `INSERT` + un `UPSERT` por petición exitosa, indexados; medir en T-15 |
| Falla de la tabla de actividad | Error logueado; la sincronización continúa (RF-16) |
| Datos personales (IP) | Conservación limitada y configurable (RN-A4) |
| `Principal.ID` o `TokenID` con formato inesperado | Verificar en T-03 antes de fijar la vista |

## 8. Pruebas

| Nivel | Qué | Cómo |
|---|---|---|
| Unitarias | `nexoResolveActor` (las tres situaciones), extracción de IP con y sin proxy confiable | `go test ./internal/cloud/cloudserver/...` |
| Integración (Postgres) | Migraciones idempotentes, columnas de autor en ambos caminos, borrados, vista de historial, `RecordActivity`, trigger de inmutabilidad, depuración | `CLOUDSTORE_TEST_DSN` contra un Postgres de prueba en Docker |
| Handler | Con token asignado, el autor es el dueño del token e ignora `req.CreatedBy`; con token compartido o sin autenticación, el autor es `no-identificado` y `req.CreatedBy` queda solo en `actor_client_claim` | Tests con fakes de store |
| NX-04 | Middleware: IP con y sin proxy confiable, cabecera falsa ignorada, User-Agent recortado; metadata de `cloud_auth_audit_log` con IP y cliente | `httptest` + Postgres de prueba |
| NX-03 | Rutas: 403 para `member` y token compartido; filtros, paginación por cursor, límite 200, `include_payload`; cada consulta auditada; tiempos con 1 millón de filas sintéticas | `httptest` + Postgres de prueba |
| Regresión | Suite completa de `internal/cloud/...` sin fallos nuevos | `go test ./internal/cloud/...` |
| E2E | Instancia `panel-admin` con la imagen del fork: Ana y Luis con tokens propios, cliente oficial 3.3.2; se verifican CA-01 a CA-36 | Script en `06-pruebas/` (repo de Nexo) |

## 9. Despliegue

1. Compilar la imagen del servidor desde `develop` (`nexo-engram-cloud:3.3.2-nexo.1`).
2. Las migraciones se aplican solas al iniciar (aditivas).
3. Cada persona recibe su token asignado desde el panel; `ENGRAM_CLOUD_TOKEN` se define con un valor aleatorio que no se entrega a nadie (RN-A3).
4. Las PCs no cambian.

## 10. Desviaciones de implementación (fases 0-4)

1. **Escritura del autor (NX-01).** Las sentencias `INSERT` de Engram no se modificaron. Dentro de la transacción de `WriteChunk` e `InsertMutationBatch`, una línea (`nexoApplyMutationActor`, `// NX-01`) publica el actor con `set_config(..., true)` (local a la transacción) y un trigger `BEFORE INSERT` (`nexo_stamp_actor`) lo copia en cada fila de `cloud_mutations` y `cloud_chunks`. El email se sigue copiando **en el INSERT** con la subconsulta a `cloud_human_users`, dentro del trigger. Costo: una sentencia extra por transacción de escritura.
2. **Formato de IDs (T-03).** `Principal.ID` = `cloud_principals.id::text` y `TokenID` = `cloud_principal_tokens.id::text` (ambos `BIGINT`, por ejemplo `"1"`). Los `JOIN` de la vista con `::text` son correctos. La vista agrega al final la columna `actor_token_id`.
3. **Migraciones.** Se ejecutan al final de `migrate` en una transacción con `pg_advisory_xact_lock` (evita choques entre procesos o tests en paralelo). Los triggers se crean con un bloque `DO` que verifica `pg_trigger` (compatible con Postgres < 14).
4. **Verificado.** `actor_verified = TRUE` exige `Kind` `human`/`service_account` **y** `Source = managed_token`.
5. **Camino de cambios.** `created_by` del paquete materializado pasa a ser el actor resuelto (`no-identificado` con token compartido o sin autenticación; antes `LEGACY_SYNC`/`unknown`). `mutationPushCreatedBy` de Engram se conserva sin uso en producción (sus tests siguen).
6. **Test de Engram ajustado.** `cmd/engram/cloud_runtime_e2e_test.go` esperaba que `/sync/push` guardara el `created_by` declarado por el cliente; se ajustó (marcado `// NX-01`) a que guarde el dueño del token, como pide RF-04.
7. **Middleware (T-12).** Tipos con prefijo (`NexoClientContext`, `NexoClientContextFrom`) para aislar de Engram. Se engancha en `Handler()` (`// NX-04`). `X-Forwarded-For` se recorre de derecha a izquierda saltando proxies confiables (con un solo proxy, como Caddy, equivale al «primer valor» y resiste cabeceras falsificadas por el cliente).
8. **Registro (NX-02).** `nexoRecordActivity` recibe además `clientClaim`. Es síncrono, con tiempo máximo de 2 s y `recover`. `item_count` incluye también el arreglo `mutations` del paquete. `pull_count` solo sube con descargas que entregan datos; el manifiesto y las descargas vacías solo actualizan `last_seen_at`, IP y cliente. En `pull_mutations` el proyecto es el de los cambios devueltos (si hay varios, la lista separada por comas). El trigger de inmutabilidad también bloquea `TRUNCATE`.
9. **Conservación.** La depuración arranca en `Start()` (`// NX-02`): al iniciar y cada 24 h; `NEXO_ACTIVITY_RETENTION_DAYS` inválido usa 180.

**Fases 3 y 4 (NX-04, NX-03):**

10. **Dónde se agrega el origen a la auditoría (T-16).** El hook no va en `recordAuthAuditBestEffort`: esa función solo cubre los rechazos de sincronización. Engram audita también por otros tres caminos que no pasan por ella: `recordAdminAudit` (API `/admin/*`), el login y el bootstrap del panel (`store.InsertAuthAuditEvent` directo) y la emisión de tokens (`CreatePrincipalTokenWithAudit`, que inserta dentro de su transacción). El hook va en el único punto común, `cloudstore.insertAuthAuditEvent` (`identity.go`, una línea `// NX-04`), que agrega `client_ip` y `user_agent` a `metadata`. El origen viaja en el contexto (`cloudstore.WithNexoRequestOrigin`, `cloudstore/nexo_audit.go`), puesto por el mismo middleware de T-12 (`nexoWithClientContext`). Nunca pisa claves que el evento ya traiga ni modifica el mapa del llamador; sin middleware (llamadas internas) la metadata queda como la define Engram. El esquema de Engram no cambia.
11. **Login rechazado en el panel (CA-24).** `ValidateLoginToken(token)` de Engram no recibe la petición (usaba `context.Background()`), así que su auditoría no podía llevar origen. Se agregó el campo opcional `MountConfig.NexoValidateLoginTokenRequest` (`dashboard.go`, `// NX-04`) y `dashboard/nexo_login.go`, que lo usa si está y si no cae en `ValidateLoginToken`. En `dashboard_session.go`, `validateDashboardLoginToken` pasó a envolver a `validateDashboardLoginTokenContext(ctx, token)` (`// NX-04`); `cloudserver.go` entrega la variante con `context.WithoutCancel(r.Context())`, que conserva el origen y mantiene la semántica de Engram (sin cancelación).
12. **Orden y cursor (RF-35).** Auditoría y actividad se ordenan por `id DESC` e historial por `seq DESC` (paginación por llave estable; `occurred_at` es la hora de inicio de la transacción y coincide en la práctica). El cursor es el `id`/`seq` como texto. En presencia el cursor es opaco (`base64url` de `last_seen_at|presence_key`); como la presencia se actualiza, una fila que se reconecta entre una página y la siguiente puede subir al inicio y no aparecer en esa recorrida.
13. **Parámetros y campos.** Además de lo previsto: `action` acepta prefijo con `*` (`nexo.*`); `actor` acepta id, usuario, email o nombre mostrado (en auditoría también `actor_source`, por ejemplo `request`; en actividad y presencia también `no-identificado`); `token` acepta id, nombre o prefijo; `project` también coincide dentro de la lista separada por comas de `pull_mutations`; historial admite `entity`, `cursor`, `limit` y devuelve `version` (1, 2, ... por proyecto y tipo de entidad). `limit` mayor que 200 se recorta a 200 (no es error); `limit` no numérico o menor que 1 es `400`. `to` es exclusivo y una fecha sola (`AAAA-MM-DD`) cubre el día completo. En las respuestas el token se llama `token_id`, `token_prefix`, `token_name`; `next_cursor` es `null` al final.
14. **Auditoría de la lectura (RF-36).** Se registra después de consultar y antes de responder, con los filtros usados y `result_count`. Si no se puede registrar, la respuesta es `500` y no se entregan datos (sin lectura sin rastro, igual criterio que el login del panel). Las peticiones rechazadas (`401`, ya auditada por Engram como `sync.auth`; `403`) y las inválidas (`400`) no se registran como lectura.
15. **Consultas.** `ListAuthAudit` es nueva: `ListAuthAuditEvents` de Engram no tiene filtros ni cursor. El almacén de consultas se detecta por *type assertion* (`nexoTraceStore`) sobre `store` y, si no, sobre `adminIdentity`.
16. **Índices nuevos (RNF-05).** `cloud_mutations (entity_key, seq)`, `nexo_activity_log (actor_principal_id, id DESC)` y `(actor_token_id, id DESC)`, `nexo_actor_presence (last_seen_at DESC, presence_key DESC)`. Medido con 1 millón de filas de actividad y 1 millón de auditoría: menos de 40 ms por página en todos los casos (test `TestNexoQueriesPerformanceWithMillionRows`, solo con `NEXO_PERF_TEST=1`).
17. **Manual (T-18).** La convención de nombres de token y el uso de la API quedan en `docs/nexo/trazabilidad/manual-admin.md` del fork. El manual del kit (`02-nexo-kit/MANUAL.md`, fuera de este repositorio) puede enlazarlo.
