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
   *Reemplazado en la unificación de roles: ver §11.4 (corrección única al migrar, con marca).*
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
5. **Arranque del superadmin.** *(Reemplazado por §11: el primer admin queda superadmin al crearse y la migración corrige una sola vez; se quitó la resolución en caliente.)* Además de la migración (design §6.3), se resuelve en caliente: si no
   hay ningún superadmin habilitado, el admin humano habilitado de Engram de menor id sin fila de
   plataforma (función SQL `nexo_authz_bootstrap_candidate()`) recibe `superadmin` en su primera
   petición y queda persistido con evento `binding.granted` de actor `sistema`. Así
   `engram cloud bootstrap admin` después de arrancar el servidor cumple RF-01 sin reiniciar.
   Consecuencia: si todos los superadmins quedaran deshabilitados por SQL directo, el admin de
   menor id pasa a superadmin (recuperación).
6. **Cuentas de servicio en plataforma.** Una cuenta de servicio es siempre miembro de plataforma
   aunque su rol de Engram sea `admin` (R-10, fallar cerrado).
7. **Rol de Engram en sintonía.** *(Ampliado por §11: la columna la deriva la base con triggers.)* `PUT/DELETE /platform/members` actualiza también
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

## 11. Roles de plataforma unificados (feat/roles-unificados)

Decisión del usuario (2026-10-13, opción A, alineada con OWASP: "hay que centralizar el rol"): una
persona tiene **un solo rol de plataforma**, el de Nexo. Reemplaza la compatibilidad de las secciones
6.3 y 10 (puntos 5 y 7) y de `requirements.md` §9. Alcance: solo plataforma y su relación con los
espacios. Los **proyectos** de memoria quedan fuera: no cambia la autorización de sincronización ni
los permisos de proyecto de Engram (`cloud_project_grants`).

### 11.1 Fuente única y equivalencia

El rol que se elige, se muestra y se audita es el de Nexo (`nexo_role_bindings`, alcance plataforma).
La columna `cloud_principals.role` (`admin` | `member`) sigue existiendo, porque Engram la lee en sus
consultas y pantallas, pero **se deriva**; nadie la escribe por su cuenta:

| Rol de plataforma (Nexo) | Columna `role` (Engram) | Quién lo asigna |
|---|---|---|
| miembro (sin fila) | `member` | cualquier admin al crear; por defecto |
| auditor | `member` | solo un superadmin (`platform.admins.manage`) |
| admin | `admin` | solo un superadmin |
| superadmin | `admin` | solo un superadmin (el primero: `engram cloud bootstrap admin`) |

Una cuenta de servicio es siempre miembro (R-10) y su columna es siempre `member`. El valor histórico
`member` que aceptan los formularios y la API de Engram vale "miembro".

### 11.2 Quién escribe la columna: nadie, la deriva la base

- Función `nexo_platform_engram_role(id)`: `admin` si la persona humana tiene `admin` o `superadmin`
  en plataforma; si no, `member`.
- Trigger `nexo_role_bindings_sync_engram` (`AFTER INSERT OR UPDATE OF role OR DELETE` en
  `nexo_role_bindings`): recalcula la columna **en la misma transacción** que cambia la asignación.
- Trigger `nexo_principal_role_derive` (`BEFORE INSERT OR UPDATE OF role` en `cloud_principals`): fuerza
  el valor derivado en cualquier escritura de la columna. Un formulario viejo, la API `/admin` de
  Engram, el bootstrap, la recuperación o SQL directo no pueden dejarla distinta del rol de Nexo.
- Los caminos de Engram que antes escribían la columna ahora piden un rol a Nexo en su transacción
  (`// NX-05`): `CreateHumanUser`, `CreateFirstAdminHumanUser` y `CreatePrincipal` (alta con
  `authz.AssignOnCreateTx`) y `UpdatePrincipal` (`authz.SyncEngramRoleTx`, que traduce `admin` o
  `member` al rol de Nexo y deja el evento). Sin `CreateRequest` en el contexto, `role = admin` significa
  "pide ser administrador": el primero sin superadmin habilitado queda superadmin (diseño §6.3, ahora
  explícito al crear y no al arrancar) y los demás, admin.
- Cada cambio deja un evento en `nexo_authz_events` (`binding.granted|changed|revoked` con actor,
  antes, después, token, IP y cliente, más `engram_role_before/after` en el detalle) y dispara la
  alerta `platform_role_changed`. Cambiar la columna sola no genera evento propio porque no ocurre.
- La columna sigue sirviendo de guarda de integridad de Engram ("al menos un admin": `HasActiveAdmin`,
  `guardLastActiveAdminTx`, elegibilidad de `recover-token`). Como se deriva, cuenta admins y
  superadmins habilitados. No decide ningún permiso.

### 11.3 Un solo punto de decisión

OWASP ASVS 4.0.3 V1.4.4 exige un único mecanismo de control de acceso por el que pasen todas las
peticiones (en ASVS 5.0.0, el requisito más cercano es V8.3.1: las reglas de autorización se aplican en
una capa de servicio de confianza; V8.2.1 pide permisos explícitos por función). Toda decisión de
plataforma pasa por `authz.Engine.Can` (`Require` si debe auditarse la denegación). Rutas de Engram que
decidían con la columna y cómo quedan:

| Ruta o pantalla (archivo) | Antes (columna `role`) | Ahora |
|---|---|---|
| `requireManagedAdmin`: toda `/admin/*` y los POST del panel (`admin_handlers.go`) | `Role == admin` y token asignado | `Require(capacidad de la ruta)` con `nexoRouteCapability`: `platform.directory.read`, `platform.users.manage`, `platform.tokens.manage`; sin almacén Nexo (dobles de prueba) sigue la regla de Engram |
| `isDashboardAdmin` (`cloudserver.go`): menú, `GET /dashboard/admin`, `/projects`, `/users`, `/users/{id}`, `/health`, `/audit-log`, y los POST de sincronización de proyecto del panel | `Role == admin` con token asignado o admin heredado | `Can(platform.users.manage)` para sesiones con token asignado (`nexoDashboardAdmin`); el token heredado sigue como hoy |
| `canManageManagedUsers` (formularios de Usuarios) | idem | idem (`nexoDashboardAdmin`) |
| `POST /dashboard/admin/users` y `POST /admin/users` (rol al crear) | cualquier admin podía crear otro admin (corregido en R1) | `platform.admins.manage` para auditor, admin y superadmin; solo miembros con `platform.users.manage` |
| `POST /dashboard/admin/users/{id}/role` (nueva) | no existía | `platform.admins.manage` (`usersPanelSetRole`); 403 auto-promoción, 409 último superadmin |
| `isManagedAdminPrincipal` | columna + token asignado | solo respaldo sin almacén Nexo |
| `dashboard_session.go` (reclamos de la sesión) y `cloud_runtime_auth.go` (`principal.Role`) | informativos | informativos: ninguna decisión los lee |
| `HasActiveAdmin`, `WouldRemoveLastActiveAdmin`, `guardLastActiveAdminTx`, `recover-token`, `/dashboard/bootstrap` (`cloudstore/identity.go`) | cuentan admins por la columna | igual: guardas de integridad sobre un valor derivado; R-8 (último superadmin) sigue en los triggers de la base |

**Token heredado `ENGRAM_CLOUD_ADMIN`** (identidad no administrada, R-15): sin roles de Nexo. Conserva el
comportamiento documentado de hoy: puede **ver** las pantallas de administración del panel; no
administra usuarios, tokens ni roles (`requireManagedAdmin` exige un token asignado) y la API de Nexo
le responde 403. **Cliente oficial de Engram:** las rutas `/sync/*` no cambian; las respuestas siguen
siendo las mismas.

### 11.4 Migración y corrección única

`Migrate` (idempotente) crea las funciones, la tabla de marca `nexo_platform_role_unification` y, solo
la primera vez, corrige las instalaciones existentes **antes** de crear los triggers:

1. Cada persona con `role = admin` y sin asignación de plataforma recibe una (evento `binding.granted`,
   actor `sistema`, `reason = unificacion_roles`): la primera habilitada sin superadmin habilitado, como
   `superadmin`; las demás, `admin`. Es lo que hacía el diseño §6.3 y la compatibilidad, ahora explícito.
2. La columna de todos se recalcula desde el rol de Nexo (una cuenta de servicio con `role = admin` pasa
   a `member`); un evento `engram_role.synced` por cada fila que cambió.
3. Se guarda la marca (versión, cuántas asignaciones y cuántas columnas). Después, el arranque no
   corrige nada: una desalineación nueva solo se informa (§11.5). Se quita la función
   `nexo_authz_bootstrap_candidate()` del arranque (queda en las bases existentes sin uso).

### 11.5 Desalineación: detectar, avisar, corregir

Con los triggers no debería existir; puede aparecer si alguien los desactiva o restaura una copia vieja.

- `engram nexo authz reconcile-roles [--apply] [--json]` (ENGRAM_DATABASE_URL): en seco por defecto, lista
  las diferencias y sale con 1 si hay; con `--apply` recalcula la columna desde el rol de Nexo (nunca al
  revés), un evento `engram_role.synced` por corrección, idempotente.
- Alerta `platform_role_drift` (severidad alta, una por persona y estado; se apaga con
  `NEXO_ALERT_RULES`), evaluada cada minuto.
- La revisión de accesos marca `role_drift` y totaliza `role_drift` en el resumen.
- El motor no concede nada por una columna `admin` sin asignación: es una desalineación que se informa.

### 11.6 Panel (Administración > Usuarios)

Ver `docs/nexo/panel/design.md` §13: alta con miembro, auditor, admin o superadmin (la interfaz desactiva
lo que quien mira no puede asignar; decide el servidor), lista y detalle con el rol en español, cambio de
rol del superadmin con confirmación y CSRF, rol junto al nombre en la cabecera.

### 11.7 Referencias

- OWASP ASVS 4.0.3 V1.4.4 (mecanismo único de control de acceso) y ASVS 5.0.0 V8.3.1 / V8.2.1 / V8.1.1
  (no hay equivalencia uno a uno: ASVS 5 reorganizó el capítulo).
- OWASP Top 10 2021 A01 Broken Access Control.
- ISO/IEC 27001:2022 Anexo A 5.18 (derechos de acceso) y 8.2 (derechos de acceso privilegiados).

### 11.8 Riesgos y límites

- Si se vuelve a la imagen oficial de Engram, el trigger `nexo_principal_role_derive` sigue en la base y
  reescribe la columna con el valor derivado (Engram oficial no lo sabe). Para abandonar el fork:
  `DROP TRIGGER nexo_principal_role_derive ON cloud_principals; DROP TRIGGER nexo_role_bindings_sync_engram ON nexo_role_bindings;`
  y fijar a mano la columna.
- Restaurar una copia anterior a la unificación sobre un servidor nuevo: la marca viaja en la copia
  (si la copia la trae, no se corrige de nuevo; use `reconcile-roles`).
- Los roles `auditor` y `superadmin` no tienen equivalente en Engram: para Engram el auditor es `member`.
