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
| `internal/mcp/mcp.go` (`registerTools`) | `nexokbclient.RegisterTools(srv, s, allowlist)` (tanda 2: recibe el almacén de Engram solo para leer su carpeta de datos) |
| `internal/setup/setup.go` (`Install`) | `nexoAfterInstall(a.slug)` (tanda 2, §19.4: protocolo Nexo en `engram setup` con la PC conectada) |
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
| `space_id` | UUID FK | desnormalizado (entrega 2a) |
| `title`, `category`, `tags`, `owner_label` | | metadatos de esa versión (el historial muestra cómo era) |
| `content_md` | TEXT NOT NULL | Markdown normalizado (límite configurable, 2 MB por defecto) |
| `content_sha256` | TEXT NOT NULL | idempotencia |
| `meta_sha256` | TEXT NOT NULL | hash de los metadatos declarados; con `content_sha256` decide "sin cambios" |
| `source_format` | TEXT | `md`, `txt`, luego `docx`, `xlsx`, `pdf`, `pptx` |
| `source_mime` | TEXT | tipo MIME del original (`text/markdown`, `text/plain`, ...) |
| `source_sha256` | TEXT | hash del archivo original |
| `source_size_bytes` | BIGINT NOT NULL | tamaño del original |
| `source_blob_ref` | TEXT NULL | referencia al original en un `BlobStore`; hoy siempre NULL (D-K11, ver "Ingesta y procedencia") |
| `section_count` | INT | secciones de la versión |
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
| `token_count` | INT | aproximado (palabras) |
| `source_location` | JSONB NOT NULL | procedencia para citar: md/txt `heading_path`, `line_start`, `line_end`; pdf `page_start`, `page_end`; xlsx `sheet`, `row_start`, `row_end`; pptx `slide_start`, `slide_end`. Una sección que cruza hojas o rutas lo registra como lista (`sheets`) |
| `extraction` | TEXT NOT NULL | `text`, `table`, `code`, `mixed` (luego `ocr`) |
| `category`, `tags` | | copia de la versión (filtros sin JOIN) |
| `is_current` | BOOLEAN NOT NULL | atajo indexado: sección del registro vigente que rige hoy o regirá (ver bitemporalidad en "Desviaciones", D-I3) |
| `valid_from`, `valid_until` | TIMESTAMPTZ | vigencia efectiva (reloj del mundo); `valid_until` se cierra cuando entra en vigor la versión siguiente o se retira el documento |
| `recorded_from`, `recorded_until` | TIMESTAMPTZ | reloj del registro (§14.4) |
| `tsv` | TSVECTOR | `setweight(nexo_es(títulos) || sin_tildes(títulos), 'A') || setweight(nexo_es(body) || sin_tildes(body), 'B')`, calculado al insertar (D-I2) |

Índices: GIN (`tsv`) WHERE `is_current`; (`space_id`, `is_current`); (`document_id`, `version_id`,
`ordinal`). Al publicar una versión nueva, las secciones anteriores pasan a `is_current = false`
(se conservan para leer versiones antiguas).

Acentos: la configuración `spanish` aplica raíces pero no quita tildes. Se crea una configuración
propia `nexo_es` sobre `unaccent` si la extensión está disponible (requiere permiso de creación de
extensiones); si no, se usa `spanish` y el servicio normaliza tildes en Go antes de indexar y de
consultar. La migración detecta y registra cuál se usa. (Implementado con un ajuste: ver
"Desviaciones", D-I2.)

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
| Búsqueda semántica, BM25 o grafo | Índices derivados y reconstruibles detrás de `Retriever`; ver sección 14 |
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

## 14. Arquitectura de indexación y evolución de la recuperación

Decisión (2026-10-12): la forma de **guardar** el conocimiento en el tiempo y la forma de
**buscarlo** son decisiones independientes. Lo temporal es una propiedad de la fuente de verdad; RAG
de texto, híbrido o de grafo son índices derivados que respetan esa temporalidad y los permisos.

### 14.1 Flujo

```
 1. INGESTA        repositorio git / CLI / propuesta aprobada
                        |
 2. NORMALIZAR     md, docx, xlsx, pdf  ->  Markdown (ingest.Converter)
                        |
 3. FUENTE DE      documento -> versión inmutable -> secciones
    VERDAD         cada sección: valid_from / valid_until      (reloj del mundo: qué regía)
    (bitemporal)                 recorded_from / recorded_until (reloj del registro: qué estaba publicado)
                        |
                   cola de cambios (nexo_kb_changes)
                        |
 4. ÍNDICES        +--------------+-------------+-----------------------+
    DERIVADOS      | Texto (FTS / | Vectores    | Grafo (tipo LightRAG) |
    (reconstruibles| BM25)        | (embeddings)| entidades y relaciones|
    desde 3)       | etapa 1      | etapa 3     | etapa 6               |
                   +--------------+-------------+-----------------------+
                   cada entrada copia los relojes y el espacio de su sección
                        |
 5. CONSULTA       a) filtro temporal (at=, known_at=; por defecto vigente hoy)
                   b) filtro de permisos (espacios visibles, authz.VisibleScopes)
                   c) búsqueda en uno o varios índices (Retriever)
                   d) fusión (RRF) y reordenamiento opcional
                   e) respuesta con cita: espacio, documento, sección, versión, vigencia
```

### 14.2 Cómo encaja cada técnica

| Técnica | Qué indexa | Cómo respeta lo temporal |
|---|---|---|
| Texto (FTS nativo de Postgres o BM25) | Secciones | Columnas de tiempo de la sección; filtro antes de rankear |
| Vectores (pgvector) | Las mismas secciones | Mismo filtro, aplicado antes de fusionar |
| Grafo tipo LightRAG / GraphRAG | Entidades y relaciones extraídas por un LLM de las secciones | Cada relación referencia su sección y versión de origen y hereda su vigencia (grafo temporal, como Zep/Graphiti o los hechos de Utopia); al reemplazarse la sección, la relación deja de estar vigente sin borrarse |
| Temporal / bitemporal | No es un índice: columnas de tiempo de la fuente de verdad | Aplicado por todos los anteriores |

### 14.3 Reglas de diseño

1. **Se indexa por versión de sección**, no por documento: una consulta con `at=` o `known_at=` encuentra el texto de ese momento en cualquier índice.
2. **Los índices son derivados y reconstruibles** desde las secciones, alimentados por `nexo_kb_changes` mediante indexadores idempotentes. Cambiar de modelo de embeddings, pasar a BM25 o agregar el grafo es reindexar, no migrar contenido.
3. **Cada índice registra su versión** (motor, modelo de embeddings, configuración del grafo) en `nexo_kb_index_versions`, para convivir durante una transición y comparar con la evaluación (14.5).
4. **El filtro temporal y el de permisos son comunes y obligatorios**: viven en la capa de consulta, no en cada índice; un `Retriever` nuevo no puede saltarlos (prueba que lo verifica).
5. **Toda respuesta cita su origen** (espacio, documento, sección, versión, vigencia), venga del texto, de un vector o del grafo (RN-03, HU-05).
6. **La fuente de verdad sigue siendo el documento aprobado.** A diferencia de Utopia, los hechos o relaciones extraídos nunca son fuente de verdad ni requieren revisión individual: si son incorrectos, se corrige el documento y se reindexa.

### 14.4 Cambios de datos que esto implica

- `nexo_kb_sections`: agregar `recorded_from` (= `published_at` de su versión) y `recorded_until` (= publicación de la versión siguiente, retiro o reemplazo; NULL si es la actual). `is_current` se mantiene como atajo indexado.
- Búsqueda: parámetro `known_at=` además de `at=` (bitemporal ligero, etapa 2).
- `nexo_kb_index_versions (index_kind, version, config JSONB, status, built_at)`.
- Etapa 3: `nexo_kb_section_embeddings (section_id, index_version, vector)` con índice HNSW por versión.
- Etapa 6: `nexo_kb_graph_entities` y `nexo_kb_graph_relations` con `section_id`, `version_id`, `valid_from`, `valid_until`, `index_version`.

### 14.5 Medición (etapa 0)

- Conjunto de evaluación: 30 a 50 preguntas reales de los desarrolladores, cada una con la o las secciones esperadas, versionado junto a los lineamientos (`eval/preguntas.yaml`).
- Comando `engram nexo kb eval <archivo> [--retriever pgfts|local-bm25|hybrid]`: acierto en los 5 primeros (Recall@5) y posición media del acierto (MRR).
- Comparación sin costo extra: el mismo conjunto contra el servidor (FTS nativo de Postgres con `ts_rank_cd`) y contra la copia local (SQLite FTS5 con BM25 real).
- Reglas de decisión: Recall@5 >= 85 % se mantiene; si BM25 local supera claramente al nativo, pasar a una extensión BM25 en Postgres (pg_textsearch, VectorChord-bm25 o pg_search, según disponibilidad en el Postgres de producción); si ambos fallan en paráfrasis o sinónimos, pasar a híbrido; grafo solo si quedan preguntas de varios saltos sin resolver.

### 14.6 Escalera de evolución

| Etapa | Qué | Cuándo | Requisitos que mejora |
|---|---|---|---|
| 0. Medir | Conjunto de evaluación y comando `eval` | Con la entrega 2 | Base de las decisiones siguientes |
| 1. Texto + vigencia | FTS en español, vigencia, versiones, `at=` | Entrega 2 | HU-05, HU-08, RN-03 |
| 2. Bitemporal ligero | `known_at=` con `recorded_from` y `recorded_until` | Entrega 2 | HU-20 c2, auditorías |
| 3. Híbrido | pgvector, embeddings por sección en el servidor, RRF, reordenamiento opcional; la copia local sigue solo con texto | Cuando la medición lo justifique y exista decisión sobre el origen de los embeddings | HU-05 c2 |
| 4. Aplicabilidad | Metadatos `applies_to` (stack, área); el inicio de sesión inyecta solo los estándares del stack del repositorio | Entrega 3 | RNF-05, HU-05 c3 |
| 5. Contradicciones | Al guardar una memoria de proyecto, buscar secciones oficiales similares y marcar contradicciones | Después del híbrido | RN-02, HU-07 c2 |
| 6. Grafo temporal | Entidades y relaciones derivadas con vigencia y cita a la sección | Solo si quedan preguntas de varios saltos sin resolver | Preguntas transversales complejas |

### 14.7 Motores considerados

| Opción | Uso en Nexo |
|---|---|
| Postgres nativo (FTS con `ts_rank_cd`) | Etapa 1: sin dependencias, raíces en español, permisos en la misma consulta |
| Postgres + extensión BM25 (pg_textsearch, VectorChord-bm25, pg_search) | Si la medición lo justifica; incompatibles entre sí a nivel de índice; verificar disponibilidad en el Postgres de producción |
| Postgres + pgvector | Etapa 3 (híbrido) |
| Meilisearch | Primera alternativa externa si se necesita búsqueda instantánea y tolerancia a errores de tipeo en una interfaz |
| OpenSearch / Elasticsearch | Solo a gran escala; duplica datos y permisos |
| Glean u otra plataforma SaaS | Fuera de alcance: no aplica el modelo de gobierno de Nexo y los datos quedan en su nube |

Decisión pendiente para la etapa 3: origen de los embeddings, API externa (los lineamientos salen de
la empresa) o modelo propio en el servidor (por ejemplo, Ollama). Depende de la política de Real Plaza.

## 15. Ingesta y procedencia (entrega 2a)

Los archivos a indexar serán variados: Markdown y texto en K1; Word y Excel en K2; PDF y PowerPoint
en K3. El modelo ya está preparado para todos; en la entrega 2a solo existen los conversores de
Markdown y texto.

### 15.1 Conversor con bloques y procedencia

`ingest.Converter` no devuelve solo texto: devuelve el Markdown normalizado (lo que se guarda en
`content_md` y lo que cubre `content_sha256`), los metadatos (front matter) y una lista de
**bloques**. Cada bloque es una unidad que nunca se parte (párrafo o lista, título, tabla, bloque de
código) y lleva:

| Campo | Contenido |
|---|---|
| `Kind`, `Level`, `Text` | clase de bloque, nivel de título, Markdown del bloque |
| `HeadingPath` | títulos que lo contienen |
| `Location` (`source_location`) | md/txt: `heading_path`, `line_start`, `line_end` (líneas del archivo original, front matter incluido); pdf: `page_start`, `page_end`; xlsx: `sheet`, `row_start`, `row_end`; pptx: `slide_start`, `slide_end` |
| `Extraction` | `text`, `table`, `code`; `ocr` reservado (un PDF escaneado se rechaza en K1..K3, RF-18) |

El `Sectioner` corta por títulos con un tamaño objetivo (350 palabras por defecto) y conserva la
procedencia: combina las ubicaciones de sus bloques (`*_start` mínimo, `*_end` máximo, así una sección
de un PDF registra "páginas 3-5"), registra como lista lo que no coincide (`sheets: [A, B]`) y abre
una sección nueva cuando cambia la hoja o la diapositiva (`SplitKeys`), para que una sección no
mezcle hojas sin registrarlo. Las respuestas de búsqueda y lectura incluyen `source_location`,
`extraction` y una cita legible ("frontend/formularios#...validacion, v2, vigente desde 2026-09-01,
líneas 26-44"; "hoja Componentes, filas 40-60"; "página 12").

### 15.2 Registro y límites

`ingest.Registry` elige el conversor por formato explícito, por tipo MIME (con prioridad) o por
extensión. Agregar Word es registrar un `Converter` con `.docx` y su MIME; nada más cambia.

`Registry.Convert` aplica a todo conversor, también a Markdown, tres límites de seguridad:

| Límite | Por defecto | Variable |
|---|---|---|
| Tamaño máximo de entrada | 2 MB | `NEXO_KB_MAX_DOC_BYTES` |
| Tiempo máximo de conversión | 10 s (el conversor recibe un contexto con plazo; si no termina, se rechaza) | — |
| Tope de salida | 2 MB de Markdown y 20 000 bloques | `NEXO_KB_MAX_DOC_BYTES` |

Además: un pedido HTTP no supera `NEXO_KB_MAX_REQUEST_BYTES` (32 MB) y un lote no supera
`NEXO_KB_MAX_BATCH` (200 documentos). Un conversor que entra en pánico se aísla (error del documento,
el lote sigue).

### 15.3 Originales: `BlobStore` (D-K11)

Decisión ratificada: por ahora **no** se guardan los archivos originales, pero el diseño permite
hacerlo después sin migrar contenido.

- `nexo_kb_versions` guarda siempre `source_sha256`, `source_mime`, `source_size_bytes` y
  `source_blob_ref` (hoy NULL).
- `internal/nexo/kb` define `BlobStore` (`Put` por hash y tipo, `Get` y `Delete` por referencia; un
  `Put` del mismo hash devuelve la misma referencia). La implementación por defecto,
  `NoopBlobStore`, no guarda nada y devuelve una referencia vacía.
- Publicar llama a `BlobStore.Put` dentro de la transacción del documento y guarda la referencia que
  devuelva.

Cómo se activaría: (1) implementar `BlobStore` sobre Postgres (tabla `nexo_kb_blobs (sha256 PK,
mime, size, data BYTEA)`, solo inserción) o sobre almacenamiento de objetos (S3, Azure Blob,
MinIO; la referencia es la clave del objeto, direccionada por hash); (2) pasarlo al servicio con
`service.WithBlobStore(...)` en el adaptador (`cloudserver/nexo_kb_bridge.go`); (3) desde ese
momento las versiones nuevas quedan con `source_blob_ref`. Reconvertir o reindexar desde el original
(cuando mejore un conversor) es leer el original con `Get`, volver a convertir y publicar una versión
nueva con origen "reconversión"; las versiones anteriores a la activación no tienen original
(`source_blob_ref` NULL) y solo pueden reindexarse desde `content_md`. Retención y borrado del
original serían una tarea explícita de administración, nunca de la API de contenido.

## 16. Desviaciones de implementación (entrega 2a)

Entrega 2a (rama `feat/conocimiento-nucleo`): A-03, C-01..C-06, C-08 (parcial), C-10 y la CLI mínima.
Lo que difiere de las secciones anteriores, y por qué:

| ID | Tema | Diseño | Implementado | Motivo |
|---|---|---|---|---|
| D-I1 | Paquetes | `domain, ingest, store, search, service, httpapi, client` | Se agregan `eval` (medición, C-10), `cli` (comando mínimo, cliente HTTP mínimo que pasa a `client` en 2b) y `kbtest` (entornos de prueba). `BlobStore` está en el paquete raíz `kb` | Separar la medición de cualquier motor; la copia local y MCP son de 2b |
| D-I2 | Búsqueda en español | `nexo_es` = `unaccent` + `spanish_stem` | `nexo_es` = copia de `spanish` (sin `unaccent`); se indexa además la variante sin tildes del mismo texto: con la extensión, `nexo_es_unaccent` (`unaccent` + `spanish_stem`); sin ella, `nexo_es` sobre el texto plegado en Go. La consulta combina ambas con OR. `NEXO_KB_DISABLE_UNACCENT=1` fuerza el modo sin extensión. El modo queda en `nexo_kb_index_versions.config.accent_folding` | El lematizador Snowball reconoce sufijos acentuados ("-ación"); con `unaccent` antes, "validación" y "validaciones" dejaban de coincidir (medido: `valid` frente a `validacion`) |
| D-I3 | Bitemporalidad | Al publicar, las secciones anteriores pasan a `is_current = false` y `recorded_until` = publicación | Regla: al entrar en vigor una versión en `F` (o retirarse el documento en `F` = ahora), cada sección vigente del documento que empezaba en `F` o después queda reemplazada en el registro (`recorded_until`); la que regía antes deja de regir en `F` (`valid_until = LEAST(valid_until, F)`) y sigue "actual" solo si todavía rige | Una versión con vigencia futura no debe dejar sin estándar a nadie hasta que empiece (prueba `TestFutureVersionKeepsCurrentUntilItStarts`); `at=` en el pasado encuentra lo que regía antes de un retiro (CA-K09). Limitación del modelo ligero: el cierre de `valid_until` se escribe en la fila, así `known_at` anterior al cambio ve el `valid_until` nuevo |
| D-I4 | Consulta en lenguaje natural | `websearch_to_tsquery` (todas las palabras) | Primero todas las palabras; si faltan resultados, cualquiera de ellas (OR), marcado `match: some_terms`. No aplica si la consulta tiene exclusiones (`-palabra`) | Medición C-10: con AND, Recall@5 = 54,5 % (preguntas como "cómo mostrar mensajes de error" fallan por una palabra); con la segunda pasada, 100 % y MRR 0,955 en el conjunto de prueba. `no_results` sigue siendo explícito cuando ninguna palabra aparece |
| D-I5 | Ranking | `ts_rank_cd` sobre todas las coincidencias | Dos etapas: `ts_rank` (pesos A/B) sobre los candidatos y `ts_rank_cd` + `ts_rank` sobre los 100 mejores; candidatos cortados en `NEXO_KB_RANK_CANDIDATES` (20 000) | `ts_rank_cd` cuesta unas 25 veces más que `ts_rank` (3,2 s frente a 0,13 s con 181 000 coincidencias). El corte acota el peor caso de términos casi universales (ver RNF-K1 abajo) |
| D-I6 | Índices | GIN parcial `WHERE is_current` | GIN parcial (vigentes, el atajo sin parámetros de tiempo) y GIN completo (consultas con `at` o `known_at`) | Las consultas históricas también deben usar índice |
| D-I7 | Crear espacio | Una operación | Dos transacciones: `authz.CreateScope` (ámbito, responsable, eventos de roles) y luego `nexo_kb_spaces` + `space.created`. Si la segunda falla, la fila se completa sola en el primer uso; también se completan los espacios creados por `/nexo/authz/v1/scopes` | `authz` no expone su transacción; así no se duplica su lógica (R-9, R-10, R-11) |
| D-I8 | Miembros | `GET/PUT/DELETE /nexo/kb/v1/spaces/{slug}/members` | No se duplican: se usan `/nexo/authz/v1/scopes/space/{slug}/members` (NX-05) | Un solo lugar para roles (instrucción de la entrega) |
| D-I9 | Lectura por ancla | `GET /sections/{anchor}` | `GET /spaces/{slug}/documents/{doc_key}/sections/{anchor}` y `GET /sections?space=&doc=&anchor=&version=` | El ancla es única dentro de una versión de un documento, no globalmente |
| D-I10 | Errores | 422 validación | 422 validación de dominio, 400 JSON o parámetros mal formados, 413 tamaño | Igual que el resto de la API Nexo para lo mal formado |
| D-I11 | Auditoría de la consulta de eventos | La consulta queda auditada (NX-03) | No se registra cada lectura de eventos del espacio | Evitar que la evidencia crezca con lecturas; se puede agregar a `cloud_auth_audit_log` como NX-03 en 2b |
| D-I12 | Denegaciones | `authz.denied` en `nexo_kb_events` | En `nexo_kb_events` y también en `nexo_authz_events` (`Engine.RecordDenied`) | Coherencia con la auditoría de roles |
| D-I13 | Auditor | `space.audit.read` | `ListEvents` acepta `space.audit.read` en el espacio o `platform.audit.read` (auditor, admin, superadmin), RF-51 | El auditor no tiene rol en los espacios |
| D-I14 | Retiro repetido | — | Retirar un retirado sin cambio es idempotente (200, sin evento); indicar el reemplazo después es posible una vez; otro cambio, 409. Publicar de nuevo un documento retirado lo reactiva con una versión nueva | Reintentos seguros y reactivación por el CI |
| D-I15 | Prueba de S1 | `TestNexoTwoAccountsGrantsAreIdempotentAndExtensible` creaba un `nexo_kb_events` ficticio | Usa `nexo_alerts` (S4) como tabla futura | Ahora la migración real crea `nexo_kb_events` |

Nota NX-07 S4 (2026-10-12, `docs/nexo/seguridad/design.md` §8.4): `nexo_kb_events` entra en la cadena
de hashes (columnas `nexo_chain_seq`, `nexo_prev_hash`, `nexo_hash`; trigger `zz_nexo_chain_nexo_kb_events`;
verificación en `GET /nexo/admin/audit/verify`), en la exportación firmada y en la retención por tabla
(`NEXO_RETENTION_KB_EVENTS_DAYS`, mínimo 365, vacía = no se depura). `nexo_kb_immutable()` admite el
único `DELETE` de la depuración segura y solo en `nexo_kb_events`; `nexo_kb_versions` no se depura
nunca. Las denegaciones de espacios alimentan la alerta `authz_denied_burst` con capa `conocimiento`
(vía `nexo_authz_events`, D-I12). La prueba D-I15 usa ahora una tabla ficticia agregada a la lista
mientras dura (`nexo_alerts` ya la crea S4). Una publicación grande retiene el candado de la cadena de
`nexo_kb_events` hasta su commit (las publicaciones concurrentes esperan).

Rendimiento (RNF-K1) con datos sintéticos: ver la tabla de mediciones en `tasks.md` (C-06). Riesgos
abiertos: el peor caso es un término presente en casi todas las secciones con todos los espacios
visibles; se acota con el tope de candidatos, a costa de rankear un subconjunto en esos términos (que
no discriminan). Si la medición con datos reales lo exige, el paso siguiente es una extensión de
índice con ranking (RUM o BM25, §14.7) detrás del mismo `Retriever`.

## 17. Estrategia de recuperación (frente propio)

Decisión (2026-10-12): la calidad de la recuperación es el valor principal del conocimiento para los
desarrolladores y se trata como un frente propio, al mismo nivel que el gobierno y la seguridad. Se
complementa §14 (arquitectura de indexación) con la estrategia completa. Insumos: comparación con
Onyx y Utopia en `nexo_ia/05-investigacion/analisis/Onyx_vs_Utopia_conocimiento.md` y análisis de
WeKnora en `nexo_ia/05-investigacion/analisis/WeKnora_conocimiento.md` (§17.5 a §17.9).

### 17.1 Etapas del pipeline de recuperación

| Etapa | Hoy (2a) | Objetivo |
|---|---|---|
| Corte del contenido | Secciones por títulos, tablas y código sin partir | Tablas y hojas grandes partidas por filas repitiendo la cabecera (de Utopia); título del documento y ruta de títulos antepuestos al texto indexado de cada fragmento (contexto); hash por sección para no recalcular índices de lo que no cambió |
| Comprensión de la consulta | Raíces en español, plegado de tildes, segunda pasada con cualquier término | Expansión con sinónimos y siglas del glosario del espacio (documentos de categoría `glosario`); términos técnicos español/inglés; varias formulaciones enviadas por el asistente y fusionadas |
| Candidatos | Texto (FTS) | Texto + vectores (pgvector, filtro por espacio compatible con HNSW) fusionados por RRF |
| Orden | `ts_rank` y `ts_rank_cd` en dos etapas | Reordenamiento con un modelo multilingüe sobre los 50 mejores |
| Resultado | Lista de secciones con cita | Máximo de secciones por documento (diversidad); secciones vecinas como contexto opcional; umbral de relevancia para responder "no hay estándar oficial" (HU-06), indispensable al agregar vectores; enlace permanente a la fuente en cada cita |
| Medición | 11 preguntas sintéticas | 50 o más preguntas reales por espacio, incluidas preguntas sin respuesta; Recall@5, MRR, nDCG y acierto de "no hay respuesta"; prueba de regresión en CI; señal "útil / no útil" por sección que alimenta el conjunto de evaluación (no el ranking) |

### 17.2 Modelos

Para el volumen previsto (miles a cientos de miles de secciones) se usan modelos abiertos
multilingües ejecutados en el servidor de Nexo, sin GPU obligatoria, de modo que los documentos no
salen de la empresa: embeddings tipo bge-m3 o multilingual-e5 y reordenamiento tipo bge-reranker-v2-m3,
servidos aparte (por ejemplo, un servicio de inferencia en el mismo despliegue) y registrados en
`nexo_kb_index_versions`. Una API externa queda como alternativa opcional, solo con aprobación de la
política de Real Plaza. Esto resuelve la decisión pendiente de §14.7 por defecto.

### 17.3 Orden de implementación

| Paso | Contenido | Condición |
|---|---|---|
| R0 | Conjunto de evaluación real (50+ preguntas): sin respuesta, de término exacto y temporales (`at`, `known_at`). El acierto se compara por ancla de sección y cada corrida se guarda en una tabla. Base: la wiki de arquitectura (§17.8). Prueba de calidad en CI | Con los primeros lineamientos reales. Los devs validan las preguntas borrador |
| R1 | Sin modelos: tablas por filas, contexto del documento en el texto indexado, hash por sección, sinónimos del glosario, diversidad por documento, secciones vecinas, umbral "no hay estándar", enlace permanente, búsqueda exacta y por expresión regular (RC-22), cita textual en `source_location` (IN-07, solo `quote`) | Entrega 2b |
| R2 | Híbrido: pgvector, modelo de embeddings local, RRF | Si R0 muestra fallos en paráfrasis o sinónimos tras R1 |
| R3 | Reordenamiento local sobre los 50 mejores | Si R2 mejora candidatos pero no el orden |
| R4 | Varias formulaciones desde el asistente (herramienta MCP acepta varias consultas) | Con la entrega 3 |
| R5 | Grafo temporal derivado | Solo si quedan preguntas de varios saltos sin resolver |

Cada paso se acepta solo si mejora la medición de R0 sin empeorar el acierto de "no hay respuesta".

### 17.4 Ideas de Onyx y Utopia y vacíos detectados

Se incorporan:

- Entrega 2b: enlace permanente a la fuente en cada cita; evaluación con preguntas sin respuesta;
  columna de hash por sección; tope de seguridad en la poda (no retirar más de un porcentaje del
  espacio por ejecución salvo confirmación explícita); registro de ejecuciones de publicación
  (`nexo_kb_publish_runs`: quién, origen, conteos, errores) para observar cada corrida del CI.
- Etapa de formatos (K2): tablas y hojas por filas con cabecera repetida; conversores pesados fuera
  del proceso del servidor.
- Etapa PDF (K3): detección de PDF escaneado por ausencia de operadores de texto.
- Híbrido (R2/R3): umbral de relevancia; filtro por espacio compatible con HNSW; ciclo de vida del
  índice con cambio atómico de versión.

No se copian: la infraestructura de Onyx (OpenSearch, servidores de modelos, colas), la
sincronización de permisos desde la fuente, los conectores masivos, el decaimiento por antigüedad (un
estándar antiguo vigente sigue valiendo), que la señal de los usuarios mueva el ranking, ni la
extracción de hechos con LLM en la ingesta.

Vacío pendiente de decisión: la etapa K3 preveía adjuntos descargables, pero D-K11 no guarda
originales. Opciones: ofrecer adjuntos solo cuando se active `BlobStore`, o guardar únicamente
diagramas e imágenes pequeñas.

### 17.5 Ajustes de R0 y R1 (acordados con el usuario el 2026-10-13)

Insumo: análisis de WeKnora en `nexo_ia/05-investigacion/analisis/WeKnora_conocimiento.md`. Los
códigos entre paréntesis son los de `docs/nexo/mejoras-y-referencias.md`.

**R0, medición (RC-01, RC-25, RC-28).**

- El acierto se compara por **ancla de sección** (`doc_key` + `anchor`; con `version` si la pregunta
  es temporal), nunca por coincidencia de texto. WeKnora compara por subcadena y cuenta como acierto
  cualquier fragmento que repita una frase.
- El conjunto `eval/preguntas.yaml` incluye cinco clases de pregunta, y las métricas se informan por
  clase:
  - con respuesta;
  - sin respuesta (deben dar `no_results`);
  - de término exacto: nombres de componentes, códigos y siglas;
  - temporales: `at` pasado, `known_at`, documento reemplazado, versión programada y documento
    retirado;
  - de paráfrasis.
- Métricas: Recall@5, MRR, nDCG@3 y nDCG@10, MAP, acierto de "no hay respuesta" y, en las temporales,
  acierto de la versión.
- Cada corrida se guarda en `nexo_kb_eval_runs`:
  - `id`, `ran_at`, `retriever` y `mode`;
  - `index_version` y `config_sha256` (motor, pesos, umbrales, modelos);
  - `question_set_sha256`;
  - métricas totales y por clase, en JSONB;
  - resultado por pregunta, en JSONB;
  - quién la ejecutó y el commit.

  Se guarda en el servidor para ver la evolución entre cambios. La prueba de CI (RC-02) compara contra
  la última corrida aceptada.
- Base del conjunto: la wiki de arquitectura (§17.8). Las preguntas generadas como borrador
  (`status: draft`) solo cuentan cuando 3 o 4 devs las validan (`status: validated`). Un borrador nunca
  es la verdad de referencia.

**R1, sin modelos.** Se agregan dos piezas:

- Búsqueda exacta o por expresión regular (RC-22), descrita en §17.7.
- `quote` en `source_location` (IN-07, solo esa parte): la cita textual de la sección, de 300
  caracteres como máximo, para que el asistente cite sin pedir la sección completa. `source_hash` y
  `partial` quedan para K2.

**Regla del reordenamiento (RC-21).**

- El reordenamiento **nunca rebaja el umbral** de "no hay estándar" ni conserva el mejor resultado
  cuando nadie supera el umbral: en ese caso la respuesta es `no_results: true` (HU-06).
- Si el modelo falla o se excede de tiempo, se devuelve el orden híbrido con `rerank: "skipped"` y el
  motivo.
- Cada respuesta registra el umbral aplicado, el mejor puntaje y cuántos resultados lo superaron (para
  la evaluación).

**Cola de vectorización (RC-20).** Se diseña ahora; se implementa en R2.

| Elemento | Diseño |
|---|---|
| Tabla `nexo_kb_index_jobs` | `id BIGSERIAL`, `index_version`, `space_id`, `document_id`, `section_id`, `op` (`upsert`, `drop`), `body_sha256`, `enqueued_at`, `claimed_at`, `claimed_by`, `fail_count`, `last_error`. Índice único parcial (`index_version`, `section_id`) para los trabajos pendientes, que deduplica |
| Productor | Un proceso por versión de índice activa lee `nexo_kb_changes` desde su último `seq` (guardado en `nexo_kb_index_versions`) y encola las secciones de cada versión publicada, tanto vigentes como históricas (§14.3 regla 1). Una versión de índice nueva encola todo: primero lo vigente, después lo histórico |
| Consumo | `SELECT ... FOR UPDATE SKIP LOCKED LIMIT n` con `claimed_at`. Un trabajo tomado hace más que el umbral de abandono se puede volver a tomar. Funciona con varias réplicas y sin Redis |
| Trabajo | Si ya existe un vector con el mismo `body_sha256` en esa versión de índice, se reutiliza (RC-05). Si no, el vector se calcula por lotes, con reintentos exponenciales. Como las secciones son inmutables, nunca hay trabajo superado por contenido; solo se descarta el de una versión de índice retirada |
| Fallos | Tras 5 intentos, el trabajo pasa a `nexo_kb_index_dead`, con la carga y el error, para volver a encolarlo a mano. Se emite la alerta `kb_index_dead_letter` en `nexo_alerts` (S4) |
| Ventana | Una sección sin vector se encuentra solo por texto. La respuesta híbrida indica `index_lag` (trabajos pendientes del espacio). `engram nexo kb eval --wait-index` espera a que la cola del espacio esté vacía |
| Observación | Pendientes, en curso, fallidos y antigüedad del más viejo, por versión de índice. Una alerta salta si la antigüedad supera un umbral |

**Consulta a Real Plaza (OP-05), antes de R2.** Hay que confirmar con Real Plaza:

- el proveedor y la versión de Postgres de producción;
- si permite `CREATE EXTENSION vector` y en qué versión: se necesita 0.8 o superior por
  `hnsw.iterative_scan`;
- si permite índices con `CONCURRENTLY` en las ventanas de mantenimiento.

La migración de R2 comprueba `pg_available_extension_versions` y, si falta la extensión, deja el
híbrido desactivado sin fallar.

### 17.6 Temporalidad en la recuperación

La bitemporalidad ya está implementada (E2a: `valid_*`, `recorded_*`, `at`, `known_at`, reemplazo;
D-I3). Esto es lo que falta para que el desarrollador la aproveche:

| Código | Qué | Etapa |
|---|---|---|
| RC-25 | Preguntas temporales en R0 (§17.5) | R0 |
| KP-07 | La copia local guarda `valid_from` y `valid_until` de cada sección e incluye las versiones programadas. Filtra por vigencia **al consultar** con el reloj local. Sin conexión no responde reglas vencidas, y una versión programada entra sola en su fecha, aunque no haya sincronización. La respuesta indica "según la copia del <fecha>" | E2b |
| RC-26 | `nexo_search` (MCP y API) acepta `at`. Cada resultado muestra "vigente desde", "vigente hasta", "reemplaza a <documento, versión>" y, si existe, "versión programada desde <fecha>" | E2b |
| KP-08 | Una propuesta puede traer su fecha de entrada en vigencia (`valid_from`, y `valid_until` opcional). Al aprobarla se crea la versión con esa vigencia, y quien revisa la puede ajustar | E2b |
| RC-27 | Consulta "qué cambió desde X": `GET /nexo/kb/v1/changes?since=&until=&spaces=` (también como herramienta MCP). Lista, por espacio y documento, las secciones agregadas, modificadas (misma ancla con otro hash de cuerpo) y retiradas entre dos fechas, calculadas desde las versiones existentes. Respeta los permisos y elige el reloj (vigencia o registro) | R1 |
| KP-09 | Alertas al responsable en `nexo_alerts` (S4): `kb_doc_expiring` (`valid_until` dentro de N días) y `kb_doc_review_due` (sin versión nueva en `settings.review_every_days` del espacio). El evaluador de S4 las calcula; no cambian el contenido | E2b / K6 |
| RC-19 | En R2, el filtro temporal tiene el mismo problema que el filtro por espacio con HNSW: lo vigente hoy es una fracción de la tabla y una consulta con `at` lo es todavía más. La consulta vectorial usa el mismo patrón (`ef_search` ampliado, `iterative_scan`) y un índice parcial `WHERE is_current` para el caso por defecto. Las consultas históricas usan recorrido exacto cuando el conjunto filtrado es pequeño. La prueba de recall cubre ambos filtros | R2 |
| KP-04 | Una contradicción entre una memoria de proyecto y un estándar se evalúa contra la regla **vigente en la fecha de la decisión** (`at` = fecha de la memoria) y contra la de hoy, e informa los dos casos: "contradecía cuando se decidió" y "contradice hoy" | Después de R2 |
| RC-18 | El grafo temporal (R5) queda al final; solo si R0 muestra preguntas de varios saltos sin resolver | R5 |

### 17.7 Puntos de extensión: adaptadores, modos y modelos

Tres familias de extensiones. Cada una se registra por nombre, se activa por configuración (variable
de entorno del servidor o `settings` del espacio) y se acepta solo si mejora la medición de R0 sin
empeorar el acierto de "no hay respuesta" (§17.3). La configuración activa queda en
`nexo_kb_index_versions.config` y en cada corrida de `nexo_kb_eval_runs`.

**(a) Adaptadores de fuente y conversores.** Contrato común: bloques con procedencia (§15.1).

| Pieza | Contrato | Implementaciones |
|---|---|---|
| `Source` (de dónde salen los documentos) | `Walk(ctx) -> []SourceDocument{DocKey, Title, PathPrefix []string, Format, Bytes, Origin, SourceURL, Hints}`. `Hints` lleva el estado sugerido: retirado, vencido o vacío | `dir` (directorio de un repositorio, el actual), `azure-devops-wiki` (IN-08), `proposal` |
| `Converter` (cómo se lee un formato) | `Convert(ctx, bytes, meta) -> {Markdown, Blocks, Meta}` con `Location` por bloque; se registra en `ingest.Registry` por tipo MIME o extensión (§15.2) | `markdown`, `text` (hoy); `ado-markdown`, `html` (tablas HTML a Markdown, sin ejecutar nada); `docx`, `xlsx` (K2); `pdf`, `pptx` (K3) |
| Paso común | Detector de secretos (SG-09), límites de §15.2, `Sectioner` | Igual para todas las fuentes |

`Source` y `Converter` son independientes: la wiki de Azure DevOps es una fuente cuyos archivos
pasan por el conversor `ado-markdown`, y un `.docx` subido a mano usa la fuente `dir` con el
conversor `docx`. Configuración en `nexo-kb.yaml`: `source: azure-devops-wiki`, organización,
proyecto y wiki para construir `SourceURL` (RC-08).

**(b) Modos de búsqueda.** Todos van detrás de `Retriever` y conservan los filtros comunes
obligatorios (§14.3 regla 4).

| Modo | Qué hace | Etapa |
|---|---|---|
| `fts` | FTS en español en dos etapas (actual, D-I2..D-I5) | Hecho |
| `exact` | Literales o expresión regular (RE2 de Go, tiempo lineal, sin retroceso): extrae los literales, preselecciona con FTS `simple` o `ILIKE` sobre las secciones visibles y filtra con la expresión. Pensado para componentes, códigos y siglas (RC-22) | R1 |
| `hybrid` | `fts` + vectores con RRF, umbral y diversidad (RC-11, RC-19) | R2 |
| `changes` | No ordena por relevancia: lista diferencias entre fechas (RC-27). Usa una interfaz aparte, `ChangeLister`, con los mismos filtros | R1 |

Parámetro `mode` en la API y en `nexo_search`; el valor por defecto se configura por espacio.

**(c) Modelos intercambiables.**

- `Embedder{Model(), Dim(), Embed(ctx, []string)}` y `Reranker{Model(), Score(ctx, query, passages)}`.
- La implementación por defecto es local: un servicio de inferencia en el mismo despliegue (§17.2).
- Una API externa exige `NEXO_KB_ALLOW_EXTERNAL_MODELS=1`, se registra en `nexo_kb_index_versions` y
  deja un evento (los documentos salen de la empresa). Solo con aprobación de la política de Real
  Plaza.

### 17.8 Corpus real para R0: wiki de arquitectura (Azure DevOps)

Ubicaciones:
- Copia de trabajo, sin `.git`: `nexo_ia/06-pruebas/conocimiento/corpus/Wiki-Arquitectura.wiki`.
- Original con historial, de solo lectura: `Documents/Wiki-Arquitectura.wiki`. Tiene unos 4 300
  commits, de 2021-11 a 2026-09, de 12 autores.

Perfil medido:

| Rasgo | Cantidad | Tratamiento (IN-08) |
|---|---|---|
| Páginas `.md` | 232, de las cuales 33 están vacías (páginas contenedoras) | Las vacías se saltan con aviso en el resultado del lote |
| Palabras | Unas 180 000 | — |
| `[[_TOC_]]` | 150 páginas | Se quita |
| Tablas Markdown | 66 páginas | Se parten por filas con la cabecera repetida (RC-04) |
| `<table>` HTML | 12 páginas | Se convierten a Markdown (`html`), sin interpretar nada más |
| Bloques de código | 127 páginas | Sin partir |
| `::: mermaid` | 1 | Se trata como bloque de código `mermaid` |
| Imágenes en `/.attachments` | Unas 1 100 en 133 páginas, más 29 zip y 1 pdf | Se conserva la referencia; no se indexan hasta IN-05; los zip se ignoran con aviso |
| Nombres de archivo codificados | `%2D`, `%7C`; el guion equivale a un espacio | El título sale del nombre decodificado |
| Jerarquía | Carpeta con página homónima; orden en `.order` | La carpeta es la ruta de títulos (`PathPrefix`) y `.order` fija el orden |
| Títulos numerados (`1.2.1.`) | Frecuentes | La numeración se quita del ancla y se conserva en el título |
| Carpetas `[TO_REMOVE]` (39), `≤-2022` (34), `[TO_TIDY]` (3) | Obsoletas o por ordenar | Se publican y quedan retiradas o vencidas (KP-11), no se omiten |
| Credenciales de demostración | En unas 15 páginas | Caso de prueba de SG-09. Nunca se copian ni se citan en documentos ni en preguntas |

Uso en R0:

- Las preguntas borrador se generan desde este corpus y se marcan `draft` hasta que 3 o 4 devs las
  validen.
- Las preguntas temporales y de cambios se arman con versiones nuevas de algunas páginas (carpeta
  `eval/overlay`, cargada después de la marca `@base`) y con las carpetas `≤-2022` (vencidas). La
  importación del historial git de la wiki (KP-10) se **descartó** el 2026-10-13: el flujo real es
  subir archivos sueltos; el historial de la wiki no es requisito y obligaba a decidir otro reloj de
  registro.

### 17.9 Ajustes de requisitos que esta sección implica (resueltos el 2026-10-13)

| Requisito | Conflicto | Resolución |
|---|---|---|
| RF-17 ("sin partir tablas") | RC-04 parte las tablas largas por filas | **Reformulado** en requirements: "sin partir filas de tabla ni bloques de código; una tabla mayor que el tamaño objetivo se parte por filas repitiendo la cabecera". Implementado (`SplitTables`, D-K2b-6) |
| RF-40 (copia local con "secciones vigentes") y §6.4 (feed con la versión actual) | KP-07 necesita también las versiones programadas y las vigencias | **Ajustado**: el feed `/sync` entrega lo vigente y lo programado con sus vigencias y la copia filtra al consultar (feed implementado en la tanda 1; la copia local, en la tanda 2) |
| RN-K4 (el servidor rechaza) y HU-17 c1 ("se omite o se enmascara") | SG-09 propone rechazar o enmascarar | **Decisión pendiente del usuario.** Se mantiene el comportamiento actual (rechazar). La detección se amplió (D-K2b-14) sin cambiar la política |
| §14.4 `recorded_from` = publicación en Nexo | KP-10 importaría versiones con las fechas reales de la wiki | **Sin efecto**: KP-10 descartado; `recorded_from` sigue siendo la publicación en Nexo |
| Requisitos §10 (grafo fuera de alcance) frente a R5 y §14.6 etapa 6 | Contradicción ya existente | **Resuelto**: el grafo temporal es la **etapa final** de la escalera (R5), no fuera de alcance; requirements §9 y §10 actualizados |
| Requisitos §9 K5 ("búsqueda semántica") frente a R2/R3 | Dos nombres para lo mismo | **Unificado**: K5 = R2 (híbrido) + R3 (reordenamiento) |
| Requisitos §9 K4 (alcance de tokens, NX-06) frente a SG-08 | Se solapan | **Resuelto**: SG-08 queda dentro de K4 |
| Rama de trabajo | Los documentos decían `feat/conocimiento` | **Corregido**: la rama real es `feat/conocimiento-nucleo` |

## 18. Entrega 2b, tanda 1: implementación y desviaciones

Fecha: 2026-10-13. Rama `feat/conocimiento-nucleo` (sobre `develop` ab6f04c, con S1..S4), sin commit.
Decisiones del usuario del 2026-10-13 que mandan sobre las secciones anteriores: (1) el responsable
sube archivos sueltos o una carpeta; git es opcional y la plantilla de GitHub Actions pasa a la tanda
2; (2) una sola API de subida para la CLI y el panel; (3) no se guardan originales (D-K11): mejorar un
conversor exige **volver a subir** el archivo; (4) KP-10 descartado; (5) política de secretos
pendiente; (6) ajustes de §17.9.

### 18.1 Qué se implementó

| Bloque | Implementación |
|---|---|
| Subida (HU-29) | `POST /nexo/kb/v1/spaces/{slug}/uploads` (multipart; campos `file`/`zip` y opciones `dry_run`, `prune`, `confirm_prune`, `source`, `collection`, `wiki_url`, `category`, `owner`, `tags`, `change_note`). La autorización (`space.publish` con token write) se decide **antes** de leer el cuerpo. Pipeline: lectura segura (`source.ReadZip`, `CleanPath`, `Sniff`) -> fuente (`dir` o `azure-devops-wiki`) -> conversores -> `publishAll` (el mismo de los lotes) -> `nexo_kb_publish_runs` |
| Defensas | Cuerpo 256 MB (`MaxBytesReader`; zip a archivo temporal, borrado al terminar); 20 MB por archivo; 64 MB descomprimido indexable; 10 000 entradas; relación 100:1 en entradas > 1 MB; tamaño declarado y leído; solo se descomprimen las entradas indexables; zip-slip (rutas absolutas, `..`, unidades, control); entradas cifradas y no regulares se saltan; tipo por contenido (UTF-8 sin NUL para texto, `PK` para docx); límite de peticiones del grupo `nexo` |
| CLI | `engram nexo kb upload` (archivos, carpeta comprimida en memoria con su nombre como raíz, o zip), `changes`, `reindex`, `eval` ampliado, `search --mode` |
| Panel | `/dashboard/kb` (espacios, crear espacio si `platform.scope.create`), `/dashboard/kb/spaces/{slug}` (documentos, subida si `space.publish`, ejecuciones), resultado de la subida, documento con versiones y secciones ancladas (destino de los enlaces de las citas), retirar. Sesión del panel revalidada; CSRF; alcance del token heredado |
| Word | `ingest/docx`: estilos de título en inglés y español y `outlineLvl`, párrafos, listas con numeración, tablas a GFM, procedencia `paragraph_start/end` y `table`, `row_start/end`; límites de entradas, tamaño, relación y elementos XML; sin dependencias nuevas |
| Wiki de Azure DevOps (IN-08) | Ver §17.8 y D-K2b-3..5 |
| Propuestas (C-07, KP-08) | Crear (`space.propose`), listar (propias; todas las del espacio con `space.review`), leer (autor o revisor; 404 para el resto), aprobar con conflicto de versión 409 salvo `force` y vigencia ajustable, descartar con motivo, retirar (autor). La aprobación publica en la misma transacción que cierra la propuesta, con origen `proposal`, autor y aprobador |
| Feed `/sync` (C-09, KP-07) | Espacios visibles con rol; cambios desde `seq` con el estado final de cada documento (versión actual y secciones vigentes **y programadas** con su vigencia) o `remove`; `has_more`, `resync_required` |
| Qué cambió (RC-27) | `GET /changes?since=&until=&spaces=&clock=valid|recorded`: secciones agregadas, modificadas (misma ancla, otro hash) y retiradas, con fecha y enlace |
| Avisos (KP-09) | Reglas `kb_doc_expiring` (ventana de 30 días o `settings.expiry_warn_days`) y `kb_doc_review_due` (`settings.review_every_days`) en el evaluador de S4, capa `conocimiento`, una alerta por documento y estado |
| Poda (KP-01) y ejecuciones (KP-02) | Tope del 25 % (más de 5 documentos) sin `confirm`; `nexo_kb_publish_runs` y `GET /spaces/{slug}/publish-runs` |
| Enlaces (RC-08) | `link` permanente (panel, versión fija, ancla) en cada resultado y cambio; `source_url` del front matter o de la wiki |
| R0 | Ver §18.3 |
| R1 | `NEXO_KB_R1`; RC-03, RC-04, RC-05, RC-06, RC-07, RC-09, RC-10, RC-22, IN-07 (`quote`); reindexado seguro (`admin/index:reindex`) |

### 18.2 Desviaciones

| ID | Tema | Diseño | Implementado | Motivo |
|---|---|---|---|---|
| D-K2b-1 | Ruta de subida | §7 solo tenía `documents:batch` | `POST /spaces/{slug}/uploads` multipart, que reutiliza la publicación por lotes sin el tope de 200 por pedido (tope propio de 2 000 archivos) | Decisión del usuario: una API para CLI y panel |
| D-K2b-2 | Referencia de la poda en subidas | `origin_ref` = nombre de archivo | `Origin.Source` = colección (nombre del zip o de la carpeta, o `collection`); la poda de subidas usa esa referencia | Un archivo suelto no identifica el conjunto que se poda |
| D-K2b-3 | `doc_key` de archivos subidos | Ruta relativa sin extensión | Cada segmento de la ruta pasa a slug (sin tildes, minúsculas, `≤-2022` -> `2022`, `[TO_REMOVE]` -> `to-remove`), recortado a 200 con sufijo estable | Las rutas reales (tildes, `%7C`, corchetes) no son `doc_key` válidos; el front matter `id` sigue mandando |
| D-K2b-4 | Título de una página de la wiki | El front matter o el primer `#` | El nombre de archivo decodificado (`%2D`, `%7C`, guion = espacio) manda sobre el primer título | En la wiki el primer `#` suele ser "1. Descripción" |
| D-K2b-5 | Carpetas `[TO_REMOVE]` y `≤-AAAA` (KP-11) | Publicar retiradas o vencidas | `[TO_REMOVE]`: se publican y se retiran en la misma transacción (`recorded_from = recorded_until`: nunca rigieron en Nexo); `≤-AAAA`: vigentes desde `2021-11-01` (inicio de la wiki, convención) hasta el 1 de enero de `AAAA+1`, encontrables con `at` en esas fechas. Reenviarlas es idempotente | Que se vean como no vigentes sin perderlas |
| D-K2b-6 | Tablas por filas (RC-04) | En el corte del `Sectioner` | Opción `SplitTables`: una tabla mayor que el tamaño objetivo se parte en bloques que repiten cabecera y separador, con `line_start/line_end` o `row_start/row_end` por parte; las partes son `-p2`, `-p3`... | Mismo mecanismo de partes que 2a; el evaluador acepta el prefijo del ancla |
| D-K2b-7 | Anclas | Slug de los títulos | Además se quita la numeración del título ("1.2.1. ", "3) ") en el ancla (no en el título), en todos los formatos | IN-08; renumerar no cambia el ancla. Cambia las anclas de títulos numerados en versiones nuevas de documentos de 2a |
| D-K2b-8 | Índice de una sección enorme | — | El `tsvector` se calcula sobre los primeros 256 KB del cuerpo (el cuerpo se guarda completo) | Error real de 2a con la wiki: un `tsvector` > 1 MB hacía fallar la publicación |
| D-K2b-9 | Umbral "no hay estándar" (RC-07) | Umbral de relevancia | Cobertura ponderada por rareza: cada palabra de la consulta (con sus sinónimos) pesa ln(1 + N/(df+1)) en las secciones vigentes de los espacios visibles; un resultado por debajo de `NEXO_KB_MIN_COVERAGE` (0,3) se descarta; sin resultados, `no_results`. Lo aplica el `Searcher` (común a todo motor que informe cobertura) en el modo `fts`; la respuesta trae `relevance` (umbral, mejor cobertura, encima y debajo, RC-21) | El puntaje de `ts_rank` no es comparable entre consultas. Costo medido: falsos "no hay" en paráfrasis (§18.3) |
| D-K2b-10 | Sinónimos (RC-06) | Expansión de la consulta | Diccionario base ES/EN y glosario del espacio (líneas "TÉRMINO (SIGLA):", "SIGLA: nombre", "A = B"), en caché 5 min, **solo en la segunda pasada** (cualquier término) | No estrechar una búsqueda que ya encuentra todas las palabras |
| D-K2b-11 | Diversidad (RC-09) | MMR | Tope simple de 2 secciones por documento (`NEXO_KB_MAX_PER_DOC`); el Searcher pide el triple de candidatos | Sin modelos; medido como mejora en temporales |
| D-K2b-12 | `quote` (IN-07) | En `source_location` | Se calcula al consultar (300 caracteres sin la línea de título) y se agrega a `source_location` de la respuesta; no se guarda | Vale para las secciones de 2a sin reindexar |
| D-K2b-13 | Modo `exact` (RC-22) | FTS `simple` o `ILIKE` + filtro | `ILIKE` sobre el literal (o el literal más largo obligatorio de la expresión, mínimo 3 caracteres) en las secciones visibles con los filtros comunes, 2 000 candidatos, filtro RE2 en Go, orden por apariciones | La expresión del usuario nunca llega a Postgres. A gran escala `ILIKE` recorre las secciones del espacio: si hace falta, índice trigram detrás del mismo modo |
| D-K2b-14 | SG-09 | Ampliar la detección | Se agregaron `Password=`/`Pwd=` **dentro de una cadena de conexión** (otra clave como `Server=` o `User Id=` en la línea), `Bearer <token>` y `api-key:`; con marcadores de ejemplo excluidos. No se agregaron pares usuario/contraseña en prosa ni control de entropía. La política no cambia (rechazo) | La versión sin contexto marcaba código como `Password = request.Password` (5 páginas de la wiki); medido: 3 rechazos en la wiki (antes 1) |
| D-K2b-15 | Evento de una ejecución | — | `publish.run` en `nexo_kb_events` solo si la ejecución cambió algo y no es dry-run; la fila de `nexo_kb_publish_runs` se escribe siempre | RF-11: reenviar sin cambios no deja eventos |
| D-K2b-16 | Panel | Pantallas (K6) | Páginas propias registradas desde `nexo_kb_bridge.go` con HTML escapado sobre `dashboard.Layout`; **sin pestaña nueva** en la navegación (evita regenerar `components_templ.go`, que cambiaba ~400 líneas por renumeración); CSRF = HMAC de la cookie con la clave de sesiones del panel, en el formulario o (subida multipart) en la consulta, más comprobación de `Origin`; CSP estricta en estas páginas | Diff mínimo en Engram; la subida multipart no puede leer el formulario antes de validar |
| D-K2b-17 | Feed `/sync` | Cambios desde `seq` | Un documento por página con su estado final; `seq` avanza al máximo del feed cuando no hay más; `resync_required` solo si `since` supera el máximo (no hay compactación todavía) | Riesgo conocido: un cambio con `seq` menor confirmado después de leer el máximo podría saltarse; la copia local de la tanda 2 debe releer una ventana de solape |
| D-K2b-18 | "Qué cambió" | Elige el reloj | `clock=valid` (por defecto): lo que regía en cada fecha según lo publicado hoy; `clock=recorded`: lo publicado y vigente en cada fecha. Una versión con `valid_from` declarado en el pasado no es un cambio en el reloj de vigencia | Coherente con la bitemporalidad de D-I3 |
| D-K2b-19 | RF-34 (descartadas parecidas) | El asistente consulta descartes del espacio | No implementado | Contradice "un proponente no ve propuestas ajenas" (decisión previa) |
| D-K2b-20 | Reindexado (RC-03) | Indexadores alimentados por el feed | Reindexado síncrono por API (`platform.settings`) en lotes de 50 documentos con transacciones cortas, versión `building` -> `active`; no automático | Suficiente para el volumen actual; la cola de R2 (§17.5) lo reemplaza |
| D-K2b-21 | Registro de corridas (RC-28) | — | `POST /eval-runs` exige `space.publish` en cada espacio medido (o `platform.audit.read` con token write); `GET /eval-runs` para quien ve algún espacio | Quien mantiene el conocimiento acepta la referencia del CI |

### 18.3 Medición R0 y R1 (wiki de arquitectura, 50 preguntas borrador)

Conjunto: `nexo_ia/06-pruebas/conocimiento/eval/preguntas-wiki.yaml` (BORRADOR, a validar por 3 o 4
devs), 15 normales, 8 de término exacto, 7 de paráfrasis, 8 sin respuesta, 8 temporales y 4 de
cambios. Carga: 161 documentos vigentes (35 retirados de `[TO_REMOVE]`, 33 páginas vacías saltadas,
3 rechazadas por secretos), unas 2 150 secciones; las preguntas temporales y de cambios usan 4
páginas con versión nueva cargadas después de `@base`.

| Clase | Línea base Hit@5 / MRR | R1 Hit@5 / MRR |
|---|---|---|
| normal (15) | 46,7 % / 0,255 | 80,0 % / 0,522 |
| exact (8) | 87,5 % / 0,875 | 100,0 % / 0,938 (modo exact) |
| paraphrase (7) | 28,6 % / 0,119 | 0,0 % / 0,000 |
| no_answer (8): acierto | 0 % | 50 % |
| temporal (8) | 62,5 % / 0,601 | 100,0 % / 0,817 |
| changes (4) | 0 % (no existía) | 100 % |
| total con respuesta | 50,0 % / 0,392 (nDCG@10 0,430, MAP 0,386) | 76,2 % / 0,616 (nDCG@10 0,654, MAP 0,618) |

Aporte de cada mejora (todas las demás activas, umbral 0,3): sin `doc_context` 73,8 %; sin
`synonyms` 69,0 %; sin `diversity` 73,8 %; sin `split_tables` 76,2 % (neutra en este conjunto: se
mantiene porque es requisito, RF-17 y HU-29 c2). El umbral sube el acierto de "no hay respuesta" de 0
a 50 % y deja 3 falsos "no hay" y la clase paráfrasis en 0 %: es exactamente el caso que R2 (vectores)
debe resolver; según la regla de §17.3, R0 ya muestra la condición para pasar a R2. Detalle y tablas
por mejora en `nexo_ia/06-pruebas/conocimiento/RESULTADOS.md`.


## 19. Entrega 2b, tanda 2: cliente Nexo (copia local, MCP, CLI) y kit

Fecha: 2026-10-13. Rama `feat/conocimiento-nucleo` (sobre `8984928`), sin commit. Cierra el flujo:
el admin crea el espacio, el responsable sube documentos (tanda 1) y el dev consulta desde su
asistente (herramientas MCP `nexo_*`) o la terminal, con cita y enlace, sin inventar cuando no hay
estándar, respetando vigencias y funcionando sin red con la copia local.

### 19.1 Qué se implementó

| Bloque | Implementación |
|---|---|
| D-01 Copia local | `internal/nexo/kb/client`: `Local` (SQLite propio con FTS5 `unicode61 remove_diacritics 2`, BM25 con pesos título 5, ruta de títulos 3, cuerpo 1) en `NEXO_KB_LOCAL_DB`, `$NEXO_HOME/kb.db` o `~/.nexo/kb.db`; nunca toca `engram.db`. `Syncer` consume `GET /nexo/kb/v1/sync` por páginas: borra los espacios que ya no están en `visible_spaces` (RF-41), reemplaza cada documento por su estado final (vigentes y programadas con su vigencia) o lo borra, guarda el cursor por página. La vigencia se aplica al consultar con el reloj local o `at` (KP-07): nunca devuelve lo vencido ni lo que aún no rige |
| D-02 MCP | `nexo_search`, `nexo_get`, `nexo_spaces`, `nexo_changes`, `nexo_propose`, `nexo_proposals`, `nexo_review`, `nexo_whoami` (`client/mcp.go`). Un solo enganche `// NX-10` al final de `registerTools` (más su import). Cada resultado trae `status`, `citation`, `link`, `source_url`, `quote` y una `guidance` para el asistente; sin estándar, `no_results` y el mensaje literal de HU-06. Primero el servidor; si no responde, la copia local marcada (`source: local_copy`, `notice` con fecha y antigüedad) |
| D-03 CLI | `engram nexo kb get`, `sync`, `search --local`, `eval --retriever local-bm25`, `import-transversal` (despachador `nexocli` existente; sin enganches nuevos en `main.go`) |
| D-04 | Plantilla `docs/nexo/conocimiento/github-actions/` (`publicar-conocimiento.yml` y `LEEME.md`): `upload --dry-run` en PR, `upload` al hacer merge a `main`, token como secreto `NEXO_KB_TOKEN`; soporte a futuro, no es el flujo principal (D-K12) |
| F-04 | **Superado por §19.4** (la instalación y la skill viven en el fork). Primero se hizo en el kit `nexo_ia/02-nexo-kit` v0.3.0, hoy histórico: skill `nexo-protocol` (lo transversal por `nexo_*`, cita siempre, "no hay estándar", `at`/`known_at`, "qué cambió", propuestas con confirmación; `nexo-transversal` como respaldo de solo lectura), plugin con las 8 herramientas en `--tools`, hook de inicio que ejecuta `engram nexo kb sync` (tolerante), instalador y README |
| Servidor (aditivo) | `/sync` agrega `public_url` y `source_url` por sección; la lectura de una sección agrega `link` y `source_url`. Sin migraciones nuevas |

### 19.2 Desviaciones

| ID | Tema | Diseño | Implementado | Motivo |
|---|---|---|---|---|
| D-K2b-22 | Ventana de solape (riesgo D-K2b-17) | Cursor `seq` | El cliente guarda puntos de control (seq, hora) y relee desde el seq del punto más nuevo anterior en `NEXO_KB_SYNC_OVERLAP_SECONDS` (120 s) a la última sincronización; si todavía no existe ese punto, relee los últimos 1 000 cambios. Aplicar un documento es idempotente | Un cambio con `seq` menor confirmado después de leer el máximo se vuelve a leer en la sincronización siguiente. Supuesto: ninguna transacción de publicación tarda más que la ventana entre recibir su `seq` y confirmarse |
| D-K2b-23 | Espacio que se vuelve visible | Feed por `seq` | Si `visible_spaces` trae un espacio que la copia no tenía, se relee el feed desde 0 una vez | Sus documentos tienen `seq` anteriores al cursor; el feed no filtra por espacio |
| D-K2b-24 | Búsqueda local | FTS5 BM25 | Términos sin palabras vacías (español e inglés), por prefijo con recorte de sufijos ("validaciones" -> `validacion*`); primero todos los términos y luego cualquiera (como D-I4); umbral de cobertura ponderada por rareza calculada sobre la copia (`NEXO_KB_MIN_COVERAGE`) y tope por documento (`NEXO_KB_MAX_PER_DOC`); modo `exact` (literal o RE2). Sin sinónimos del glosario ni `known_at` (la copia no guarda el historial de publicación: se dice) | La copia no tiene el lematizador de Postgres. La calidad local se mide con `eval --retriever local-bm25` (§14.5) |
| D-K2b-25 | Identidad de la copia | — | Huella `sha256(servidor + token)` truncada (nunca el token): si cambia la persona o el servidor, la copia se reconstruye | Una copia pertenece a una persona |
| D-K2b-26 | Registro de herramientas | `RegisterTools(srv, cfg, allowlist)` | `RegisterTools(srv, s, allowlist)`; se registran si no hay allowlist, si su nombre está en ella o si incluye `nexo` (`--tools=agent,nexo`), y solo con servidor y token configurados; `NEXO_KB_MCP=off` las apaga. Un pánico interno no tumba el MCP | El perfil `agent` de Engram no puede incluir nombres que el Engram oficial no conoce; el kit usa la lista explícita |
| D-K2b-27 | Herramientas | Seis en §6.5 | Ocho: se agregan `nexo_changes` (HU-38) y `nexo_whoami`. "Sin conexión" = error de red, tiempo agotado (`NEXO_KB_TIMEOUT_SECONDS`, 8 s) o 502/503/504; un 401/403/404/422 no cae a la copia (el servidor respondió) | HU-38 y diagnóstico (`estado` de la skill) |
| D-K2b-28 | Sincronización | Al iniciar el MCP, por hook y cada 10 min | Igual (`NEXO_KB_SYNC_MINUTES`), más una sincronización en segundo plano cuando el servidor vuelve a responder tras estar caído o la copia tiene más de 10 minutos; nunca bloquea la respuesta. `engram nexo kb sync` termina en 0 sin red (para los hooks) | CA-K10 (al reconectar se actualiza sola) |
| D-K2b-29 | Migración de `nexo-transversal` | Publicar Oficial, listar Propuestas | `import-transversal` lee `engram.db` en solo lectura (`mode=ro`) o un `engram export` (`--from`); Oficial -> documento por lote (origen `import`, `doc_key` `transversal/<topic_key>`, categoría desde **Categoría**, vigencia desde **Vigente desde**, responsable); Propuesta (o sin Estado) -> propuesta del espacio, idempotente por `doc_key` entre las propias; Obsoleto y otros se omiten. `--dry-run` no escribe nada | Las propuestas quedan a nombre de quien ejecuta la migración (la autoría original queda en la sección "Procedencia" del texto); el autor original no tiene token en ese momento |
| D-K2b-30 | Cliente HTTP | En `client` (2b) | El cliente mínimo de `cli` pasó a `client.API` (`cli.Client` es un alias) | Una sola implementación para CLI y MCP |

### 19.3 Riesgos

- Si en la PC que corre las pruebas de Engram están definidos `ENGRAM_CLOUD_SERVER` y `ENGRAM_CLOUD_TOKEN`,
  el MCP de las pruebas registraría también las herramientas `nexo_*` (y cambiaría la cuenta de
  herramientas). Mitigación: `NEXO_KB_MCP=off` al correr esas pruebas. En CI no están definidas.
- El binario oficial de Engram no tiene `nexo_*`: el kit depende de distribuir el binario del fork
  (OP-01).
- La copia local guarda texto de documentos oficiales en la PC (como antes `nexo-transversal`); se
  borra al perder acceso al espacio en la siguiente sincronización, no antes.

### 19.4 Cambio de alcance (decisión del usuario, 2026-10-13): la instalación vive en el fork

La skill y la instalación de Nexo dejan el kit externo (`nexo_ia/02-nexo-kit`, que queda como
histórico) y pasan al fork. Instalación para un dev, igual en Windows, Mac y Linux:

1. Instalar el binario `engram` del fork (`3.4.0-nexo.N`; la base es Engram v3.4.0 desde el 2026-10-13).
2. `engram nexo connect --server https://<servidor-nexo> --token-stdin` (o `NEXO_TOKEN`, o `--token`).
3. `engram setup <agente>` (claude-code, codex, cursor, windsurf, ...). Si ya estaba configurado:
   `engram nexo setup <agente>`.
4. Reiniciar el asistente. Comprobar con `engram nexo status`; quitar con `engram nexo disconnect`.

| ID | Tema | Implementado | Motivo |
|---|---|---|---|
| D-K2b-31 | Conexión | `engram nexo connect` valida servidor y token con `GET /nexo/authz/v1/me` (401, 403 y 404 dan mensajes claros y no guardan nada), guarda servidor y token en `cloud.json` de Engram (0600, el mismo archivo de `engram cloud config`: el servidor Nexo es el Engram Cloud del equipo), escribe el marcador `nexo.json` en la carpeta de datos (servidor, persona, alcance; sin el token) y hace la primera sincronización de la copia local. `status` informa conexión, identidad, servidor y copia; `disconnect` borra el marcador, el token y la copia (`--keep-copy` la conserva). El token nunca se imprime | Reemplaza la parte Nexo de `Install-Nexo.ps1` y funciona en las tres plataformas |
| D-K2b-32 | Registro de las herramientas | Además de D-K2b-26: con la PC conectada (`nexo.json`) se registran con el perfil `agent` (el que escribe `engram setup`: `--tools=agent`) o sin allowlist. Sin conexión, un Engram con nube que no es Nexo no las ve | `engram setup` escribe `--tools=agent` en todos los agentes; cambiar cada escritor de MCP habría tocado muchos archivos de Engram |
| D-K2b-33 | Protocolo en `engram setup` | Un enganche `// NX-10` en `setup.Install` llama a `nexoAfterInstall` (`internal/setup/nexo_setup.go`). **Decisión:** solo instala si la PC está conectada; sin conexión `engram setup` queda igual que en Engram. Destinos: claude-code -> `~/.claude/skills/nexo-protocol/SKILL.md` (respeta `CLAUDE_CONFIG_DIR`); codex -> `~/.codex/skills/` y `~/.agents/skills/`; opencode -> `~/.config/opencode/skills/`; pi -> `AGENTS.md` de Pi; gemini-cli y antigravity -> bloque en `~/.gemini/GEMINI.md`; windsurf, qwen, kiro, kilocode, commandcode, kimi -> bloque administrado (`<!-- BEGIN NEXO PROTOCOL (NX-10) ... -->`) en el mismo archivo de reglas que usa Engram, sin tocar el bloque de Engram; cursor -> `~/.cursor/nexo-protocol.md` para pegar como User Rule (como hace Engram); vscode-copilot -> `prompts/nexo.instructions.md` con `applyTo: "**"`. Un error al instalar el protocolo no hace fallar `engram setup` (aviso en stderr) y se instala aunque falle un paso propio de Engram (por ejemplo, sin el CLI de claude) | Coexistir con lo de Engram sin cambiar su comportamiento; el orden recomendado es conectar primero |
| D-K2b-34 | Fuente única del texto | `internal/nexo/setup/SKILL.md` (embebido con `go:embed`); `go generate ./internal/nexo/setup/` copia a `plugin/claude-code/skills/nexo-protocol/SKILL.md` y `plugin/codex/skills/nexo-protocol/SKILL.md`; una prueba falla si divergen. Los archivos de reglas reciben el mismo texto sin front matter | Que no diverja |
| D-K2b-35 | Marketplace del plugin de Claude Code y de Codex | **Resuelto con la opción A (decisión del usuario, 2026-10-13).** `claudeCodeMarketplace` y `codexMarketplace` apuntan a `Adriel2503/engram-nexo` Desde el 2026-10-13 los dos instalan desde **`main`**, la rama estable de Nexo: la rama por defecto de GitHub es `main` (Claude Code la toma de ahí) y Codex usa `--ref main`. `develop` es la rama de trabajo y `engram-upstream` la copia del Engram original. Un cambio de la skill llega a los devs solo al pasar a `main`. El plugin `engram` del fork trae `skills/memory` y `skills/nexo-protocol`, así que para Claude Code y Codex `engram setup` ya no copia la skill aparte y borra copias directas anteriores. Verificado con configuraciones temporales de Claude Code y Codex contra el repositorio privado (clonado por HTTPS con las credenciales de git). Opción B, para producción: embeber el plugin en el binario y registrarlo como marketplace local, sin acceso al repositorio | Cada persona de la demo necesita acceso de lectura al repositorio privado y credenciales de git; las mejoras del plugin de Engram llegan al actualizar el fork desde Engram. En Windows el plugin de Claude Code exige `jq` y `curl` en el PATH |

Pruebas: `internal/setup/nexo_setup_test.go` (sin conexión no se toca nada; Windsurf con los dos
bloques e idempotente; Cursor y VS Code con archivo propio; todos los agentes con destino;
skill en claude-code y codex; `Install("claude-code")` deja la skill aunque falle el CLI; copias del
plugin iguales a la fuente), `internal/nexo/nexocli/connection_test.go` (token inválido no guarda
nada; token válido guarda sin el token en `nexo.json`, sincroniza; `status` con y sin servidor;
`disconnect`), `client/local_test.go` (`TestEffectiveAllowlist`). E2E T2-21..T2-26.
