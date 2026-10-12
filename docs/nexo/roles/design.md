# Roles: diseño técnico

Estado: borrador para revisión. Fecha: 2026-10-12. Requisitos: `requirements.md` (NX-05).

## 1. Principios

1. **Un motor de autorización para todo.** Paquete `internal/nexo/authz`, usado por la plataforma,
   las rutas de proyectos de Engram y los espacios (NX-10).
2. **Capacidades en el código, roles en una tabla.** Las rutas piden una capacidad; `authz` traduce el
   rol efectivo. Agregar un rol es agregar una entrada.
3. **Engram se toca solo en enganches.** Cada punto de control en código de Engram es una llamada de
   una línea marcada `// NX-05`.
4. **Compatible por defecto.** Sin datos Nexo, el comportamiento es el de Engram hoy (requisitos §9).
5. **Fallar cerrado.** Si `authz` no puede resolver un rol (error de base), la petición se rechaza.

## 2. Modelo de datos

```
cloud_principals (Engram) 1---* nexo_role_bindings *---1 nexo_scopes
                                                    nexo_authz_events (solo inserción)
```

### 2.1 `nexo_scopes`

| Columna | Tipo | Notas |
|---|---|---|
| `scope_type` | TEXT | CHECK `platform`, `project`, `space`; PK con `scope_key` |
| `scope_key` | TEXT | `*` para plataforma; nombre normalizado del proyecto (misma normalización que `cloud_project_grants`); `slug` del espacio |
| `visibility` | TEXT | CHECK `open`, `restricted`; por defecto `restricted` para proyectos y `open` para espacios (lo fija el servicio al crear) |
| `default_role` | TEXT | CHECK `lector`, `proponente`; por defecto `lector`; solo aplica si es abierto |
| `status` | TEXT | CHECK `active`, `archived` |
| `created_by_principal_id`, `created_at`, `updated_at` | | |

Una fila `('platform', '*')` se crea en la migración. Un proyecto de Engram sin fila en
`nexo_scopes` se trata como restringido y activo (compatibilidad).

### 2.2 `nexo_role_bindings`

| Columna | Tipo | Notas |
|---|---|---|
| `principal_id` | BIGINT | FK `cloud_principals(id)` ON DELETE CASCADE |
| `scope_type`, `scope_key` | TEXT | FK a `nexo_scopes` |
| `role` | TEXT | CHECK según nivel: plataforma `superadmin`, `admin`, `auditor`; ámbitos `lector`, `proponente`, `publicador`, `responsable` (un trigger valida la combinación) |
| `granted_by_principal_id`, `created_at`, `updated_at` | | |

PK (`principal_id`, `scope_type`, `scope_key`). Índices: (`scope_type`, `scope_key`, `role`) para
listar miembros y contar responsables; (`principal_id`) para `VisibleScopes`.

"miembro" de plataforma es la ausencia de fila de plataforma: no se guarda.

### 2.3 `nexo_authz_events` (solo inserción)

`id BIGSERIAL`, `occurred_at`, `action` (`binding.granted`, `binding.changed`, `binding.revoked`,
`scope.created`, `scope.updated`, `scope.archived`, `scope.recovered`, `authz.denied`),
`scope_type`, `scope_key`, `target_principal_id`, `role_before`, `role_after`, actor (principal,
token, etiqueta, email), `client_ip`, `user_agent`, `details JSONB`. Trigger de inmutabilidad como
`nexo_activity_log`. La API de auditoría NX-03 agrega `GET /nexo/admin/authz-events`.

## 3. Motor `internal/nexo/authz`

```go
type ScopeType string // "platform" | "project" | "space"
type Scope struct { Type ScopeType; Key string }
type Role string
type Capability string

type Identity struct {
    PrincipalID string
    TokenID     string
    Kind        string // human | service_account
    Verified    bool   // token administrado (NX-01)
    TokenScope  string // "" = write (hasta NX-06), "read", "write"
    EngramRole  string // admin | member (compatibilidad)
    ClientIP, UserAgent, ClientClaim string
}

type Authorizer interface {
    EffectiveRole(ctx context.Context, id Identity, s Scope) (Role, error) // "" = ninguno
    Can(ctx context.Context, id Identity, s Scope, c Capability) (Decision, error)
    VisibleScopes(ctx context.Context, id Identity, t ScopeType) ([]ScopeRole, error)
}

type Decision struct {
    Allowed bool
    Role    Role
    Reason  string // "no_role", "capability", "token_scope", "disabled", ...
    Hidden  bool   // true => responder 404 (ámbito restringido sin rol)
}
```

Tabla de capacidades (`roles.go`), una por nivel, tal como requisitos §4. El orden de roles define la
herencia: cada rol hereda las capacidades del anterior en su nivel.

Resolución del rol efectivo:

| Nivel | Orden |
|---|---|
| Plataforma | fila en `nexo_role_bindings`; si no hay y `EngramRole = admin`: `admin` (el principal de bootstrap, el menor id con rol admin, es `superadmin` en la migración); si no: miembro |
| Proyecto | fila en `nexo_role_bindings`; si no hay y existe `cloud_project_grants`: `publicador` (compatibilidad); si no y el ámbito es abierto: `default_role`; si no: ninguno |
| Espacio | fila en `nexo_role_bindings`; si no y el espacio está activo y abierto: `default_role`; si no: ninguno |

`VisibleScopes` usa una sola consulta (unión de asignaciones, permisos de Engram y ámbitos abiertos),
como `visible_kb_roles` de Utopia. Resultado cacheable por petición.

Identidad sin token administrado (token compartido, modo inseguro): `Can` devuelve denegado para
plataforma y espacios; para proyectos, `authz` no se consulta y rige el comportamiento de Engram
(R-15).

## 4. Dónde se aplica

### 4.1 Plataforma (Engram y Nexo)

| Punto | Enganche | Regla |
|---|---|---|
| `requireManagedAdmin` (`admin_handlers.go:374`) | `// NX-05` pasa por `authz` con la capacidad de cada ruta | `platform.users.manage`, `platform.tokens.manage`, `platform.audit.read` |
| `handleAdminCreateUser` (`admin_handlers.go:108`) y su par del panel | `// NX-05` antes de crear | Si `role = admin`: exige `platform.admins.manage` (solo superadmin). Hoy cualquier admin de Engram puede crear otro admin: este es el control que lo impide (RF-07) |
| Habilitar y deshabilitar usuarios (`handleAdminSetUserEnabled`, panel) | `// NX-05` | Sobre un admin, superadmin o auditor: `platform.admins.manage`; R-8 para el último superadmin |
| Rutas NX-03 `/nexo/admin/*` | ya en código Nexo | `platform.audit.read` (incluye auditor) |
| `/nexo/authz/v1` | código Nexo | sección 5 |

### 4.2 Proyectos (rutas de sincronización de Engram)

`authorizeProjectScope(ctx, w, project)` (`cloudserver.go:850`) hoy no distingue lectura de escritura.
Se agrega una variante `// NX-05` que recibe la capacidad:

| Ruta | Capacidad |
|---|---|
| Descarga de manifiesto y paquetes | `project.read` |
| Descarga de cambios (`handleMutationPull`) | lista de proyectos = `VisibleScopes(project)` con `project.read` (reemplaza `EnrolledProjectsForPrincipal` para principals administrados) |
| Subida de cambios (`handleMutationPush`) | `project.write.own` por proyecto del lote; en la etapa R3, `project.write.any` si alguna entrada toca una entidad cuyo primer autor es otra persona (consulta por `entity_key` a `cloud_mutations.actor_principal_id`, índice ya creado en NX-03) |
| Subida de paquetes (`handlePushChunk`) | `project.write.own`; en R3, el paquete ya se decodifica (`chunkcodec`), se aplica la misma regla por entidad |

Respuestas: se reutilizan los códigos de Engram (`policy_forbidden`, 403) para no romper al cliente
oficial; la denegación se audita en `nexo_authz_events` además de `cloud_auth_audit_log`.

Autosync trabado por un 403 (hallazgo de la E2E de trazabilidad): el cliente Nexo consulta
`GET /nexo/authz/v1/me` al iniciar y solo inscribe proyectos con `project.write.own`; si pierde ese
permiso, deja de inscribirlo y marca lo pendiente como conflicto local visible.

### 4.3 Espacios

Cada ruta de `/nexo/kb/v1` llama a `authz.Can` con su capacidad (`docs/nexo/conocimiento/design.md`
§7). `Decision.Hidden` se traduce a 404.

## 5. API `/nexo/authz/v1`

| Método y ruta | Capacidad |
|---|---|
| `GET /me` | `self.read`: roles en plataforma y en cada ámbito visible, alcance del token |
| `GET /platform/members` | `platform.directory.read` |
| `PUT /platform/members/{principal}` (body `role`) , `DELETE` | `platform.admins.manage` |
| `POST /scopes` (tipo, clave, visibilidad, responsable inicial) | `platform.scope.create` |
| `PATCH /scopes/{type}/{key}` (visibilidad, `default_role`) | `project.configure` o `space.configure` |
| `POST /scopes/{type}/{key}:archive` , `:restore` | `platform.scope.archive` |
| `POST /scopes/{type}/{key}:recover` (body: nuevo responsable) | `platform.scope.recover` |
| `GET /scopes/{type}/{key}/members` | `*.read` del ámbito o `platform.directory.read` |
| `PUT /scopes/{type}/{key}/members/{principal}` , `DELETE` | `project.members.manage` o `space.members.manage` |
| `GET /events` | `platform.audit.read` |

Errores y paginación como NX-03. Cada escritura genera un evento; cada denegación, `authz.denied`.

## 6. Migraciones y arranque

1. Crear tablas, trigger de combinaciones válidas y trigger de inmutabilidad (idempotente, en
   `nexo_migrations.go` o en `internal/nexo/authz` llamado desde ahí).
2. Insertar `('platform', '*')` en `nexo_scopes`.
3. Si no hay ningún superadmin: asignar `superadmin` al principal humano habilitado con rol Engram
   `admin` de menor id (el de bootstrap) y registrar el evento `binding.granted` con actor "sistema".
4. No se crean filas para proyectos existentes: rige la compatibilidad hasta que un responsable los
   configure.

## 7. Rendimiento y escala

- Una consulta indexada por petición para el rol efectivo (o `VisibleScopes` en listados y
  descargas). Objetivo < 2 ms.
- Caché por petición; opcional caché por token de 5 s en memoria (los cambios de rol tardan como
  máximo eso; la revocación de tokens no se ve afectada porque se valida aparte en cada petición).
- Sin estado entre réplicas.

## 8. Extensión

| Cambio | Dónde |
|---|---|
| Nuevo rol de plataforma (soporte, gestor de espacios) | `roles.go` + valor permitido en el trigger de combinaciones |
| Nuevo tipo de ámbito (workspace) | Valor en `scope_type`, tabla de capacidades del nivel, enganches del nuevo recurso |
| Alcance y caducidad de tokens (NX-06) | `Identity.TokenScope` ya participa en `Can`; columnas nuevas en `cloud_principal_tokens` |
| Roles desde un proveedor de identidad (OIDC, grupos) | Un `BindingSource` adicional que `authz` une a las filas locales |

## 9. Pruebas

| Nivel | Qué |
|---|---|
| Unitarias | Matriz completa nivel x rol x capacidad x alcance de token x visibilidad x estado; reglas R-1..R-15 |
| Store | Migración y arranque del superadmin, triggers, último superadmin y último responsable |
| HTTP | `/nexo/authz/v1` y las rutas de Engram con enganche: CA-R01..CA-R10 |
| Sincronización | Lector descarga y no sube; proponente sube; cliente oficial 3.3.2 sin cambios (CA-R11) |
| Rendimiento | RNF-R1 con 100 000 asignaciones |

## 10. Desviaciones de implementación (etapa R1)

Implementado en la rama `feat/roles-plataforma` (R1-01 a R1-06). Lo que difiere de las secciones
anteriores o las precisa:

1. **Paquete y archivos.** `internal/nexo/authz`: `roles.go` (tipos, tablas, herencia, alcance de
   token), `identity.go` (`Identity`, `Decision`, `ScopeRole`, identidad y caché en el contexto),
   `engine.go` (`Engine`: `EffectiveRole`, `Can`, `VisibleScopes`, `Require`), `pgstore.go`
   (Postgres: una consulta por decisión, listados y eventos), `service.go` (casos de uso con las
   reglas), `migrate.go`, `events.go`. El motor depende de una interfaz `Store`; la matriz se prueba
   con un store en memoria y el resto contra Postgres.
2. **`Identity`.** Agrega `Label` (copia del nombre para eventos) y `Enabled`. `Managed()` exige
   token administrado (`Verified`), tipo `human` o `service_account` e id numérico; si no, la
   identidad no tiene roles (R-15) y `Can` responde `unmanaged_identity` sin consultar la base,
   también en proyectos. La etapa R2 decide en el enganche de sincronización que, para esas
   identidades, rige Engram (no llama a `Can`).
3. **Normalización de proyectos.** `authz` no importa `cloudstore`: la normalización de
   `cloud_project_grants` se inyecta con `WithProjectNormalizer(cloudstore.NormalizeProjectGrant)`.
   Espacios: slug `^[a-z0-9][a-z0-9-]{1,62}$`. Clave inválida = ámbito oculto (404).
4. **Ámbito archivado.** `Can` solo admite `*.audit.read`; el resto responde `archived` (403) a
   quien tiene rol explícito, y el rol implícito deja de aplicar (oculto, 404). `VisibleScopes`
   excluye archivados. Se agregó la acción de evento `scope.restored`.
5. **Arranque del superadmin.** Además de la migración (design §6.3), se resuelve en caliente: si no
   hay ningún superadmin habilitado, el admin humano habilitado de Engram de menor id sin fila de
   plataforma (función SQL `nexo_authz_bootstrap_candidate()`) recibe `superadmin` en su primera
   petición y queda persistido con evento `binding.granted` de actor `sistema`. Así
   `engram cloud bootstrap admin` después de arrancar el servidor cumple RF-01 sin reiniciar.
   Consecuencia: si todos los superadmins quedaran deshabilitados por SQL directo, el admin de
   menor id pasa a superadmin (recuperación).
6. **Cuentas de servicio en plataforma.** Una cuenta de servicio es siempre miembro de plataforma
   aunque su rol de Engram sea `admin` (R-10, fallar cerrado).
7. **Rol de Engram en sintonía.** `PUT/DELETE /platform/members` actualiza también
   `cloud_principals.role` (superadmin y admin = `admin`; auditor y miembro = `member`). Sin esto,
   quitar el rol a un admin de Engram lo devolvería a admin por compatibilidad (requisitos §9).
8. **R-8 en la base.** Además de la regla en el servicio, dos triggers rechazan dejar a la plataforma
   sin superadmin habilitado por cualquier vía (API Nexo, `/admin/*`, panel, CLI o SQL):
   `nexo_role_bindings_guard` (quitar o degradar la asignación) y `nexo_authz_guard_principal`
   (`UPDATE OF enabled, role` y `DELETE` en `cloud_principals`). Ambos toman el advisory lock
   `nexo_authz_superadmin_guard`. El servicio bloquea primero la fila del principal y después ese
   lock (mismo orden que los triggers) para no cruzarse con la guarda de Engram.
9. **Trigger de combinaciones.** `nexo_role_bindings_validate` valida rol/nivel y también R-10
   (cuenta de servicio sin rol de plataforma ni `responsable`). La PK de `nexo_role_bindings`
   empieza por `principal_id` y cubre el índice `(principal_id)` del §2.2.
10. **Reglas de escritura.** R-1 por la PK y `ON CONFLICT DO UPDATE` (repetir el mismo rol no genera
    evento). R-9 se serializa con `SELECT ... FOR UPDATE` sobre la fila del ámbito y cuenta solo
    responsables habilitados; quitar a un responsable deshabilitado está permitido. R-11 se aplica
    también al nombrar responsable al crear o recuperar un ámbito: un admin o superadmin no se
    nombra a sí mismo (coherente con R-6 y D-7). Asignar miembros exige que el ámbito exista en
    `nexo_scopes` (un proyecto de Engram se configura primero con `POST /scopes`).
11. **Enganches extra en Engram (R-7).** Emitir o revocar tokens de otro admin, superadmin o auditor
    exige `platform.admins.manage` (`/admin/*` y panel): con un token ajeno un admin podría actuar
    como un superadmin. Los tokens propios no cambian. Habilitar o deshabilitar a un admin,
    superadmin o auditor exige `platform.admins.manage` aunque sea uno mismo.
12. **Capacidad por ruta en `requireManagedAdmin`.** El enganche usa `r.Pattern`:
    `GET /admin/users` y `GET .../grants` = `platform.directory.read`; usuarios y permisos de
    proyecto = `platform.users.manage`; tokens = `platform.tokens.manage`; `/nexo/admin/*` =
    `platform.audit.read`. Una ruta administrativa sin entrada exige `platform.users.manage`.
    Las denegaciones generan `authz.denied` con la ruta en `details`.
13. **Sin almacén Nexo, comportamiento de Engram.** Si el almacén no es `CloudStore` (fakes de los
    tests de Engram o un servidor embebido), los enganches no intervienen y la API de roles responde
    500. En producción el almacén siempre es `CloudStore`. Con almacén, un error al resolver roles
    rechaza la petición (500).
14. **Errores HTTP.** 400 `invalid_request`, 403 `policy_forbidden` (incluye `self_promotion` con
    su propio código), 404 `not_found` (ámbito oculto o inexistente, principal inexistente),
    409 `last_superadmin`, `last_owner`, `scope_exists`, `scope_has_owner`, `principal_disabled`,
    422 `service_account_role`. Un `{principal}` acepta id o nombre de usuario. Las acciones
    `:archive`, `:restore` y `:recover` se atienden con un comodín de segmento (el mux de Go no
    admite sufijos fijos).
15. **Eventos.** `GET /nexo/authz/v1/events` y `GET /nexo/admin/authz-events` son la misma consulta
    (filtros `scope_type`, `scope_key`, `action` exacta o con `*`, `principal` como actor u objetivo
    por id o usuario, `from`, `to`, `cursor`, `limit`). Como en NX-03, cada lectura se registra en
    `cloud_auth_audit_log` (`nexo.authz_events.read`) y si no se puede registrar no se entregan datos.
    `actor_token_id` es `BIGINT`; actor `no-identificado` para identidades no administradas.
16. **Rendimiento (RNF-R1).** Medido con 2 000 personas, 5 000 ámbitos y 100 000 asignaciones
    (Postgres 16 en Docker Desktop, `TestPGAuthorizationCostWith100kBindings`): `Can` mediana
    aproximada de 1,1 ms y p95 de 1,8 ms (un viaje a la base); `VisibleScopes` mediana de 1,7 ms.
    Sin caché entre peticiones (los cambios de rol rigen en la siguiente); caché por petición con
    `authz.WithRequestCache`.
17. **Pendiente para etapas siguientes.** R2: `project.read`/`project.write.own` en sincronización y
    `VisibleScopes` en descargas. R3: R-12 (`project.write.any` por autor). R4/NX-06: alcance del
    token; hoy `TokenScope` vale siempre `""` (write) y CA-R08 se prueba a nivel de motor y servicio.
    Un admin todavía puede darse a sí mismo un permiso de proyecto de Engram (`/admin/.../grants`),
    que equivale a publicador por compatibilidad: se cierra en R2 junto con las rutas de proyectos.
    El panel no muestra pantallas para el auditor (solo la API).
