# Espacios de conocimiento: requisitos

Estado: borrador para revisión. Fecha: 2026-10-12. Rama: `feat/conocimiento`.
Identificador del cambio: **NX-10** (espacios de conocimiento). Base: Engram 3.3.2 + trazabilidad
NX-01..NX-04 (`develop`, `d0a0e62`). Roles: `docs/nexo/roles/` (NX-05).

## 1. Contexto y objetivo

Hoy la memoria transversal es un proyecto reservado de Engram (`nexo-transversal`): el estado
(Propuesta/Oficial), la vigencia y el responsable son texto dentro de la memoria, cualquier persona con
permiso sobre el proyecto puede escribir, y no existen documentos con secciones. Esa representación fue
la solución temporal del piloto (`05-investigacion/analisis/Nexo_IA_Piloto_Basico.md`, sección
"Cuándo crear una entidad transversal propia") y su reemplazo estaba planificado como fase 2
(`02-nexo-kit/MAPA_FASE1.md` §6; `02-nexo-kit/ROLES_OBJETIVO.md`, opción D).

Objetivo: una **capa de conocimiento** separada de la memoria de proyectos, con espacios que el
administrador crea con nombre propio, roles por espacio aplicados en el servidor, documentos con
versiones, secciones y vigencia como datos, propuestas revisadas por personas, y consulta desde los
asistentes en el servidor con copia local para trabajar sin conexión.

## 2. Dos capas separadas

| | Memoria (proyectos de Engram) | Conocimiento (espacios Nexo) |
|---|---|---|
| Qué guarda | Lo que el equipo y sus asistentes aprenden trabajando: decisiones, tareas, pendientes | Lo oficial de la empresa: estándares, convenciones, lineamientos, glosario, reglas de negocio generales, catálogos |
| Quién escribe | Los miembros del proyecto (proponentes y más), directo | Solo publicadores y responsables del espacio; los demás, como mucho, proponen |
| Estados | Verificado, Pendiente de confirmar, Descartado | Oficial (vigente), Retirado; propuestas Abierta, Aprobada, Descartada |
| Almacenamiento | Tablas de Engram (`cloud_chunks`, `cloud_mutations`), sin cambios | Tablas propias `nexo_kb_*` |
| Consulta | Local (SQLite de Engram), herramientas `mem_*` | Servidor, con copia local de solo lectura; herramientas `nexo_*` |
| Comparten | Usuarios, tokens, roles (NX-05), administración y trazabilidad (NX-01..NX-04) | Lo mismo |

Esta especificación no modifica el almacenamiento ni el protocolo de la capa de proyectos.

## 3. Glosario

| Término | Definición |
|---|---|
| Espacio | Almacén de conocimiento con nombre propio (`slug`), descripción y visibilidad. No está ligado a un repositorio ni a una carpeta |
| Miembro | Persona o cuenta de servicio con un rol en un espacio |
| Documento | Unidad publicada en un espacio, con identidad estable (`doc_key`), categoría, responsable, vigencia y versiones |
| Versión | Contenido de un documento en un momento, normalizado a Markdown, inmutable |
| Sección | Fragmento de una versión, cortado por títulos, con su ruta de títulos; es lo que se busca y se cita |
| Propuesta | Contenido nuevo o un cambio a un documento, enviado por un proponente, en espera de revisión |
| Origen | De dónde vino una versión: repositorio git y commit, archivo subido, o propuesta aprobada |
| Copia local | Réplica de solo lectura, en la PC, de las secciones vigentes de los espacios visibles para esa persona |

## 4. Roles

Los roles se definen una sola vez para toda la plataforma en `docs/nexo/roles/requirements.md`
(NX-05): roles de plataforma (superadmin, admin, auditor, miembro) y roles de ámbito, **los mismos
para proyectos y espacios** (lector, proponente, publicador, responsable). Esta sección resume solo
lo que aplica a los espacios.

| Rol | En un espacio |
|---|---|
| lector | Buscar y leer documentos vigentes, historial y copia local (`space.read`) |
| proponente | Lo anterior + crear y retirar propuestas propias y ver su estado (`space.propose`) |
| publicador | Lo anterior + publicar, actualizar, retirar y reemplazar documentos; revisar propuestas, incluidas las propias; leer eventos del espacio (`space.publish`, `space.review`, `space.audit.read`) |
| responsable | Lo anterior + gestionar miembros y configuración del espacio (`space.members.manage`, `space.configure`) |
| superadmin, admin (plataforma) | Crear y archivar espacios, nombrar el primer responsable, recuperar un espacio sin responsable; sin permisos de contenido salvo que tengan un rol en el espacio |
| auditor (plataforma) | Leer los eventos de todos los espacios; sin contenido salvo rol en el espacio |

Reglas que aplican aquí (detalle en `docs/nexo/roles/requirements.md` §3.1 y §5): solo los
publicadores y responsables asignados actualizan un espacio; en un espacio abierto, quien no tiene
asignación recibe el rol implícito del espacio (`default_role`: lector por defecto, o proponente si el
responsable quiere recibir propuestas de cualquier dev); un espacio restringido es invisible para
quien no tiene rol (404); cada espacio tiene al menos un responsable; las cuentas de servicio nunca son
responsables; permiso efectivo = rol ∩ alcance del token; el código comprueba capacidades, no nombres
de rol.

## 5. Requisitos funcionales

### Espacios y miembros

- **RF-01.** Un superadmin o admin crea un espacio con `slug` único (minúsculas, números y guiones), nombre, descripción, visibilidad (por defecto **abierto**), rol implícito (por defecto lector) y al menos un responsable.
- **RF-02.** El responsable cambia nombre, descripción, visibilidad, rol implícito y categorías permitidas del espacio. El `slug` no cambia.
- **RF-03.** Un superadmin o admin archiva un espacio: deja de aparecer en búsquedas y copias locales, conserva todo su contenido e historial y se puede restaurar.
- **RF-04.** El responsable agrega, cambia de rol y quita miembros, respetando las reglas de roles (cuentas de servicio nunca responsables, nunca sin responsable).
- **RF-05.** Cualquier usuario lista los espacios que puede ver, con su rol efectivo en cada uno.

### Documentos y publicación

- **RF-10.** Un publicador publica un documento en un espacio con: `doc_key` (identidad estable dentro del espacio), título, categoría (estándar, convención, lineamiento, glosario, regla de negocio, catálogo; ampliable por espacio), responsable, vigente desde (por defecto, la publicación), vigente hasta (opcional), contenido Markdown y origen. Cubre HU-29.
- **RF-11.** Publicar el mismo `doc_key` con contenido distinto crea una versión nueva; con contenido idéntico (mismo hash) no crea versión ni evento. Publicar es idempotente.
- **RF-12.** Publicar acepta lotes (un directorio de un repositorio en un solo pedido), con resultado por documento: creado, actualizado, sin cambios o rechazado y por qué.
- **RF-13.** Con la opción de poda, los documentos del espacio cuyo origen es el mismo repositorio y que ya no vienen en el lote quedan **retirados** (no borrados). Los documentos de otros orígenes no se tocan.
- **RF-14.** Un publicador retira un documento (deja de estar vigente) o lo reemplaza por otro; el reemplazado indica cuál lo reemplaza. Cubre HU-30 y HU-08.
- **RF-15.** Toda versión guarda quién la publicó (persona y token, NX-01) y su origen: para git, repositorio, rama, commit, ruta y, si el CI lo informa, autor y aprobador del cambio; para archivos subidos, nombre y hash.
- **RF-16.** Las versiones son inmutables. Nada se borra físicamente por la API; el historial completo queda consultable (HU-20 c2).
- **RF-17.** El servidor corta cada versión en secciones por títulos, conservando la ruta de títulos, sin partir tablas ni bloques de código, con un tamaño objetivo configurable; cada sección es citable por un ancla estable.
- **RF-18.** Formatos: primera entrega Markdown y texto plano. Word, Excel, PowerPoint y PDF con texto se convierten a Markdown antes de publicar (etapas posteriores, sección 9). Un PDF escaneado se rechaza con un mensaje claro.

### Consulta

- **RF-20.** Buscar en todos los espacios visibles o en los indicados, por texto, con filtros por categoría y etiqueta. Por defecto solo secciones de documentos **vigentes hoy**; con `at=<fecha>`, lo vigente en esa fecha.
- **RF-21.** Cada resultado trae: espacio, documento, título, sección y ruta de títulos, extracto, categoría, versión, vigencia, responsable y un ancla para pedir el texto completo. Cubre HU-05 y RN-03.
- **RF-22.** Leer un documento completo o una sección por su ancla, en la versión vigente o en una versión dada.
- **RF-23.** Si no hay resultados, la respuesta lo dice explícitamente para que el asistente indique que no existe un estándar oficial (HU-06).
- **RF-24.** Un documento retirado o reemplazado nunca aparece como vigente; si se pide, se indica su estado y su reemplazo (HU-08).

### Propuestas

- **RF-30.** Un proponente crea una propuesta en un espacio: documento nuevo o cambio a uno existente (indicando la versión que vio), con título, contenido Markdown y justificación. Guarda quién la hizo, con qué token y desde qué asistente.
- **RF-31.** Una propuesta nunca aparece en búsquedas ni en copias locales. La ven su autor y quienes tienen `space.review` en el espacio.
- **RF-32.** Un publicador aprueba (opcionalmente editando el contenido) o descarta con motivo. Aprobar crea el documento o la versión nueva con origen "propuesta"; si el documento cambió desde la versión que vio el proponente, la aprobación exige confirmación explícita.
- **RF-33.** El autor consulta el estado de sus propuestas y el motivo de un descarte.
- **RF-34.** Antes de proponer, el asistente puede consultar propuestas descartadas parecidas del mismo espacio para no repetirlas.

### Copia local y sin conexión

- **RF-40.** El cliente mantiene una copia local de solo lectura con las secciones vigentes de los espacios visibles para su usuario, actualizada de forma incremental.
- **RF-41.** Si un espacio deja de ser visible (cambio de rol, espacio restringido o archivado), su contenido desaparece de la copia local en la siguiente actualización.
- **RF-42.** Sin conexión, la búsqueda usa la copia local y la respuesta indica la fecha de la última actualización (HU-28, RU-13).
- **RF-43.** La copia local no se puede editar ni se vuelve a subir.

### Auditoría

- **RF-50.** Cada acción que cambia el estado (espacio, miembros, publicación, retiro, propuesta, revisión) queda en un registro de eventos de solo inserción con actor, token, IP y cliente (reusa NX-01/NX-04).
- **RF-51.** Superadmin, admin, auditor y los responsables y publicadores del espacio consultan sus eventos por la API (mismas reglas de NX-03: paginación por cursor, máximo 200, la consulta queda auditada).

## 6. Reglas de negocio

- **RN-K1.** Solo un publicador o responsable del espacio cambia contenido oficial. Ninguna ruta de sincronización de Engram escribe en las tablas de conocimiento.
- **RN-K2.** Una propuesta no es conocimiento oficial hasta que una persona con `space.review` la aprueba (RN-11 del MVP, reformulada: los asistentes proponen, no publican).
- **RN-K3.** Si un recuerdo de proyecto contradice un documento oficial vigente, prevalece el documento y el asistente señala la contradicción (RN-02 del MVP).
- **RN-K4.** No se publica ni se propone contenido con secretos ni datos personales (RN-09): el servidor rechaza patrones evidentes de secretos y el asistente omite lo detectado.
- **RN-K5.** La identidad de quien publica o propone sale siempre del token (principio de NX-01); lo que declara el cliente es solo referencia.

## 7. Requisitos no funcionales

| ID | Requisito | Valor objetivo |
|---|---|---|
| RNF-K1 | Búsqueda en el servidor | p95 < 300 ms con 1 millón de secciones vigentes; máximo aceptable 3 s (RNF-02 del MVP) |
| RNF-K2 | Búsqueda en la copia local | < 200 ms (RNF-01 del MVP: < 1 s) |
| RNF-K3 | Publicación de un lote de 200 documentos Markdown | < 60 s |
| RNF-K4 | Actualización incremental de la copia local sin cambios | Una petición, < 500 ms |
| RNF-K5 | Escala de diseño | 500 espacios, 50 000 documentos, 1 millón de secciones, 2 000 usuarios, varias réplicas del servidor sin estado |
| RNF-K6 | Aislamiento de Engram | Código en paquete propio y tablas `nexo_kb_*`; en archivos de Engram solo líneas de enganche marcadas `// NX-10` |
| RNF-K7 | Compatibilidad | Clientes oficiales de Engram siguen sincronizando proyectos sin cambios; las funciones de conocimiento requieren el cliente Nexo |
| RNF-K8 | Evolución | API versionada (`/nexo/kb/v1`); roles, categorías, formatos y motor de búsqueda reemplazables sin migrar datos de contenido |

## 8. Criterios de aceptación

| ID | Criterio |
|---|---|
| CA-K01 | Un admin crea `lineamientos-frontend` (abierto) con Ana como responsable; Luis, sin ser miembro, lo ve y busca en él como lector |
| CA-K02 | En un espacio restringido, quien no es miembro recibe 404 al pedirlo y no lo ve en listados ni búsquedas |
| CA-K03 | La cuenta de servicio del CI publica 3 Markdown; volver a publicar el mismo lote no crea versiones ni eventos |
| CA-K04 | Se modifica un archivo y se publica: versión 2, la búsqueda devuelve el texto nuevo y el historial muestra ambas con su autor y commit |
| CA-K05 | Con poda, un archivo borrado del repositorio queda retirado; un documento del mismo espacio subido a mano no se toca |
| CA-K06 | Un lector que intenta publicar o revisar recibe 403 y el intento queda auditado |
| CA-K07 | Ana cambia el rol implícito a proponente; Luis propone una regla; no aparece en búsquedas ni en la copia local de nadie; Ana la aprueba; aparece como oficial con origen "propuesta" y autor de la propuesta y de la aprobación |
| CA-K08 | Una propuesta descartada muestra el motivo a su autor y no aparece para otros proponentes |
| CA-K09 | Un documento con vigencia futura no aparece hoy y sí con `at=` en esa fecha; uno retirado indica su reemplazo |
| CA-K10 | Sin conexión, la búsqueda local responde e informa la fecha de la copia; al reconectar se actualiza sola |
| CA-K11 | Al quitar a Luis de un espacio restringido, su copia local deja de tener ese espacio tras la siguiente actualización |
| CA-K12 | Los proyectos de Engram siguen sincronizando igual con el cliente oficial 3.3.2 (prueba E2E de trazabilidad sin cambios) |
| CA-K13 | Cada acción de los criterios anteriores aparece en los eventos del espacio con actor, token, IP y cliente |
| CA-K14 | Rendimiento: RNF-K1 y RNF-K2 medidos con datos sintéticos |

## 9. Alcance por etapas

| Etapa | Incluye |
|---|---|
| **K1 (esta especificación)** | Espacios con roles (NX-05); documentos Markdown y texto con versiones, secciones y vigencia; publicación por API y CLI (lotes, idempotencia, poda); búsqueda y lectura; copia local y herramientas MCP; propuestas y revisión; eventos; migración del proyecto `nexo-transversal` a un espacio |
| K2 | Conversores Word y Excel |
| K3 | PDF con texto y PowerPoint; adjuntos (diagramas) descargables |
| K4 | Alcance y caducidad de tokens (NX-06, etapa R4 de roles) |
| K5 | Búsqueda semántica o híbrida (embeddings en el servidor), tras decidir de dónde salen los embeddings |
| K6 | Pantallas del panel para espacios, documentos y revisión |

## 10. Fuera de alcance

- Cambios en el almacenamiento de la capa de proyectos de Engram, workspaces con varios repositorios (HU-09, HU-14) y la memoria de trabajo. (Los roles sobre proyectos se especifican en `docs/nexo/roles/`.)
- OCR, grafo, ontología y extracción automática de hechos.
- Edición colaborativa en línea de documentos (se edita en el origen: repositorio o propuesta).

## 11. Decisiones

Tomadas:

- D-K1. Capa separada de los proyectos, dentro del mismo servidor (opción A).
- D-K2. Varios espacios con nombre propio, creados por superadmin o admin.
- D-K3. Roles por espacio, los mismos que en proyectos (NX-05).
- D-K4. Espacios nuevos abiertos por defecto.
- D-K5. Un espacio no está ligado a un repositorio; git es una de las formas de publicar.
- D-K6. Consulta en el servidor con copia local de solo lectura.
- D-K7. Un publicador puede aprobar su propia propuesta (D-R2 del modelo funcional).
- D-K8. Las propuestas descartadas se conservan con su motivo (D-R3).

Aprobadas por el usuario el 2026-10-12, tal como se proponen:

- D-K9. Rol **responsable** por ámbito (D-1 de `docs/nexo/roles/requirements.md`).
- D-K10. Los documentos cortos que nacen de propuestas viven en el servidor (no generan un PR al repositorio).
- D-K11. No se guarda el archivo original binario en la primera etapa, solo su hash y su origen.
