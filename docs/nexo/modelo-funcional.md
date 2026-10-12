# Modelo funcional: Engram operando como Utopia

> **Nota (2026-10-12).** Las secciones 3 a 8 (dos cajas como proyectos y niveles `read/propose/write`)
> quedan reemplazadas por `docs/nexo/roles/` (roles de plataforma y roles únicos para proyectos y
> espacios, NX-05) y `docs/nexo/conocimiento/` (espacios de conocimiento, NX-10). Se conservan como
> antecedente; la sección 9 (Engram propio y vigencia) sigue vigente.

Estado: borrador para revisión. Fecha: 2026-10-12.
Base: Engram v3.3.2 (fork engram-nexo). Referencia: Utopia rc9 (`repository/utopia`).

## 1. Objetivo y alcance

Que Nexo (Engram + skill + plugin) se comporte operativamente como Utopia en lo que importa para
gobernar memoria compartida: quién es cada persona, qué puede leer o escribir, cómo una memoria
pasa de propuesta a oficial, y qué queda registrado.

Se toma de Utopia:

- Identidad por persona y token personal (el token actúa como la persona).
- Roles ordenados y una única comprobación de permiso en cada petición.
- El token solo restringe: permiso efectivo = permiso de la persona intersectado con el alcance del token.
- Lo que propone un agente no es oficial hasta que una persona lo aprueba (propuesta separada de lo oficial).
- Rechazos que se recuerdan.
- Auditoría de solo inserción que sobrevive a la persona.
- Baja de persona que invalida sus tokens sin borrar su historial.

No se toma de Utopia:

- Bitemporalidad (dos relojes, `as_of`, eje del mundo).
- Grafo, entidades, ontología, alineación, reglas y derivaciones.
- Extracción automática de hechos desde documentos.
- Gobernador con LLM, cola de duplicados, precedentes, fusible.
- Chat, lakehouse, acciones declaradas, exportación RDF.

En Nexo la unidad es la memoria de Engram (observación con `topic_key`), no el hecho del grafo.

## 2. Equivalencias de conceptos

| Utopia | Nexo / Engram | Estado en Engram 3.3.2 |
|---|---|---|
| Despliegue | Servidor `engram cloud serve` + Postgres | Existe |
| Workspace | Un único servidor por organización | Existe (implícito) |
| Base de conocimiento (`knowledge_bases`) | Proyecto Engram: `nexo-transversal`, `nexo-transversal-propuestas` | Existe (proyecto) |
| Visibilidad `open` / `restricted` | Sin permiso sobre el proyecto = no se descarga ni se ve | Existe (todo o nada) |
| `users` + `is_admin` | `cloud_principals` (rol `admin` o `member`) + `cloud_human_users` | Existe |
| `kb_members` (viewer, editor, admin) | `cloud_project_grants` + nivel de acceso | Falta el nivel |
| `personal_tokens` (scope, expiry, last_used, revoked_at) | `cloud_principal_tokens` | Falta scope y expiry |
| `remember` (MCP) | `mem_save` en el asistente | Existe |
| `pending_facts` (espera de aprobación) | Caja `nexo-transversal-propuestas` | Falta permiso "solo subir" |
| Nod (confirmar) | Publicar: copiar la propuesta a `nexo-transversal` | Falta (skill + permiso) |
| `rejected_facts` | Propuesta marcada Descartado con motivo | Falta (skill) |
| `proposed_by` + `proposed_token` | `actor_principal_id` + `actor_token_id` (NX-01) | En desarrollo |
| `audit_events` | `cloud_auth_audit_log` + `nexo_activity_log` + autoría por cambio | En desarrollo (NX-01..04) |
| `deactivated_at` | `cloud_principals.enabled = false` | Existe |
| Alertas del sistema | Fuera de alcance por ahora | No aplica |

## 3. Perfiles de acceso

Un perfil Nexo es un conjunto de permisos por proyecto. El servidor solo conoce permisos por
proyecto; el perfil es un nombre para asignarlos de una vez (desde el panel o la API admin).

Niveles de acceso por proyecto (nuevo en `cloud_project_grants`):

| Nivel | Descargar | Subir | Equivale en Utopia |
|---|---|---|---|
| `read` | Sí | No | Viewer |
| `propose` | No (ver decisión abierta A1) | Sí | Token de ingesta, solo empuja |
| `write` | Sí | Sí | Editor |

Perfiles:

| Perfil | `nexo-transversal` | `nexo-transversal-propuestas` | Gestión de usuarios |
|---|---|---|---|
| lector | `read` | ninguno | No |
| proponente | `read` | `propose` | No |
| publicador | `write` | `write` | No |
| admin | según perfil asignado | según perfil asignado | Sí (rol `admin` del principal) |

Perfil por defecto de una persona nueva: lector.

Administrar usuarios y publicar memoria son permisos distintos: un admin publica solo si además
tiene el perfil publicador. Verificado: el rol `admin` de Engram no salta los permisos por proyecto
(`cmd/engram/cloud_runtime_auth.go:146-173` solo consulta `cloud_project_grants`), así que este
comportamiento ya es el de Engram. Ese mismo autorizador es el punto donde se agrega el nivel de acceso.

## 4. Funcionamiento

### F1. Alta de persona

1. Un admin crea el usuario en el panel (`/dashboard/admin/users`) con nombre y correo.
2. Asigna un perfil, que crea los permisos por proyecto.
3. Emite un token personal con nombre (por ejemplo "laptop Ana - Claude"), alcance (`read` o `write`) y caducidad (90 días por defecto; "sin caducidad" es elección explícita).
4. El token se muestra una sola vez. Se guarda solo el hash.
5. Todo queda en `cloud_auth_audit_log`.

### F2. Conectar el asistente

1. El dev ejecuta `Install-Nexo.ps1` con su token.
2. El instalador inscribe localmente (`engram cloud enroll`) solo los proyectos donde su perfil puede subir: el proponente inscribe `nexo-transversal-propuestas`, el publicador ambos y el lector ninguno.
3. La descarga no depende de la inscripción local: el servidor entrega todos los proyectos con permiso de lectura.

### F3. Consultar

1. El asistente busca siempre en el SQLite local (FTS5/BM25). El servidor no busca.
2. Lo oficial está en local porque el servidor lo entrega a todos con permiso `read`.
3. Las propuestas solo están en las PCs de su autor y de los publicadores.
4. La skill consulta `nexo-transversal` para estándares y nunca trata una propuesta como estándar.

### F4. Proponer

1. El asistente detecta algo transversal (umbrales de la skill) y guarda con `mem_save(project="nexo-transversal-propuestas", ...)`, estado Propuesta.
2. Engram lo escribe en local y el autosync lo sube.
3. El servidor fija autor y token desde el token (NX-01) y registra la conexión (NX-02, NX-04).
4. El asistente dice "propuesta enviada, pendiente de aprobación", no "guardado como estándar" (Utopia 0015 d3).

### F5. Revisar y publicar

1. El publicador pide a su asistente el repaso (`/nexo repaso`). La skill lista las propuestas abiertas con autor y fecha.
2. Para aprobar, la skill guarda la memoria en `nexo-transversal` con estado Oficial, el mismo `topic_key` y una referencia a la propuesta de origen; y marca la propuesta como Aprobada.
3. El servidor solo acepta escrituras en `nexo-transversal` de quien tiene `write`.
4. Un publicador puede aprobar su propia propuesta (decisión tomada; queda auditado quién propuso y quién publicó).
5. En la siguiente sincronización la memoria oficial llega a todas las PCs.

### F6. Rechazar

1. El publicador marca la propuesta como Descartado con el motivo; no se borra.
2. Antes de proponer, la skill busca en descartadas para no repetir una propuesta rechazada (Utopia `rejected_facts`).

### F7. Volver obsoleto un estándar

1. Un publicador marca la memoria oficial como Obsoleto (o la reemplaza con `mem_judge` / relación supersedes).
2. Nunca se borra físicamente: el historial de cambios en el servidor conserva cada versión con su autor.

### F8. Baja de persona o pérdida de equipo

1. Perder un equipo: el admin revoca ese token (`revoked_at`); la siguiente petición con él falla.
2. Baja de persona: el admin deshabilita al usuario; todos sus tokens dejan de funcionar.
3. La autoría y el registro de conexiones se conservan con la copia de nombre y correo (NX-01 RF-03b).

### F9. Auditoría

1. API admin `/nexo/admin/{audit,activity,presence,history}` (NX-03): quién subió qué, desde dónde, cuándo se conectó cada token, historial de una memoria.
2. Cada consulta de auditoría también se audita.

## 5. Reglas del servidor

- R1. Permiso efectivo = nivel del permiso por proyecto intersectado con el alcance del token. Un token `read` nunca sube, aunque la persona sea publicador.
- R2. Se comprueba en cada petición (subida y descarga, chunks y mutaciones), con revocación, caducidad y usuario habilitado en la misma consulta.
- R3. La descarga de mutaciones solo incluye proyectos con nivel `read` o `write`. Un proyecto `propose` nunca se entrega (salvo decisión A1).
- R4. La subida a un proyecto requiere nivel `propose` o `write`.
- R5. El token heredado compartido (`ENGRAM_CLOUD_TOKEN`) no tiene perfil: queda sin uso (valor aleatorio no entregado a nadie).
- R6. Toda denegación queda en `cloud_auth_audit_log` con motivo.

## 6. Restricciones que impone Engram (verificadas en el código)

| Hallazgo | Dónde | Consecuencia para el diseño |
|---|---|---|
| La comprobación de proyecto no distingue subir de descargar | `cloudserver.go:850` `authorizeProjectScope` | Hay que pasarle la dirección (lectura o escritura) |
| La descarga de mutaciones entrega todos los proyectos con permiso; el cliente no elige | `mutations.go` `handleMutationPull`, `EnrolledProjectsForPrincipal` | El filtrado de propuestas debe hacerse en el servidor (R3). Ventaja: no depende del cliente |
| La subida de mutaciones rechaza el lote completo si un proyecto no está permitido | `mutations.go:136-150` | El cliente agrupa por proyecto, así que en la práctica afecta solo a ese proyecto |
| Si la subida recibe 403, el ciclo de autosync termina sin descargar y entra en reintento con espera | `autosync/manager.go:544-548` | Un lector que inscriba `nexo-transversal` y guarde algo ahí deja de recibir actualizaciones. Mitigación: el instalador inscribe según perfil y la skill no escribe donde el perfil no permite |
| Lo no inscrito localmente no se sube y no bloquea la descarga | `autosync/manager.go:712-718`, `safeOutboundBlock` | Es el comportamiento deseado para el lector |
| Los chunks agrupan muchas memorias de un proyecto | ruta `/sync/push` y `/sync/pull` | En chunks solo se puede filtrar por proyecto, no por autor |
| Los tokens no tienen alcance ni caducidad | `cloudstore.go:668-680` | Columnas nuevas `scope` y `expires_at` |
| Los permisos por proyecto no tienen nivel | `cloudstore.go:683-689` | Columna nueva `access` con valor por defecto `write` (compatibilidad) |

## 7. Qué existe y qué falta

| Pieza | Estado | Trabajo |
|---|---|---|
| Usuarios, tokens, permisos por proyecto, panel admin | Existe | Nada |
| Autoría por cambio desde el token | En desarrollo | NX-01 |
| Registro de conexiones y metadatos del cliente | En desarrollo | NX-02, NX-04 |
| API de auditoría | Especificado | NX-03 |
| Nivel de acceso por proyecto y perfiles | Falta | NX-05 (servidor + panel/API) |
| Alcance y caducidad de token | Falta | NX-06 (servidor + panel/API) |
| Caja de propuestas, publicar, descartar | Falta | Skill nexo-protocol v0.3 + instalador por perfil |

## 8. Decisiones

Tomadas (2026-10-12):

- D-R1. Dos cajas: propuestas y oficial, como proyectos separados.
- D-R2. Un publicador puede aprobar su propia propuesta (como Utopia).
- D-R3. Una propuesta rechazada se conserva como Descartado con motivo.
- D-R4. Alcance y caducidad de token entran en la misma especificación que los roles.
- D-R5. Un proponente no ve las propuestas de otros (nivel `propose` = solo subir).
- D-R6. Se distribuye un Engram propio (cliente y servidor del fork) a los devs. Lo nativo puede
  vivir en el cliente: vigencia, búsqueda filtrada y herramientas MCP de Nexo (sección 9).

Abiertas:

- A1. Cómo se entera el autor de que su propuesta fue aprobada o descartada. Opciones:
  (a) solo por la memoria oficial, que llega con el mismo `topic_key`; no ve los descartes;
  (b) el servidor entrega al proponente únicamente las propuestas que él creó, usando la autoría de NX-01 (solo por la ruta de mutaciones, no por chunks).
  Recomendación: (b).
- A2. Resuelta: el rol `admin` no salta los permisos por proyecto (ver sección 3).
- A3. Nombre final de la caja de propuestas (`nexo-transversal-propuestas` propuesto).

## 9. Engram propio: temporalidad y funciones nativas

Con D-R6 el cliente también es del fork. Se toma de la bitemporalidad de Utopia solo lo útil para
memorias de texto; no se copia el modelo de hechos atómicos con dos relojes completos.

### 9.1 Reloj de registro (cuándo lo supo Nexo y quién lo cambió)

Casi todo existe:

- Local: `observations.created_at`, `updated_at`, `deleted_at` (borrado lógico), `observation_versions`
  (historial por memoria) y `memory_relations` con `superseded_at`.
- Servidor: `cloud_mutations` guarda la memoria completa en cada cambio, con autor desde NX-01.

Falta: consulta "cómo estaba a una fecha" (`as_of`), reconstruida desde `cloud_mutations` en el
servidor. Extensión de la API NX-03, sin cambios en el cliente.

### 9.2 Reloj de vigencia (desde y hasta cuándo es cierto)

No existe. Propuesta:

- Columnas `valid_from` y `valid_until` (opcionales) en `observations` (SQLite) y en el payload de
  sincronización; el servidor las conserva en la copia materializada.
- `mem_save` acepta vigencia; `mem_search` devuelve por defecto solo lo vigente hoy y admite `at=<fecha>`.
- Obsoleto deja de ser texto en el contenido: es `valid_until` en el pasado.
- Compatibilidad verificada: el payload de sincronización no se decodifica en modo estricto (solo
  `prompt_discard.go` usa `DisallowUnknownFields`), así que un cliente oficial ignora los campos
  nuevos. Riesgo: si un cliente oficial reescribe una memoria con vigencia, la pierde. Mitigación:
  en el servidor, rechazar o advertir escrituras sin versión Nexo sobre memorias con vigencia.

### 9.3 Funciones que pasan de la skill a Engram

| Hoy (convención de la skill) | Nativo |
|---|---|
| Estado Oficial/Obsoleto en el texto | Vigencia y filtro en la búsqueda |
| "No uses propuestas como estándar" | La búsqueda excluye la caja de propuestas por defecto |
| Proponer o publicar con `mem_save` y un proyecto | Herramientas MCP `nexo_propose`, `nexo_publish`, `nexo_discard` |
| Reglas de permiso explicadas en el prompt | Validación en el servidor (sección 5) |

La skill sigue decidiendo qué es transversal (umbrales) y cuándo consultar; deja de cargar reglas
que el código ya hace cumplir.

### 9.4 Distribución

- Versiones `vX.Y.Z-nexo.N` del mismo código para cliente y servidor; un binario por plataforma con
  el `.goreleaser.yaml` existente, publicado en releases del repositorio privado.
- `Install-Nexo.ps1` instala el binario del fork en `.nexo-bin` en lugar del oficial.
- Aviso de actualización: `internal/version/check.go` consulta las releases del Engram oficial y
  sugiere `brew upgrade` o `go install` del oficial, lo que reemplazaría el binario del fork. Hay que
  apuntarlo a las releases del fork o desactivarlo (`ENGRAM_NO_UPDATE_CHECK`) desde el instalador.
- `engram version` debe mostrar el sufijo `-nexo.N` para saber qué tiene cada PC; el registro de
  conexiones (NX-02) ya guarda el User-Agent del cliente.
- Costo: cada actualización de Engram fusiona también cliente y migraciones SQLite. Las migraciones
  locales de Nexo van en archivos `nexo_*.go`, aditivas y marcadas, como en el servidor.

### 9.5 Orden propuesto

1. Consulta `as_of` en el servidor (junto a NX-03).
2. Roles y tokens (NX-05, NX-06) en el servidor.
3. Primera versión del cliente propio: vigencia, búsqueda filtrada, herramientas `nexo_*`, aviso de
   actualización apuntando al fork, instalador que distribuye el binario.
4. Skill nexo-protocol v0.3 simplificada sobre las herramientas nativas.
