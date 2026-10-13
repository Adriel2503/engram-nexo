# Espacios de conocimiento (NX-10): tareas

Estado: borrador para revisión. Fecha: 2026-10-12 (actualizado el 2026-10-13, tanda 2). Rama: `feat/conocimiento-nucleo` (desde `develop`).
Documentos: `requirements.md`, `design.md`; roles en `docs/nexo/roles/` (NX-05, con sus propias
tareas en `docs/nexo/roles/tasks.md`).

Reglas de trabajo (las mismas de la trazabilidad): código en paquetes `internal/nexo/...`; en archivos
de Engram solo enganches de una línea marcados `// NX-05` o `// NX-10`; tests contra Postgres real con
`CLOUDSTORE_TEST_DSN`; sin commits ni push sin revisión del usuario; sin atribución.

Dependencia: la etapa R1 de roles (motor `internal/nexo/authz`, plataforma y API `/nexo/authz/v1`)
va antes de la fase C.

Panel web para la presentación (2026-10-13, sin commit): marca Nexo IA y textos en español, pestaña Conocimiento, pasos de "Cómo funciona", buscador con respuesta citada, documentos por carpeta, Markdown seguro (`internal/nexo/kb/mdhtml`) y "qué cambió" entre versiones; diseño en `docs/nexo/panel/design.md`, cambios en `NEXO_CHANGES.md` §3 y §4.

## Estado (entrega 2b, tanda 2, 2026-10-13, sin commit)

Detalle y desviaciones D-K2b-22..D-K2b-30 en `design.md` §19. E2E: `nexo_ia/06-pruebas/conocimiento/RESULTADOS.md` §4
(26 de 26 casos OK, con el MCP real por stdio, `engram nexo connect` y `engram setup` en un HOME temporal).

| ID | Estado | Notas |
|---|---|---|
| D-01 Copia local (CA-K10, CA-K11, KP-07) | Hecho | `internal/nexo/kb/client`: SQLite propio con FTS5/BM25 (`~/.nexo/kb.db`, `$NEXO_HOME/kb.db` o `NEXO_KB_LOCAL_DB`), feed `/sync` por páginas, vigentes y programadas con vigencia filtradas al consultar, borrado de espacios no visibles, relectura de un espacio nuevo, ventana de solape (D-K2b-22), copia reconstruida al cambiar de persona o servidor, respuesta desde la copia marcada con fecha y antigüedad |
| D-02 MCP | Hecho | 8 herramientas (`nexo_search`, `nexo_get`, `nexo_spaces`, `nexo_changes`, `nexo_propose`, `nexo_proposals`, `nexo_review`, `nexo_whoami`) con `at`/`known_at`; un enganche `// NX-10` en `registerTools`; pruebas de contrato con un cliente MCP en proceso (`client/client_pg_test.go`) |
| D-03 CLI | Hecho | `get`, `sync`, `search --local`, `eval --retriever local-bm25`, `import-transversal` (dry-run, idempotente, `engram.db` en solo lectura o `--from` un export) |
| D-04 GitHub Actions | Hecho (plantilla) | `docs/nexo/conocimiento/github-actions/` (dry-run en PR, `upload` al hacer merge a `main`, secreto `NEXO_KB_TOKEN`); opcional, no probado contra GitHub |
| F-04 Skill e instalación | Hecho (en el fork) | Cambio de alcance del 2026-10-13 (design §19.4): `engram nexo connect`, `status`, `disconnect` y `setup`; `engram setup <agente>` instala el protocolo con la PC conectada: en claude-code y codex dentro del plugin del fork (marketplace `Adriel2503/engram-nexo`, D-K2b-35 opción A), en opencode como skill y en el resto como bloque de reglas; fuente única `internal/nexo/setup/SKILL.md`. El kit `02-nexo-kit` queda histórico. Opción B (plugin embebido, sin acceso al repositorio) pendiente para producción |
| C-10 (motor local) | Hecho | `eval --retriever local-bm25`: medición de la copia local (RESULTADOS §4.1) |
| F-05 | Pendiente | Revisión del usuario y commit |

## Estado (entrega 2b, tanda 1, 2026-10-13, sin commit)

Decisiones del usuario del 2026-10-13: subida de archivos sueltos como vía principal (git opcional),
una API para CLI y panel, sin originales (volver a subir para reprocesar), KP-10 descartado, política
de secretos pendiente. Detalle y desviaciones D-K2b-1..D-K2b-21 en `design.md` §18.

| ID | Estado | Notas |
|---|---|---|
| U-01 Subida (HU-29) | Hecho | `POST /spaces/{slug}/uploads` (archivos, carpeta, zip), defensas de tamaño, entradas, tipo, zip-slip y zip bomb; CLI `upload`; panel `/dashboard/kb` |
| U-02 Word | Hecho | `ingest/docx` en Go puro (títulos, párrafos, listas, tablas, procedencia) |
| U-03 Wiki de Azure DevOps (IN-08, KP-11) | Hecho | `source` + conversor `ado-md`; `[TO_REMOVE]` retiradas, `≤-AAAA` vencidas, `[TO_TIDY]` etiquetada |
| C-07 Propuestas (KP-08) | Hecho | Conflicto 409 con `force`, vigencia futura, descarte con motivo, retiro; proponentes no ven ajenas; CA-K07, CA-K08 |
| C-08 Rutas | Hecho | Se agregan subida, propuestas, `/changes`, `/sync`, `/eval-runs`, `publish-runs`, `admin/index` |
| C-09 Feed `/sync` (KP-07) | Hecho | Vigentes y programadas con vigencias; la copia local lo consume desde la tanda 2 (con `public_url` y `source_url`) |
| RC-27 Qué cambió | Hecho | `/changes` y `engram nexo kb changes`, relojes `valid` y `recorded` |
| KP-09 Avisos | Hecho | `kb_doc_expiring`, `kb_doc_review_due` en `nexo_alerts` (capa conocimiento) |
| KP-01, KP-02 | Hecho | Tope de poda (25 %, `confirm`); `nexo_kb_publish_runs` |
| RC-08 Enlaces | Hecho | `link` permanente al panel y `source_url` |
| C-10 / R0 | Hecho (borrador) | `eval` por clases y métricas portadas (Hit@5, Recall@5, MRR, nDCG, MAP, sin respuesta), `nexo_kb_eval_runs`, prueba de calidad en CI (`eval/quality_test.go`), 50 preguntas borrador sobre la wiki (a validar por 3-4 devs) |
| R1 | Hecho | RC-03 (con reindexado seguro), RC-04, RC-05, RC-06, RC-07, RC-09, RC-10, RC-22, IN-07 (`quote`); todo desactivable con `NEXO_KB_R1` |
| SG-09 | Parcial | Detección ampliada; la política (rechazar o enmascarar) sigue pendiente de decisión: hoy se rechaza |
| D-01, D-02, D-03 (`get`, `import-transversal`), D-04, F-04 | Hecho en la tanda 2 | Ver el estado de la tanda 2 arriba |

Medición de calidad (wiki de arquitectura, 50 preguntas borrador, `design.md` §18.3):

| Clase | Línea base Hit@5 / MRR | R1 Hit@5 / MRR |
|---|---|---|
| normal (15) | 46,7 % / 0,255 | 80,0 % / 0,522 |
| exact (8) | 87,5 % / 0,875 | 100,0 % / 0,938 |
| paraphrase (7) | 28,6 % / 0,119 | 0,0 % / 0,000 |
| no_answer (8), acierto | 0 % | 50 % |
| temporal (8) | 62,5 % / 0,601 | 100,0 % / 0,817 |
| changes (4) | 0 % | 100 % |
| total con respuesta | 50,0 % / 0,392 | 76,2 % / 0,616 |

La paráfrasis queda en 0 % con el umbral (3 falsos "no hay"): es la condición de §17.3 para pasar a
R2 (vectores). La prueba de CI usa un corpus sintético (`eval/testdata/mini`) con umbral Hit@5 0,80 y
"no hay respuesta" 0,66, y compara con la última corrida aceptada en `nexo_kb_eval_runs`.

## Estado (entrega 2a, 2026-10-12, rama `feat/conocimiento-nucleo`)

| ID | Estado | Notas |
|---|---|---|
| A-02 | Hecho | Línea base: 1254 pass, 1 skip, 0 fail en `./internal/cloud/... ./internal/nexo/...`; `TestCloudRuntime` ok |
| A-03 | Hecho | `internal/nexo/kb/{domain,ingest,store,search,service,httpapi}` más `eval`, `cli`, `kbtest`; `client` en 2b |
| C-01 | Hecho | 8 tablas (incluida `nexo_kb_proposals` sin lógica), índices, triggers de inmutabilidad, `nexo_es` con o sin `unaccent` (detectado y registrado), `nexo_kb_index_versions`; idempotente; dos cuentas (la dueña migra; versiones y eventos solo inserción para la aplicación) |
| C-02 | Hecho | Conversores Markdown y texto con bloques y procedencia, registro por extensión y MIME, límites, front matter, `Sectioner`, detector de secretos; pruebas con lineamientos de frontend |
| C-03 | Hecho | Crear (responsable, abierto, `default_role`), configurar, archivar, restaurar sobre `authz`; CA-K01, CA-K02 |
| C-04 | Hecho | Lotes idempotentes por hash, versiones, secciones, cambios, eventos con actor del token, origen git, poda con lote completo, `dry-run`; CA-K03, CA-K04, CA-K05 |
| C-05 | Hecho | Retirar y reemplazar (idempotente), reactivación al volver a publicar; CA-K09 |
| C-06 | Hecho | `pgfts` detrás de `Retriever` con filtros comunes obligatorios, `at` y `known_at`, `no_results`, lectura de documento y sección (vigente o por versión); ver desviaciones D-I2..D-I6 y la medición abajo |
| C-08 | Parcial | Rutas de espacios, lotes, documentos, retiro y reemplazo, búsqueda, lectura y eventos con 401/403/404/409/413/422 y cursor; CA-K06, CA-K13. Faltan las de propuestas y `/sync` (2b). Miembros por `/nexo/authz/v1` (D-I8) |
| C-10 | Hecho (servidor) | `eval/preguntas.yaml`, paquete `eval`, `engram nexo kb eval`; el motor local BM25 llega con la copia local (2b) |
| D-03 | Parcial | `engram nexo kb publish|search|spaces|eval`; faltan `get` e `import-transversal` (2b) |
| C-07, C-09, D-01, D-02, D-04 | Pendiente (2b) | Propuestas, feed `/sync`, copia local, MCP, plantilla de GitHub Actions |

Medición de calidad (C-10) con `internal/nexo/kb/eval/testdata/preguntas.yaml` (11 preguntas sobre
los lineamientos de prueba): solo AND, Recall@5 = 54,5 %, MRR = 0,545; con la segunda pasada OR
(D-I4), Recall@5 = 100 %, MRR = 0,955.

Medición de rendimiento (RNF-K1, `service/perf_test.go`, `NEXO_KB_PERF_SECTIONS`): Postgres 16 de
pruebas en Docker Desktop (Windows, configuración por defecto: `shared_buffers` 128 MB), 500 espacios
abiertos, 20 secciones vigentes y 2 históricas por documento, vocabulario sesgado (las 20 palabras más
comunes aparecen en el 90 % de las secciones: peor caso a propósito), 100 búsquedas por escenario a
través del servicio completo (autorización, filtros comunes, pgfts en dos etapas, `ts_headline`):

| Secciones vigentes | Escenario | p50 | p95 | p99 | máx. |
|---|---|---|---|---|---|
| 20 000 (antes de D-I5) | 500 espacios visibles | 148 ms | 1 205 ms | 1 819 ms | 1 975 ms |
| 200 000 | 500 espacios visibles | 75 ms | 190 ms | 271 ms | 280 ms |
| 200 000 | 5 espacios pedidos | 25 ms | 52 ms | 64 ms | 98 ms |
| 200 000 | `known_at` hace 15 días | 76 ms | 134 ms | 275 ms | 282 ms |
| 1 000 000 | 500 espacios visibles | 185 ms | 312 ms | 1 124 ms | 1 203 ms |
| 1 000 000 | 5 espacios pedidos | 68 ms | 163 ms | 214 ms | 1 353 ms |
| 1 000 000 | `known_at` hace 15 días | 198 ms | 366 ms | 1 373 ms | 14 s (primera consulta, índice completo en frío) |

Con 1 millón el p95 queda 4 % por encima del objetivo de 300 ms en el peor caso (todos los espacios
visibles y términos presentes en casi todas las secciones) y siempre bajo el máximo aceptable de 3 s,
salvo la primera consulta histórica en frío. Para producción: dimensionar `shared_buffers` para que
quepan los índices GIN, `NEXO_KB_RANK_CANDIDATES` más bajo si hace falta y, si la medición con datos
reales lo pide, un índice con ranking (RUM o BM25) detrás del mismo `Retriever` (design §14.7).
RNF-K3 (`TestPublishBatchOf200Documents`): un lote de 200 documentos Markdown (1 800 secciones) se
publica en 3,0 s y su reintento idempotente tarda 1,8 s (objetivo < 60 s).

## Fase A. Preparación

| ID | Tarea | Resultado |
|---|---|---|
| A-01 | Confirmar decisiones abiertas: D-1..D-7 de roles, D-K9..D-K11 | Decisiones cerradas en los documentos |
| A-02 | Línea base: `go test ./internal/cloud/...`, `./cmd/engram` y E2E de trazabilidad | Números de referencia |
| A-03 | Esqueleto de paquetes `internal/nexo/kb/{domain,ingest,store,search,service,httpapi,client}` | Compila, sin lógica |

## Fase C. Espacios: núcleo

| ID | Tarea | Resultado |
|---|---|---|
| C-01 | Migraciones `nexo_kb_spaces`, `nexo_kb_documents`, `nexo_kb_versions`, `nexo_kb_sections`, `nexo_kb_proposals`, `nexo_kb_events`, `nexo_kb_changes`; configuración de búsqueda `nexo_es` con o sin `unaccent` | Migraciones idempotentes y probadas |
| C-02 | `ingest`: conversor Markdown y texto, front matter, `Sectioner` (títulos, tablas y código sin partir, anclas estables, tamaño objetivo) | Tests con documentos reales de lineamientos |
| C-03 | Servicio de espacios sobre `authz`: crear (con responsable), configurar, archivar, restaurar | CA-K01, CA-K02 |
| C-04 | Publicación por lotes: idempotencia por hash, versiones, secciones, cambios, eventos, poda por origen, `dry-run` | CA-K03, CA-K04, CA-K05 |
| C-05 | Retirar y reemplazar documentos | CA-K09 (parte) |
| C-06 | Búsqueda `pgfts` con vigencia, `at` y `known_at` (`recorded_from`/`recorded_until`), filtros temporal y de permisos comunes a todo `Retriever`, `nexo_kb_index_versions`, lectura de sección y documento (design §14) | CA-K09, RF-20..RF-24 |
| C-10 | Medición: formato `eval/preguntas.yaml` y comando `engram nexo kb eval` (Recall@5, MRR) contra el servidor y la copia local con BM25 (design §14.5) | Línea base de calidad de búsqueda |
| C-07 | Propuestas y revisión, con conflicto de versión | CA-K07, CA-K08 |
| C-08 | Rutas `/nexo/kb/v1` con capacidades y paginación | Tests HTTP por ruta, CA-K06, CA-K13 |
| C-09 | Feed `/sync` con espacios visibles | Tests de feed |

## Fase D. Cliente Nexo

| ID | Tarea | Resultado |
|---|---|---|
| D-01 | Cliente HTTP y copia local `~/.nexo/kb.db` (FTS5), aplicación del feed y borrado de espacios no visibles | CA-K10, CA-K11 |
| D-02 | Herramientas MCP `nexo_search`, `nexo_get`, `nexo_spaces`, `nexo_propose`, `nexo_proposals`, `nexo_review`, `nexo_whoami` (enganche `// NX-10` en `registerTools`) | Tests de contrato de herramientas |
| D-03 | CLI `engram nexo kb publish|search|get|spaces` y `engram nexo kb import-transversal` (enganche en `main.go`) | Publicación desde un repositorio de prueba con CI simulado |
| D-04 | Ejemplo de workflow de GitHub Actions para un repositorio de lineamientos (publicar al hacer merge, `dry-run` en PR) | Plantilla documentada |

## Fase F. Verificación y entrega

| ID | Tarea | Resultado |
|---|---|---|
| F-01 | Prueba E2E en compose propio (`06-pruebas/conocimiento`): superadmin, admin, auditor, Ana responsable, Luis dev, CI publicador; CA-K01..CA-K13 y CA-R01..CA-R11 | `RESULTADOS.md` |
| F-02 | Rendimiento: 1 millón de secciones en servidor, 50 000 locales (RNF-K1, RNF-K2) | Números medidos |
| F-03 | Imagen y binario `3.3.2-nexo.2`; `NEXO_CHANGES.md` con NX-05 y NX-10 | Entregables |
| F-04 | Skill nexo-protocol v0.3 y plugin: usar `nexo_*` para lo transversal; migración del proyecto `nexo-transversal` al espacio `general` | Kit actualizado |
| F-05 | Revisión del usuario y commit | `develop` |

## Estimación orientativa

| Fase | Esfuerzo |
|---|---|
| A. Preparación | 0,5 día |
| C. Espacios núcleo | 6 días |
| D. Cliente Nexo | 4 días |
| F. Verificación y entrega | 2,5 días |
| **Total conocimiento** | **unos 13 días** (más roles R1 y R2: 5 días) |

Después: K2 (Word y Excel), K3 (PDF y PowerPoint), K5 (búsqueda semántica), K6 (pantallas del panel).

## R2. Búsqueda por significado (2026-10-13, rama `feat/r2-hibrido`)

Diseño y desviaciones: `design.md` §20. Todas las tareas están hechas y verificadas, sin commit.

| ID | Tarea | Verificación |
|---|---|---|
| R2-01 | Interfaz `Embedder` y capa común: lotes, reintentos, límites, tiempo máximo, dimensión, normalización y clave borrada en los errores | `embed/embed_test.go`: prueba de contrato con un servidor simulado por proveedor y modelo |
| R2-02 | Adaptadores Gemini, OpenAI, Azure OpenAI y compatible, con la tabla de modelos validada al arrancar | `TestModelTable`; `TestEmbedderLive` con la API real (solo con clave) |
| R2-03 | Migración: extensión `vector` tolerante, `nexo_kb_section_vectors`, HNSW por dimensión (`vector` y `halfvec`), cola y cartas muertas | `r2_pg_test.go`: sin pgvector, dos cuentas y `halfvec` |
| R2-04 | Versiones de índice vectorial y cambio atómico (RC-16) | `TestR2HybridLifecycle`: reconstrucción con la versión anterior en uso y purga |
| R2-05 | Indexador en `engram cloud serve`, alimentado por `nexo_kb_changes`, con reutilización por hash, reintentos y cartas muertas (RC-20) | `TestR2HybridLifecycle`; E2E `nexo-r2` |
| R2-06 | Modo `hybrid`: filtros comunes, HNSW o recorrido exacto, RRF, umbral recalibrado, degradación a texto (RC-11, RC-19, RC-23) | `TestR2HNSWRecallSmallSpace`, `TestR2HybridLifecycle` (permisos, `known_at`, proveedor caído) |
| R2-07 | API, MCP, panel y CLI (`search --mode hybrid`, `eval --retriever hybrid --wait-index`, `reindex --vectors`) | `cli_test.go`; E2E |
| R2-08 | Prueba de calidad en el CI sin red (embedder simulado) | `eval/quality_test.go` `TestSearchQualityGateHybrid` |
| R2-09 | Medición R0 con Gemini: línea base contra el híbrido, 13 configuraciones | `design.md` §20.6; `nexo_ia/06-pruebas/conocimiento/RESULTADOS.md` §5 |
| R2-10 | Documentación: `NEXO_CHANGES`, manuales de producción, local y administración, compose de ejemplo y `THIRD_PARTY_NOTICES` | Revisión |

Pendientes:

- Validar las 50 preguntas con 3 o 4 devs y volver a calibrar los umbrales.
- Aprobación de Real Plaza para enviar datos a un proveedor externo.
- R3 (reordenamiento) si el orden sigue flojo. El MRR de las paráfrasis es 0,43. Hecho: ver la
  sección siguiente.

## R3. Reordenamiento (2026-10-13, rama `feat/r3-rerank`)

Diseño, medición, decisión del valor por defecto y desviaciones: `design.md` §21. Hecho y verificado,
sin fusionar.

| ID | Tarea | Verificación |
|---|---|---|
| R3-01 | Interfaz `Reranker` y capa común: lotes, paralelismo con tope, reintentos con `Retry-After`, RPM, tiempo máximo, validación de cada respuesta y clave borrada en los errores | `rerank/rerank_test.go`: prueba de contrato con un servidor simulado por adaptador |
| R3-02 | Adaptadores Cohere v2 (también Azure AI con `api-key` y URL propia), Jina, Voyage, TEI y compatible | Idem; `TestRerankerLive` (solo con clave; Cohere pendiente) |
| R3-03 | Reordenador LLM: Gemini y API de chat de OpenAI, Azure OpenAI o compatible; prompt con puntaje 0 a 10, JSON estricto, lectura defensiva y lote no válido sin puntaje | Idem; `TestParseScores`; prueba en vivo con Gemini |
| R3-04 | Tubería en la búsqueda: después del filtro y del umbral, TopN, mezcla, descarte opcional, presupuesto y caída segura con `rerank.status` y aviso | `search/rerank_test.go` (sin base de datos) |
| R3-05 | Servicio, API (`rerank=on` u `off`), MCP (`nexo_search`), CLI (`search --rerank`, `eval --rerank`) y estado en `GET /admin/index` | `r3_pg_test.go`; `nexo_response_fields_test.go` |
| R3-06 | Rastro por pregunta y resumen (p50, p95, mejoradas, empeoradas) en `nexo_kb_eval_runs` | `r3_pg_test.go` `TestR3AdminStateAndEvalTrace` |
| R3-07 | Prueba de calidad en el CI sin red (reordenador simulado) | `eval/quality_test.go` `TestSearchQualityGateRerank` |
| R3-08 | Medición R0 con Gemini: línea base contra reordenamiento, 2 modelos, TopN 20 y 50, mezcla, descarte por puntaje | `design.md` §21.5; `nexo_ia/06-pruebas/conocimiento/RESULTADOS.md` §6; `evidencia/r3/` |
| R3-09 | Documentación: `NEXO_CHANGES`, manuales de producción y administración, backlog (RC-13, RC-21, RC-29) | Revisión |

Pendientes:

- Probar Cohere (y Azure AI) con una clave: `TestRerankerLive`.
- Repetir la medición con las preguntas validadas y decidir si se activa por defecto
  (`NEXO_KB_RERANK_DEFAULT=on`).
- Elegir el proveedor de producción y obtener la aprobación de Real Plaza para enviar fragmentos a un
  proveedor externo en cada consulta reordenada.
