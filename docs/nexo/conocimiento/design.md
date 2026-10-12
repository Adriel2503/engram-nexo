# Espacios de conocimiento: diseño técnico

Estado: borrador para revisión. Fecha: 2026-10-12. Requisitos: `requirements.md` (NX-10).
Referencias: Utopia (`knowledge_bases`, `kb_members`, `documents`, `document_versions`, `chunks`,
`pending_facts`, `access::kb_role`), trazabilidad NX-01..NX-04 (`docs/nexo/trazabilidad/`).

## 1. Principios de arquitectura

1. **Capa separada, servidor compartido.** El conocimiento es un módulo propio dentro del mismo binario
   y la misma base Postgres que Engram Cloud. Comparte identidad, auditoría y despliegue; no comparte
   tablas, rutas ni lógica con la sincronización de proyectos.
2. **Engram se toca solo en puntos de enganche.** Todo el código vive en `internal/nexo/kb/...`. En
   archivos de Engram solo hay llamadas de una línea marcadas `// NX-10` (registrar rutas, migrar,
   registrar herramientas MCP y el comando CLI). Si un día conviene un servicio aparte, el paquete se
   extrae sin reescribirlo (la identidad entra por una interfaz, sección 4).
3. **El servidor es la fuente de verdad; el cliente es una vista.** Publicar, revisar y autorizar
   ocurre solo en el servidor. La copia local es una réplica de solo lectura y descartable.
4. **Capacidades, no roles, en el código.** Las rutas piden una capacidad (`space.publish`); una tabla
   traduce rol a capacidades. Cambiar o agregar roles no toca los handlers.
5. **Inmutable donde importa.** Versiones y eventos son de solo inserción; retirar o reemplazar es un
   cambio de estado, no un borrado.
6. **Puntos de extensión explícitos.** Conversores de formato, motor de búsqueda, categorías y
   embeddings son interfaces o datos, no código cableado.

## 2. Vista de componentes

```
 PC del dev (cliente Nexo = Engram del fork)          Servidor (engram cloud serve del fork)
 +--------------------------------------+             +---------------------------------------------+
 | Asistente (Claude, Codex, ...)       |             | Capa compartida                             |
 |   herramientas MCP                   |             |   auth (tokens, principals), NX-01..NX-04   |
 |   mem_*  -> Engram SQLite (proyectos)|  /sync/*    |---------------------------------------------|
 |   nexo_* -> internal/nexo/kb/client  |-----------> | Engram: proyectos (sin cambios)             |
 |              |                       |             |---------------------------------------------|
 |              +-> copia local         | /nexo/kb/v1 | NX-10 internal/nexo/kb                      |
 |                  ~/.nexo/kb.db       |-----------> |   httpapi -> service -> authz               |
 |                  (SQLite FTS5)       |             |                     -> store (Postgres)     |
 | CLI: engram nexo kb publish|search.. |             |                     -> ingest (Markdown)    |
 +--------------------------------------+             |                     -> search (FTS)         |
 CI de un repo de lineamientos ------- publish -----> |   tablas nexo_kb_*                          |
                                                      +---------------------------------------------+
```

## 3. Paquetes

| Paquete | Responsabilidad | Depende de |
|---|---|---|
| `internal/nexo/kb/domain` | Tipos (Space, Member, Document, Version, Section, Proposal, Event), roles, capacidades, errores de dominio | nada |
| `internal/nexo/authz` (compartido, NX-05) | Roles, capacidades, asignaciones y visibilidad para proyectos y espacios (`docs/nexo/roles/`) | nada de kb |
| `internal/nexo/kb/ingest` | Interfaz `Converter` (formato a Markdown normalizado) y `Sectioner` (Markdown a secciones). Primera entrega: Markdown y texto | domain |
| `internal/nexo/kb/store` | Repositorio Postgres y migraciones `nexo_kb_*` | domain, pgx/database/sql |
| `internal/nexo/kb/search` | Interfaz `Retriever`; implementación `pgfts` (Postgres FTS) | domain, store |
| `internal/nexo/kb/service` | Casos de uso: crear espacio, publicar lote, retirar, proponer, revisar, sincronizar copia; transacciones y eventos | todos los anteriores |
| `internal/nexo/kb/httpapi` | Rutas `/nexo/kb/v1`, validación, paginación, errores JSON | service, authz |
| `internal/nexo/kb/client` | Cliente HTTP, copia local SQLite, herramientas MCP `nexo_*`, comandos CLI | domain, ingest |

Puntos de enganche en Engram (cada uno una línea, `// NX-10`):

| Archivo | Enganche |
|---|---|
| `internal/cloud/cloudserver/cloudserver.go` (`routes`) | `nexokb.Register(s.mux, s.nexoKBDeps())` |
| `internal/cloud/cloudstore/cloudstore.go` (`migrate`) | `nexokbstore.Migrate(ctx, cs.db)` |
| `internal/mcp/mcp.go` (`registerTools`) | `nexokbclient.RegisterTools(srv, cfg, allowlist)` |
| `cmd/engram/main.go` | `case "nexo": nexokbclient.RunCLI(args)` |

## 4. Identidad y autorización

El módulo no importa `cloudserver`. Recibe la identidad ya resuelta por un adaptador:

```go
// internal/nexo/kb/domain
type Identity struct {
    PrincipalID string // vacío = sin identidad asignada (token compartido o modo inseguro)
    TokenID     string
    Kind        string // human | service_account
    PlatformAdmin bool
    TokenScope  string // "" (sin NX-06), "read" o "write"
    ClientIP, UserAgent, ClientClaim string
}
```

El adaptador en `cloudserver` (archivo `nexo_kb_bridge.go`) envuelve cada ruta con `withAuth`, arma
`Identity` desde `PrincipalFromContext`, `nexoResolveActor` y `NexoClientContextFrom`, y la pone en el
contexto. Reglas:

- Sin identidad asignada (token compartido, modo inseguro): 403 en todas las rutas de conocimiento. El
  conocimiento exige usuarios administrados.
- `authz.EffectiveRole(identity, scope)`: asignación explícita en `nexo_role_bindings`, o `lector` si
  el ámbito es abierto y está activo, o ninguno. Si ninguno y el espacio es restringido: el servicio
  responde `NotFound` (no `Forbidden`) para no revelar su existencia (modelo de roles, R-2).
- `authz.Can(identity, scope, cap)`: capacidades del rol efectivo ∩ alcance del token. Las
  capacidades de plataforma (`platform.scope.create`, `platform.scope.archive`, y
  `space.members.manage` como respaldo) se conceden por `PlatformAdmin`.
- Toda consulta de lectura filtra por `space_id = ANY($visibles)`; la lista de espacios visibles se
  calcula una vez por petición con una sola consulta (como `visible_kb_roles` de Utopia).

La tabla rol -> capacidades es la del modelo de roles (sección 3.2) y vive en
`internal/nexo/authz/roles.go`, compartida con los proyectos.

## 5. Modelo de datos (Postgres)

Convenciones: prefijo `nexo_kb_`; identificadores `UUID` generados en la aplicación (sin depender de
extensiones); referencias a personas como `BIGINT` hacia `cloud_principals(id)` con copia del nombre y
email al momento (patrón NX-01); `TIMESTAMPTZ`; `JSONB` solo para datos de forma variable.

```
nexo_scopes (type=space) 1---1 nexo_kb_spaces    nexo_role_bindings (type=space) *---1 cloud_principals
                                     |
      1---* nexo_kb_documents 1---* nexo_kb_versions 1---* nexo_kb_sections
      |            |
      |            +---(superseded_by)--> nexo_kb_documents
      1---* nexo_kb_proposals ---(target)--> nexo_kb_documents
      1---* nexo_kb_events
nexo_kb_changes (bigserial): feed para copias locales
```

### 5.1 `nexo_kb_spaces`

| Columna | Tipo | Notas |
|---|---|---|
| `id` | UUID PK | |
| `slug` | TEXT UNIQUE NOT NULL | `^[a-z0-9][a-z0-9-]{1,62}$`, inmutable; coincide con `nexo_scopes.scope_key` (`scope_type = 'space'`) |
| `name`, `description` | TEXT | |
| `categories` | TEXT[] NOT NULL | por defecto: estandar, convencion, lineamiento, glosario, regla_negocio, catalogo |
| `settings` | JSONB NOT NULL DEFAULT '{}' | extensiones (tamaño de sección, idioma de búsqueda) |
| `created_by`, `created_at`, `updated_at` | | |

### 5.2 Visibilidad, estado y miembros (modelo de roles compartido)

No hay tablas propias: visibilidad (`open` por defecto para espacios) y estado (`active`,
`archived`) están en `nexo_scopes`, y los roles en `nexo_role_bindings`, ambas de
`docs/nexo/roles/design.md` §2, las mismas que usan los proyectos. Las reglas (una cuenta de
servicio no es responsable, no se quita el último responsable) las aplica `internal/nexo/authz`.

### 5.3 `nexo_kb_documents`

| Columna | Tipo | Notas |
|---|---|---|
| `id` | UUID PK | |
| `space_id` | UUID FK | |
| `doc_key` | TEXT NOT NULL | identidad estable; UNIQUE (`space_id`, `doc_key`). Para git: ruta relativa sin extensión o `id` del front matter |
| `title`, `category` | TEXT NOT NULL | `category` validada contra `spaces.categories` |
| `tags` | TEXT[] | |
| `owner_principal_id` + `owner_label` | | responsable del contenido (HU-29) |
| `status` | TEXT NOT NULL | CHECK `active`, `retired` |
| `valid_from`, `valid_until` | TIMESTAMPTZ | vigencia de la versión actual (copiada de la versión) |
| `current_version` | INT NOT NULL | |
| `superseded_by` | UUID FK documents NULL | reemplazo (HU-08) |
| `origin_kind`, `origin_ref` | TEXT | `git` + `org/repo`, `upload`, `proposal`; usado por la poda |
| `created_at`, `updated_at`, `retired_at` | | |

### 5.4 `nexo_kb_versions` (solo inserción)

| Columna | Tipo | Notas |
|---|---|---|
| `id` | UUID PK | |
| `document_id` | UUID FK | UNIQUE (`document_id`, `version`) |
| `version` | INT | 1, 2, ... |
| `content_md` | TEXT NOT NULL | Markdown normalizado (límite configurable, 2 MB por defecto) |
| `content_sha256` | TEXT NOT NULL | idempotencia |
| `source_format` | TEXT | `md`, `txt`, luego `docx`, `xlsx`, `pdf`, `pptx` |
| `source_sha256` | TEXT | hash del archivo original |
| `origin` | JSONB | git: `repo`, `branch`, `commit`, `path`, `author`, `approver`, `ci_run`; upload: `filename`; proposal: `proposal_id` |
| `change_note` | TEXT | |
| `valid_from`, `valid_until` | TIMESTAMPTZ | |
| `published_by_principal_id`, `published_by_token_id`, `published_by_label`, `published_by_email` | | autoría desde el token (NX-01) |
| `published_at` | TIMESTAMPTZ | |

Trigger de inmutabilidad (bloquea UPDATE/DELETE), igual que `nexo_activity_log`.

### 5.5 `nexo_kb_sections`

| Columna | Tipo | Notas |
|---|---|---|
| `id` | UUID PK | |
| `space_id`, `document_id`, `version_id` | UUID | `space_id` desnormalizado para filtrar sin JOIN |
| `ordinal` | INT | orden dentro de la versión |
| `anchor` | TEXT | estable: slug de la ruta de títulos + sufijo si se repite |
| `heading_path` | TEXT[] | p. ej. {Frontend, Formularios, Validación} |
| `body` | TEXT | texto de la sección (con su título) |
| `token_count` | INT | |
| `is_current` | BOOLEAN NOT NULL | true solo para secciones de la versión actual de un documento activo |
| `valid_from`, `valid_until` | TIMESTAMPTZ | copia de la versión (filtro de vigencia sin JOIN) |
| `tsv` | TSVECTOR | `setweight(to_tsvector('spanish', títulos), 'A') || setweight(to_tsvector('spanish', body), 'B')`, calculado al insertar |

Índices: GIN (`tsv`) WHERE `is_current`; (`space_id`, `is_current`); (`document_id`, `version_id`,
`ordinal`). Al publicar una versión nueva, las secciones anteriores pasan a `is_current = false`
(se conservan para leer versiones antiguas).

Acentos: la configuración `spanish` aplica raíces pero no quita tildes. Se crea una configuración
propia `nexo_es` sobre `unaccent` si la extensión está disponible (requiere permiso de creación de
extensiones); si no, se usa `spanish` y el servicio normaliza tildes en Go antes de indexar y de
consultar. La migración detecta y registra cuál se usa.

### 5.6 `nexo_kb_proposals`

| Columna | Tipo | Notas |
|---|---|---|
| `id` | UUID PK | |
| `space_id` | UUID FK | |
| `target_document_id` | UUID NULL | NULL = documento nuevo |
| `base_version` | INT NULL | versión que vio el proponente (detección de conflicto) |
| `doc_key`, `title`, `category`, `content_md`, `rationale` | | |
| `status` | TEXT | CHECK `open`, `approved`, `discarded`, `withdrawn` |
| `proposed_by_*` | | principal, token, etiqueta, email, `client_claim` (qué asistente, como `proposed_token` de Utopia) |
| `reviewed_by_*`, `review_note`, `decided_at` | | |
| `result_version_id` | UUID NULL | versión creada al aprobar |
| `created_at`, `updated_at` | | |

Índices: (`space_id`, `status`, `created_at DESC`); (`proposed_by_principal_id`, `created_at DESC`);
FTS sobre título y contenido para RF-34 (propuestas descartadas parecidas).

### 5.7 `nexo_kb_events` (solo inserción)

`id BIGSERIAL`, `occurred_at`, `space_id`, `action` (`space.created`, `space.updated`,
`space.archived`, `member.added`, `member.role_changed`, `member.removed`, `document.published`,
`document.retired`, `document.superseded`, `proposal.created`, `proposal.approved`,
`proposal.discarded`, `proposal.withdrawn`, `authz.denied`), `target_kind`, `target_id`, actor
(principal, token, etiqueta, email, verificado), `client_ip`, `user_agent`, `details JSONB` (antes y
después, motivo). Trigger de inmutabilidad. Es la auditoría del dominio y la base para notificaciones
futuras (webhooks), sin cambiar el esquema.

### 5.8 `nexo_kb_changes` (feed de la copia local)

`seq BIGSERIAL PK`, `space_id`, `document_id`, `kind` (`upsert`, `remove`), `occurred_at`. Se inserta
una fila en la misma transacción que cada publicación, retiro, reemplazo o archivado. Un cambio de
visibilidad o de miembros no necesita fila: el cliente recibe en cada sincronización la lista de
espacios visibles y borra el resto (sección 8).

## 6. Flujos

### 6.1 Publicar un lote (CLI o CI)

```
engram nexo kb publish --space lineamientos-frontend [--prune] [--dry-run] <rutas...>
```

1. El cliente recorre las rutas, aplica el `Converter` según extensión (`.md`, `.txt` en K1), lee el
   front matter YAML (`id`, `title`, `category`, `tags`, `owner`, `valid_from`, `valid_until`) y los
   valores por defecto de un `nexo-kb.yaml` opcional en la raíz. Si corre en CI, toma `origin` de las
   variables del CI (GitHub Actions: `GITHUB_REPOSITORY`, `GITHUB_SHA`, `GITHUB_REF_NAME`, actor).
2. `POST /nexo/kb/v1/spaces/{slug}/documents:batch` con hasta 200 documentos por pedido (el cliente
   pagina lotes mayores). Requiere `space.publish`.
3. El servicio, por documento y en una transacción por documento: valida, calcula
   `content_sha256`; si coincide con la versión actual responde `unchanged`; si no, inserta versión,
   corta secciones con el `Sectioner`, marca las anteriores `is_current = false`, actualiza el
   documento, inserta en `nexo_kb_changes` y en `nexo_kb_events`.
4. Con `--prune`: documentos activos del espacio con el mismo `origin_kind = git` y `origin_ref`
   (repositorio) que no vinieron en el lote pasan a `retired` (con cambio y evento). Solo si el lote
   está completo (el cliente envía `prune_scope` y el total; el servidor rechaza la poda si el lote
   llegó parcial).
5. Respuesta: resultado por documento. `--dry-run` devuelve lo mismo sin escribir (para el PR).

Idempotente y reintentable: reenviar el lote no duplica nada.

### 6.2 Buscar

`GET /nexo/kb/v1/search?q=&spaces=&category=&tag=&at=&limit=` (máximo 50).

1. Espacios visibles para la identidad ∩ `spaces` pedidos.
2. `Retriever.Search(ctx, query, filtros)`. Implementación `pgfts`:
   `websearch_to_tsquery('nexo_es', q)` sobre `tsv` con `is_current` (o, con `at`, por vigencia sobre
   todas las versiones), filtro de vigencia `valid_from <= at AND (valid_until IS NULL OR valid_until > at)`,
   orden por `ts_rank_cd` con peso de títulos, y extracto con `ts_headline`.
3. Respuesta: resultados con espacio, documento, ruta de títulos, extracto, ancla, versión, vigencia,
   categoría, responsable; y `no_results: true` explícito si no hay.

La interfaz `Retriever` permite agregar después un motor híbrido (FTS + embeddings con fusión por
rango) sin cambiar la API.

### 6.3 Proponer y revisar

1. `nexo_propose` (asistente) o `POST /spaces/{slug}/proposals` con `space.propose`.
2. Validaciones: secretos evidentes (RN-K4), tamaño, categoría.
3. Revisión: `POST /proposals/{id}:approve` (cuerpo opcional con contenido editado, vigencia) o
   `:discard` con motivo; requiere `space.review`. Aprobar reutiliza el flujo 6.1 para un solo documento
   con `origin = {kind: proposal, proposal_id}` y guarda `result_version_id`. Si
   `base_version` ya no es la actual, responde 409 salvo `force: true`.
4. El autor ve las suyas: `GET /proposals?mine=true`.

### 6.4 Copia local

Archivo propio `~/.nexo/kb.db` (SQLite con FTS5, `unicode61 remove_diacritics 2`), separado de la base
de Engram.

1. `GET /nexo/kb/v1/sync?since=<seq>` devuelve: `visible_spaces` (id, slug, rol), cambios desde `seq`
   (para `upsert`, el documento con su versión actual y sus secciones; para `remove`, solo el id) y el
   nuevo `seq`. Paginado.
2. El cliente aplica en una transacción local: borra espacios que ya no están en `visible_spaces`,
   aplica cambios, guarda `seq` y `synced_at`.
3. Disparadores: al iniciar el MCP, al comienzo de la sesión (hook) y como máximo cada 10 minutos
   mientras el MCP corre. Nunca bloquea la respuesta al asistente.
4. Consistencia: si el servidor recibió cambios de visibilidad, el feed no los necesita (punto 2). Si
   el `seq` del cliente es anterior al mínimo conservado (compactación futura), el servidor responde
   `resync_required` y el cliente reconstruye la copia.

### 6.5 Herramientas MCP

| Herramienta | Uso | Online | Offline |
|---|---|---|---|
| `nexo_search(query, spaces?, category?, at?)` | Buscar lineamientos | Servidor | Copia local, con aviso y fecha |
| `nexo_get(anchor | doc, version?)` | Leer sección o documento completo | Servidor | Copia local (solo versión vigente) |
| `nexo_spaces()` | Espacios visibles y rol | Servidor | Copia local |
| `nexo_propose(space, title, content, rationale, doc?)` | Proponer | Servidor | Error claro (HU-27), sin guardar |
| `nexo_proposals(mine?, status?)` | Ver propuestas propias o pendientes de revisar | Servidor | No disponible |
| `nexo_review(proposal, approve|discard, note)` | Revisar (solo publicadores) | Servidor | No disponible |

Las respuestas incluyen el estado para HU-07 (`Oficial`, espacio y vigencia) y el texto de citación
para RN-03. Las herramientas se registran solo si el cliente tiene un servidor configurado y token
asignado.

## 7. API (`/nexo/kb/v1`)

| Método y ruta | Capacidad |
|---|---|
| `GET /spaces` | identidad |
| `POST /spaces` | `platform.scope.create` |
| `GET /spaces/{slug}` | `space.read` |
| `PATCH /spaces/{slug}` | `space.configure` |
| `POST /spaces/{slug}:archive`, `:restore` | `platform.scope.archive` |
| `GET /spaces/{slug}/members` | `space.read` |
| `PUT /spaces/{slug}/members/{principal}` , `DELETE` | `space.members.manage` |
| `POST /spaces/{slug}/documents:batch` | `space.publish` |
| `GET /spaces/{slug}/documents` , `/documents/{doc_key}` , `/documents/{doc_key}/versions` | `space.read` |
| `POST /spaces/{slug}/documents/{doc_key}:retire` , `:supersede` | `space.publish` |
| `GET /search` , `GET /sections/{anchor}` | `space.read` |
| `POST /spaces/{slug}/proposals` | `space.propose` |
| `GET /proposals` | propias, o `space.review` del espacio |
| `POST /proposals/{id}:approve` , `:discard` | `space.review` |
| `POST /proposals/{id}:withdraw` | autor |
| `GET /sync` | identidad |
| `GET /spaces/{slug}/events` | `space.audit.read` |

Convenciones: JSON; errores `{error_code, error}`; 401 sin token válido, 403 sin capacidad (y evento
`authz.denied`), 404 para espacio restringido no visible, 409 conflicto de versión, 422 validación;
paginación por cursor opaco (máximo 200) como NX-03.

## 8. Escala y rendimiento

| Aspecto | Decisión |
|---|---|
| Búsqueda | GIN parcial sobre secciones vigentes; filtro por `space_id` desnormalizado; sin JOIN para el ranking. Medir con 1 millón de secciones (prueba sintética como NX-03) |
| Autorización por petición | Una consulta para espacios visibles y roles; sin consultas por resultado |
| Publicación | Una transacción por documento (un documento malo no aborta el lote); lotes de 200 |
| Copia local | Feed incremental por `seq`; sin cambios cuesta una consulta indexada |
| Servidor | Sin estado en memoria; varias réplicas detrás del proxy |
| Crecimiento de versiones | Las secciones no vigentes no entran al índice GIN parcial; se podrían archivar a otra tabla sin cambiar la API |
| Límites | 2 MB de Markdown por documento, 200 documentos por lote, 50 resultados por búsqueda; configurables por variable de entorno |

## 9. Extensibilidad

| Necesidad futura | Dónde se agrega |
|---|---|
| Word, Excel, PDF, PowerPoint | Nuevas implementaciones de `ingest.Converter` registradas por extensión; servidor y CLI comparten el paquete |
| Búsqueda semántica | Tabla nueva `nexo_kb_section_embeddings (section_id, model, dims, vector)` y un `Retriever` híbrido; no cambia `nexo_kb_sections` ni la API |
| Nuevo rol o cambio de permisos | Tabla de capacidades de `internal/nexo/authz` y el CHECK de `nexo_role_bindings.role` (vale para proyectos y espacios) |
| Alcance y caducidad de tokens (NX-06) | `Identity.TokenScope` ya participa en `authz.Can` |
| Notificaciones (Slack, correo) | Consumidor de `nexo_kb_events` |
| Pantallas del panel | Consumen la misma API v1 |
| Categorías o metadatos propios de un espacio | `spaces.categories` y `settings JSONB` |
| Servicio separado (opción B) | Extraer `internal/nexo/kb`; reemplazar el adaptador de identidad por introspección de tokens |

## 10. Seguridad

- Solo usuarios administrados; token compartido y modo inseguro sin acceso al conocimiento.
- Espacios restringidos invisibles para no miembros (404) en listados, búsqueda, lectura y feed.
- Autoría y origen tomados del token; lo declarado por el cliente se guarda solo como referencia.
- Contenido tratado como texto: nunca se ejecuta ni se interpreta HTML en el servidor; el cliente lo
  entrega al asistente como datos (mitiga inyección de instrucciones desde documentos, igual que el
  "confused deputy" de Utopia 0014).
- Rechazo de secretos evidentes al publicar y proponer (claves privadas, tokens con formato conocido,
  cadenas de conexión con contraseña).
- Toda denegación queda en `nexo_kb_events` (`authz.denied`).

## 11. Migración desde `nexo-transversal`

1. Crear el espacio `general` (abierto) con el responsable actual.
2. Comando `engram nexo kb import-transversal --space general`: lee del Engram local las memorias del
   proyecto `nexo-transversal` en estado Oficial, las publica como documentos (categoría según su
   contenido, origen `import`), y lista las Propuestas para que se reenvíen como propuestas.
3. Skill nexo-protocol v0.3: usa `nexo_*` para lo transversal y deja de escribir en
   `nexo-transversal`. El proyecto queda de solo lectura y se retira cuando todas las PCs actualicen.

## 12. Compatibilidad

- La sincronización de proyectos y todas las rutas de Engram quedan iguales; la prueba E2E de
  trazabilidad debe seguir pasando sin cambios.
- Las herramientas `nexo_*` existen solo en el cliente Nexo (D-R6). Un cliente oficial sigue
  funcionando para proyectos; no ve el conocimiento.
- Versionado: imagen y binario `3.3.2-nexo.2`.

## 13. Pruebas

| Nivel | Qué |
|---|---|
| Unitarias | `authz` (matriz completa rol x capacidad x visibilidad x scope), `ingest` (cortes por títulos, tablas, bloques de código, anclas estables), validaciones |
| Store (Postgres real) | Migraciones idempotentes, inmutabilidad, idempotencia de publicación, poda, feed, búsqueda con vigencia |
| HTTP | 401/403/404/409/422 por ruta, auditoría de denegaciones, paginación |
| Cliente | Copia local: aplicar feed, borrar espacios no visibles, búsqueda offline |
| E2E | CA-K01..CA-K13 con Ana, Luis y CI en un compose propio (patrón `06-pruebas/trazabilidad`) |
| Rendimiento | RNF-K1 con 1 millón de secciones; RNF-K2 con 50 000 secciones locales |


