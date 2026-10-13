# Mejoras e inspiración: lo que encontramos para hacer mejor a Nexo

Estado: documento vivo. Creado: 2026-10-12. Fuente única de ideas pendientes; cada agente y cada
especificación nueva lo consulta antes de diseñar. Cuando una idea se implementa, se marca como hecha
y se enlaza el commit o la especificación.

Fuentes:
- Comparación Onyx / Utopia / Nexo: `nexo_ia/05-investigacion/analisis/Onyx_vs_Utopia_conocimiento.md`.
- Análisis previo de Utopia: `nexo_ia/05-investigacion/analisis/Utopia.md`.
- Investigación de seguridad (OWASP, ISO/IEC 27002, NIST):
  `nexo_ia/05-investigacion/claudedocs/research_seguridad_bd_permisos_2026-10-12.md`.
- Revisiones de código de las entregas (reportes y desviaciones en cada `design.md`).
- Repositorios de referencia en `nexo_ia/repository/`: onyx, utopia, graphiti, RAG-Anything,
  basic-memory, openmemory, AgentMemory, mcp-server-qdrant, rag-knowledge-base, WeKnora (Tencent,
  MIT, Go + pgvector/ParadeDB, descargado 2026-10-12, analizado:
  `nexo_ia/05-investigacion/analisis/WeKnora_conocimiento.md`).

Leyenda de prioridad: **A** alta, **M** media, **B** baja. Estado: pendiente, en curso, hecho.

## 1. Recuperación e indexación (el valor principal para los devs)

Detalle técnico en `docs/nexo/conocimiento/design.md` §14 y §17.

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| RC-01 | Conjunto de evaluación real: 50+ preguntas de devs por espacio, incluidas preguntas sin respuesta; Recall@5, MRR, nDCG y acierto de "no hay respuesta" | Nexo §14.5, Onyx no mide; WeKnora (nDCG@3/10, MAP, precisión; emparejar por ancla, no por subcadena) | Decidir con datos | R0 | A | pendiente (falta contenido real) |
| RC-02 | Prueba de calidad de búsqueda en CI que falle si empeora | Práctica de la industria | Evitar regresiones | R0 | A | pendiente |
| RC-03 | Título del documento y ruta de títulos antepuestos al texto indexado de cada fragmento | Contextual retrieval, WeKnora (también en el pasaje del reranker) | Fragmentos entendibles fuera de contexto | R1 | A | pendiente |
| RC-04 | Tablas y hojas grandes partidas por filas, repitiendo la cabecera | Utopia (parsers de tablas y Excel), WeKnora (también en Markdown) | Hoy una tabla grande es una sección gigante | R1 / K2 | A | pendiente |
| RC-05 | Hash por sección para no recalcular índices de lo que no cambió | Onyx | Reindexado barato | R1 (columna), R2 (uso) | A | pendiente |
| RC-06 | Sinónimos y siglas desde el glosario del espacio; términos técnicos español/inglés | Nexo | Encuentra "form" y "formulario" | R1 | A | pendiente |
| RC-07 | Umbral de relevancia para responder "no hay estándar oficial" | Hallazgo de la comparación | HU-06; imprescindible al agregar vectores | R1 / R2 | A | pendiente |
| RC-08 | Enlace permanente a la fuente en cada cita | Onyx, WeKnora (citas verificadas contra los resultados) | RN-03, HU-05 | E2b | A | pendiente |
| RC-09 | Máximo de secciones por documento en los resultados (diversidad, MMR) | Onyx / graphiti, WeKnora (MMR por Jaccard de tokens, sin modelos) | Respuestas menos repetidas | R1 | M | pendiente |
| RC-10 | Secciones vecinas como contexto opcional | Práctica de la industria, WeKnora (vecinas para secciones cortas, padre-hijo) | Respuestas más completas | R1 | M | pendiente |
| RC-11 | Búsqueda híbrida: pgvector + texto, fusión RRF, filtro por espacio compatible con HNSW | Onyx (0,5/0,5), estado del arte, WeKnora (RRF 0,7/0,3, k=60, normalizado, rango por lista) | Paráfrasis y sinónimos | R2 | A si R0 lo pide | pendiente |
| RC-12 | Modelos abiertos multilingües en nuestro servidor (embeddings tipo bge-m3 o multilingual-e5) | Decisión §17.2 | Los documentos no salen de la empresa | R2 | A | pendiente |
| RC-13 | Reordenamiento local (tipo bge-reranker-v2-m3) sobre los 50 mejores | Estado del arte, WeKnora (pasaje con título y ruta de títulos) | Mejor primer resultado | R3 | M | pendiente |
| RC-14 | Varias formulaciones de la pregunta desde el asistente, fusionadas | Estado del arte, WeKnora (`source_query` y `match_type` por resultado) | Más cobertura sin costo de modelos | R4 | M | pendiente |
| RC-15 | Señal "útil / no útil" por sección que alimente el conjunto de evaluación (no el ranking) | Onyx tiene feedback | Mejora continua medida | E3 | M | pendiente |
| RC-16 | Ciclo de vida del índice con cambio atómico de versión | Onyx, WeKnora (un solo modelo por consulta) | Reindexar sin cortar el servicio | R2 | M | pendiente |
| RC-17 | Extensión BM25 en Postgres (pg_textsearch, VectorChord-bm25 o pg_search) o índice RUM | Investigación de motores | Si FTS nativo queda corto o lento a escala | Según medición | B | pendiente |
| RC-18 | Grafo temporal derivado de las secciones (relaciones con vigencia y cita) | Utopia, graphiti | Preguntas de varios saltos | R5 | B | pendiente |
| RC-19 | Consulta pgvector con filtro sin perder resultados: subconsulta con LIMIT ampliado (100-200), `SET LOCAL hnsw.ef_search` e `hnsw.iterative_scan` (pgvector >= 0.8), umbral en la consulta externa, índice HNSW parcial por versión de índice sobre la misma expresión que el `ORDER BY`; prueba de recall con un espacio pequeño | WeKnora (`retriever/postgres/repository.go:369-469`, migraciones 000002 y 000059) | HU-17 c2, HU-05 c2; evita recorridos secuenciales silenciosos | R2 (diseño ahora) | A | pendiente |
| RC-20 | Indexador asíncrono de embeddings con cola en Postgres (`FOR UPDATE SKIP LOCKED`, deduplicación por sección y versión del índice, `fail_count`, cartas muertas que se vuelven a encolar a mano, descarte del trabajo superado), alimentado por `nexo_kb_changes`; sin Redis | WeKnora (migración 000041, `asynqdl`) | §14.3 regla 2; define la ventana entre publicar y vectorizar | R2 | M | pendiente |
| RC-21 | Rerank con resultado registrado y caída segura al orden híbrido si el modelo falla; nunca rebajar el umbral ni conservar el mejor por debajo de él | WeKnora (`internal/reranking/rerank.go`; se descarta su degradación de umbral) | HU-06, disponibilidad | R3 | M | pendiente |
| RC-22 | Modo de búsqueda exacta o por expresión regular (nombres de componentes, códigos, siglas) en `nexo_search` | WeKnora (`grep_chunks`, `internal/mcpserver/tools_retrieve.go:197-264`) | Términos técnicos que el lematizado o los vectores difuminan | E2b / R1 | M | pendiente |
| RC-23 | Un solo modelo y versión de índice por consulta multiespacio; vector de la consulta calculado una vez | WeKnora (`knowledgebase_search.go:199-247`) | Puntajes comparables entre espacios | R2 | B | pendiente |
| RC-24 | Preguntas sintéticas por sección indexadas como vectores adicionales (solo con LLM local y si R0 lo justifica) | WeKnora (`knowledge_process.go:2423-2458`) | Paráfrasis | Después de R2 | B | pendiente |

## 2. Ingesta y formatos

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| IN-01 | Conversores Word y Excel con bloques y procedencia (hoja, filas) | Plan K2, Utopia | Archivos variados | K2 | A | pendiente |
| IN-02 | Detección de PDF escaneado por ausencia de operadores de texto, con mensaje claro | Onyx / Utopia, WeKnora (clasificación por página según el área de imagen) | No indexar PDFs vacíos | K3 | A | pendiente |
| IN-03 | Conversores pesados fuera del proceso del servidor (con límites) | Onyx, WeKnora (docreader por gRPC) | Aislar fallas y memoria | K2 | M | pendiente |
| IN-04 | Conversión de Office vía LibreOffice como respaldo | RAG-Anything | Formatos raros | K2/K3 | B | pendiente |
| IN-05 | Imágenes y diagramas con modelos de visión | RAG-Anything | Diagramas buscables | Posterior | B | pendiente |
| IN-06 | Guardar originales (`BlobStore`) para reconvertir y descargar | D-K11 (diferido) | Reindexar al mejorar un conversor | Cuando se decida | M | pendiente de decisión |
| IN-07 | `source_location` con `quote` (300 caracteres como máximo), `source_hash` (= `source_sha256` de la versión) y `partial` cuando parte de la sección no se pudo mapear al original | WeKnora (`internal/types/source_locator.go`, `internal/sourceloc`) | RN-03, HU-05 c1; cita verificable | K2 | M | pendiente |

## 3. Producto de conocimiento

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| KP-01 | Tope de seguridad en la poda (no retirar más de un porcentaje del espacio por ejecución sin confirmación) | Hallazgo de la comparación | Evitar retiros masivos por un lote mal armado | E2b | A | pendiente |
| KP-02 | Registro de ejecuciones de publicación (`nexo_kb_publish_runs`) | Onyx (index attempts), WeKnora (pasos por documento e intento) | Observar cada corrida del CI | E2b | A | pendiente |
| KP-03 | Metadatos de aplicabilidad (stack, área) e inyección al iniciar sesión solo de lo que aplica al repo | Nexo §14.6 | RNF-05 contexto breve, HU-05 c3 | E3 | A | pendiente |
| KP-04 | Detección de contradicciones entre memorias de proyecto y estándares | Nexo §14.6 | RN-02, HU-07 c2 | Después de R2 | M | pendiente |
| KP-05 | Adjuntos descargables: definir si esperan a `BlobStore` o se guardan solo diagramas | Contradicción K3 vs D-K11 | Coherencia del plan | Antes de K3 | M | pendiente de decisión |
| KP-06 | Revisión por segunda persona opcional en espacios críticos (`require_second_reviewer`) | ISO 27002 5.3, NIST AC-5 | Separación de funciones | E2b o posterior | B | pendiente |

## 4. Proyectos (memoria de equipos)

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| PR-01 | Registro de proyectos `nexo_projects` (nombre, descripción, responsable, repo) unido por nombre a las tablas de Engram | Pedido del usuario | Proyectos administrables como los espacios; sin variable ni reinicio para habilitar uno | E4 | A | pendiente de decisión: rechazar no registrados (recomendado) o registrarlos solos |
| PR-02 | Roles en proyectos: lector descarga, proponente cambia lo suyo, publicador lo de otros | `docs/nexo/roles` R2/R3 | Gobierno de la memoria de equipos | E4 | A | pendiente |

## 5. Seguridad y gobierno

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| SG-01 | Respuestas sin campos internos (BOPLA), registro de rutas con su capacidad, prueba de secretos en logs, pruebas de seguridad en CI | Plan S5, OWASP API | Defensa en profundidad | S5 (pospuesta) | M | pendiente |
| SG-02 | Segundo factor para superadmin vía OIDC corporativo | ASVS 8.4.2, ISO 8.5 | Administración más segura | Cuando haya OIDC | M | pendiente |
| SG-03 | Row-Level Security de Postgres en tablas de conocimiento | OWASP DB CS | Un error de código no filtra datos | Posterior | B | pendiente |
| SG-04 | Límite de peticiones compartido entre réplicas (proxy o almacén común) | Riesgo D-S2 | Con varias réplicas el límite se multiplica | Al escalar | B | pendiente |
| SG-05 | Prueba de punta a punta con TLS hacia Postgres | Riesgo S1 | Validar `sslmode=verify-full` | Antes de producción | M | pendiente |
| SG-06 | Renovación del token del superadmin inicial (vence a 90 días) y segundo superadmin | Riesgo S3 | No perder acceso | Operación | A | pendiente |
| SG-07 | Pantallas del panel para auditor, espacios y revisión | K6 | Usabilidad | K6 | M | pendiente |
| SG-08 | Tokens de MCP y API con alcance restringido a espacios y lista de herramientas permitidas (CI que solo publica en un espacio; asistente de solo lectura) | WeKnora (API keys por base, endpoints MCP con lista de herramientas) | HU-17 c2; menos daño si un token se filtra | E3 | M | pendiente |

## 6. Distribución y operación

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| OP-01 | Binario propio de Engram para los devs, con aviso de actualización apuntando al fork | D-R6 | Herramientas `nexo_*` en los asistentes | E3 | A | pendiente |
| OP-02 | Instalador que inscribe proyectos según roles (`/nexo/authz/v1/me`) | Hallazgo E2E trazabilidad | Evitar autosync trabado | E3 / E4 | A | pendiente |
| OP-03 | Pasar `develop` a `main` y etiquetar versiones `v3.3.2-nexo.N` | Flujo del fork | Versiones estables identificables | Al cerrar cada bloque | M | pendiente |
| OP-04 | Ajuste de Postgres para búsqueda a escala (`shared_buffers`, memoria) si p95 supera 300 ms | Medición 2a (312 ms con 1 millón) | Rendimiento | Producción | B | pendiente |
| OP-05 | Migraciones condicionales de extensiones (`vector`): verificar `pg_available_extension_versions` y la versión mínima (0.8), no dejar la migración a medias si falta, índices HNSW grandes con `CONCURRENTLY` fuera de la transacción; confirmar con Real Plaza el proveedor y la versión de Postgres y pgvector | WeKnora (migraciones 000059, 000112, 000115) | R2 en un Postgres administrado | R2 (consulta ahora) | M | pendiente |

## 7. Lo que decidimos no copiar (y por qué)

- Infraestructura de Onyx (OpenSearch, servidores de modelos, colas, unos 12 contenedores): costo de
  operación desproporcionado para nuestro volumen.
- Sincronización de permisos desde las fuentes y conectores masivos: el conocimiento de Nexo se
  publica y gobierna en Nexo.
- Decaimiento por antigüedad: un estándar antiguo vigente sigue valiendo.
- Que la señal de los usuarios mueva el ranking: se usa para evaluar, no para ordenar.
- Extracción de hechos con LLM en la ingesta y hechos como fuente de verdad (Utopia): la fuente de
  verdad es el documento aprobado.
- Glean u otra plataforma SaaS: no aplica nuestro modelo de gobierno y los datos quedan en su nube.
- ParadeDB `pg_search` con tokenizador chino (WeKnora): sin raíces en español, imagen y versión propias,
  rara vez disponible en un Postgres administrado y con licencia AGPL; el FTS nativo `nexo_es` cubre el lado
  de texto del híbrido.
- Rerank que rebaja el umbral o conserva el mejor resultado aunque no lo pase (WeKnora): rompe HU-06.
- Documento sin versiones, activación de fragmentos fuera de la versión y Excel con una fila por fragmento
  (WeKnora).

## 8. Decisiones pendientes del usuario

| Tema | Opciones | Recomendación |
|---|---|---|
| Registro de proyectos (PR-01) | (a) rechazar no registrados, (b) registrarlos solos | (a), con registro automático único de los existentes |
| Adjuntos vs originales (KP-05) | Esperar a `BlobStore` o guardar solo diagramas | Esperar a `BlobStore` |
| Modelos de embeddings (RC-12) | Locales o API externa | Locales; API externa solo con aprobación |
| Contenido para R0 (RC-01) | Lineamientos reales y 10-15 preguntas por dev | Pedir a 3-4 devs |
