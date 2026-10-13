# Espacios de conocimiento (NX-10): tareas

Estado: borrador para revisión. Fecha: 2026-10-12. Rama: `feat/conocimiento` (desde `develop`).
Documentos: `requirements.md`, `design.md`; roles en `docs/nexo/roles/` (NX-05, con sus propias
tareas en `docs/nexo/roles/tasks.md`).

Reglas de trabajo (las mismas de la trazabilidad): código en paquetes `internal/nexo/...`; en archivos
de Engram solo enganches de una línea marcados `// NX-05` o `// NX-10`; tests contra Postgres real con
`CLOUDSTORE_TEST_DSN`; sin commits ni push sin revisión del usuario; sin atribución.

Dependencia: la etapa R1 de roles (motor `internal/nexo/authz`, plataforma y API `/nexo/authz/v1`)
va antes de la fase C.

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
