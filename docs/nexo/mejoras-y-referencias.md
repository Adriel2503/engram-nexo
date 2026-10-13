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
| RC-01 | Conjunto de evaluación real: 50+ preguntas por espacio en cinco clases (con respuesta, sin respuesta, término exacto, temporales y paráfrasis). Acierto comparado por ancla de sección, nunca por subcadena. Métricas: Recall@5, MRR, nDCG@3/10, MAP y acierto de "no hay respuesta", también por clase. Base: la wiki de arquitectura de Azure DevOps (design §17.8), con preguntas borrador que 3 o 4 devs validan y que nunca son la verdad final | Nexo §14.5, Onyx no mide; WeKnora (nDCG, MAP, precisión) | Decidir con datos | R0 | A | hecho en borrador (2b tanda 1: 50 preguntas de la wiki, a validar por 3-4 devs; design §18.3) |
| RC-02 | Prueba de calidad de búsqueda en CI que falle si empeora | Práctica de la industria | Evitar regresiones | R0 | A | hecho (eval/quality_test.go, corpus sintético) |
| RC-03 | Título del documento y ruta de títulos antepuestos al texto indexado de cada fragmento | Contextual retrieval, WeKnora (también en el pasaje del reranker) | Fragmentos entendibles fuera de contexto | R1 | A | hecho (2b tanda 1; medido: sin él, Hit@5 total 73,8 % frente a 76,2 %) |
| RC-04 | Tablas y hojas grandes partidas por filas, repitiendo la cabecera | Utopia (parsers de tablas y Excel), WeKnora (también en Markdown) | Hoy una tabla grande es una sección gigante | R1 / K2 | A | hecho en Markdown y Word (2b tanda 1); hojas de Excel en K2 |
| RC-05 | Hash por sección para no recalcular índices de lo que no cambió | Onyx | Reindexado barato | R1 (columna), R2 (uso) | A | hecho (columna en 2b tanda 1; uso en R2: el indexador reutiliza el vector de un texto idéntico, design §20.3) |
| RC-06 | Sinónimos y siglas desde el glosario del espacio; términos técnicos español/inglés | Nexo | Encuentra "form" y "formulario" | R1 | A | hecho (2b tanda 1: diccionario base y glosario) |
| RC-07 | Umbral de relevancia para responder "no hay estándar oficial" | Hallazgo de la comparación | HU-06; imprescindible al agregar vectores | R1 / R2 | A | hecho sin modelos (2b tanda 1: cobertura ponderada, 0,3); recalibrado en R2 con similitud (0,70 sola, 0,65 con cobertura; "no hay" 50 % -> 88 %, design §20.5) |
| RC-08 | Enlace permanente a la fuente en cada cita. Para la wiki de Azure DevOps, el enlace a la página, configurable por organización, proyecto y wiki (IN-08) | Onyx, WeKnora (citas verificadas contra los resultados) | RN-03, HU-05 | E2b | A | hecho (2b tanda 1; también en las herramientas MCP y la copia local, tanda 2) |
| RC-09 | Máximo de secciones por documento en los resultados (diversidad, MMR) | Onyx / graphiti, WeKnora (MMR por Jaccard de tokens, sin modelos) | Respuestas menos repetidas | R1 | M | hecho como tope por documento (2b tanda 1); MMR pendiente |
| RC-10 | Secciones vecinas como contexto opcional | Práctica de la industria, WeKnora (vecinas para secciones cortas, padre-hijo) | Respuestas más completas | R1 | M | hecho (2b tanda 1, por consulta) |
| RC-11 | Búsqueda híbrida: pgvector + texto, fusión RRF, filtro por espacio compatible con HNSW | Onyx (0,5/0,5), estado del arte, WeKnora (RRF 0,7/0,3, k=60, normalizado, rango por lista) | Paráfrasis y sinónimos | R2 | A si R0 lo pide | hecho (R2: RRF 0,7/0,3, k 60, medido contra 0,5/0,5 y 0,3/0,7; Hit@5 76,2 % -> 92,9 %, design §20.6) |
| RC-12 | Embeddings con proveedor externo: adaptadores OpenAI (`text-embedding-3-small` o `-large`) y Gemini (`gemini-embedding-001`) detrás de la misma interfaz, elegidos por configuración (`NEXO_KB_EMBEDDER`), clave solo en el entorno del servidor, un modelo por versión de índice, lotes con reintentos, caída a búsqueda por palabras si el proveedor no responde. Se miden los dos con R0. Requisito del usuario: poder cambiar de proveedor a futuro sin tocar la búsqueda. Por eso: interfaz `Embedder` común (modelo, dimensiones, textos a vectores) con lotes, reintentos y límites resueltos una vez; un tercer adaptador "compatible con OpenAI" con URL configurable (cubre Azure OpenAI, Ollama, vLLM, TEI y similares); proveedor, modelo y dimensiones registrados en `nexo_kb_index_versions`; cambio de proveedor = índice nuevo construido en segundo plano y activado al completarse, sin mezclar vectores; prueba de contrato común para todos los adaptadores. El modelo local (bge-m3, multilingual-e5) entra por el adaptador compatible | Decisión del usuario (2026-10-13), reemplaza la de §17.2 | Mejor calidad en español sin operar un modelo; costo de centavos para la wiki | R2 | A | hecho (R2: Gemini, OpenAI, Azure OpenAI y compatible; tabla de modelos; prueba de contrato; medido con Gemini 768. Pendiente: aprobación de Real Plaza para enviar datos al proveedor) |
| RC-13 | Reordenamiento local (tipo bge-reranker-v2-m3) sobre los 50 mejores | Estado del arte, WeKnora (pasaje con título y ruta de títulos) | Mejor primer resultado | R3 | M | pendiente |
| RC-14 | Varias formulaciones de la pregunta desde el asistente, fusionadas | Estado del arte, WeKnora (`source_query` y `match_type` por resultado) | Más cobertura sin costo de modelos | R4 | M | pendiente |
| RC-15 | Señal "útil / no útil" por sección que alimente el conjunto de evaluación (no el ranking) | Onyx tiene feedback | Mejora continua medida | E3 | M | pendiente |
| RC-16 | Ciclo de vida del índice con cambio atómico de versión | Onyx, WeKnora (un solo modelo por consulta) | Reindexar sin cortar el servicio | R2 | M | hecho (R2: versiones building/active/retired, construcción en segundo plano, descarte de lo retirado) |
| RC-17 | Mejorar el ranking de texto sin salir del Postgres estándar: FTS nativo, módulos contrib (`unaccent`, `pg_trgm`) y pgvector. Extensiones BM25 de terceros (pg_search de ParadeDB, VectorChord-bm25, pg_textsearch) e índice RUM **descartadas** por decisión del usuario (2026-10-13): Postgres oficial y extensiones estándar, disponibles en servicios administrados | Investigación de motores; decisión del usuario | Si FTS nativo queda corto, se mejora dentro de esos límites | Según medición | B | acotado |
| RC-18 | Grafo temporal derivado de las secciones (relaciones con vigencia y cita); queda al final de la escalera | Utopia, graphiti | Preguntas de varios saltos | R5 | B | pendiente |
| RC-19 | Consulta pgvector con filtro sin perder resultados: subconsulta con LIMIT ampliado (100-200), `SET LOCAL hnsw.ef_search` e `hnsw.iterative_scan` (pgvector >= 0.8), umbral en la consulta externa, índice HNSW parcial por versión de índice sobre la misma expresión que el `ORDER BY`. Cubre también el **filtro temporal**: índice parcial `WHERE is_current` para lo vigente y recorrido exacto para `at`/`known_at` cuando el conjunto filtrado es pequeño. La prueba de recall incluye un espacio pequeño y una consulta histórica (design §17.6) | WeKnora (`retriever/postgres/repository.go:369-469`, migraciones 000002 y 000059) | HU-17 c2, HU-05 c2; evita recorridos secuenciales silenciosos | R2 (diseño ahora) | A | hecho (R2: subconsulta 200, ef_search, iterative_scan relaxed_order, recorrido exacto con pocas secciones y en consultas históricas; índice parcial por dimensión, D-R2-2) |
| RC-20 | Indexador asíncrono de embeddings con cola en Postgres (`FOR UPDATE SKIP LOCKED`, deduplicación por sección y versión del índice, `fail_count`, cartas muertas que se vuelven a encolar a mano, descarte del trabajo superado), alimentado por `nexo_kb_changes`; sin Redis | WeKnora (migración 000041, `asynqdl`) | §14.3 regla 2; define la ventana entre publicar y vectorizar | Diseño ahora (design §17.5), implementación en R2 | M | hecho (R2: nexo_kb_index_jobs, nexo_kb_index_dead, alerta kb_index_dead_letter, indexador en cloud serve) |
| RC-21 | Rerank con resultado registrado y caída segura al orden híbrido si el modelo falla; nunca rebajar el umbral ni conservar el mejor por debajo de él | WeKnora (`internal/reranking/rerank.go`; se descarta su degradación de umbral) | HU-06, disponibilidad | R3 | M | regla escrita (design §17.5); en R2 el umbral nunca conserva el mejor por debajo y la caída del proveedor se informa (vector.status); rerank en R3 |
| RC-22 | Modo de búsqueda exacta o por expresión regular (nombres de componentes, códigos, siglas) en `nexo_search` | WeKnora (`grep_chunks`, `internal/mcpserver/tools_retrieve.go:197-264`) | Términos técnicos que el lematizado o los vectores difuminan | R1 (modo `exact`, design §17.7) | A | hecho (2b tanda 1) |
| RC-23 | Un solo modelo y versión de índice por consulta multiespacio; vector de la consulta calculado una vez | WeKnora (`knowledgebase_search.go:199-247`) | Puntajes comparables entre espacios | R2 | B | hecho (R2: una sola versión activa; vector de la pregunta una vez, con caché) |
| RC-24 | Preguntas sintéticas por sección indexadas como vectores adicionales (solo con LLM local y si R0 lo justifica) | WeKnora (`knowledge_process.go:2423-2458`) | Paráfrasis | Después de R2 | B | pendiente |
| RC-25 | Preguntas temporales en R0: `at` pasado, `known_at`, documento reemplazado, versión programada y documento retirado; con acierto de versión | Bitemporalidad E2a | HU-08, HU-20 c2, HU-30 | R0 | A | hecho en borrador (2b tanda 1) |
| RC-26 | `nexo_search` (MCP y API) acepta `at`; cada resultado muestra vigente desde y hasta, "reemplaza a" y la versión programada si existe | Bitemporalidad E2a | HU-08, RN-03 | E2b | A | hecho (API en 2b tanda 1; `nexo_search` MCP con `at` y estado en tanda 2) |
| RC-27 | Consulta "qué cambió desde X": secciones agregadas, modificadas y retiradas entre dos fechas, calculadas desde las versiones existentes (`/nexo/kb/v1/changes`, modo `changes`) | Bitemporalidad E2a | HU-20 c2, HU-30 | R1 | M | hecho (2b tanda 1) |
| RC-28 | Registro de corridas de evaluación (`nexo_kb_eval_runs`: configuración, versión de índice, conjunto de preguntas, métricas totales y por clase, resultado por pregunta) para ver la evolución y que el CI compare contra la última aceptada | Acuerdo 2026-10-13 | Decidir con datos; RC-02 | R0 | A | hecho (2b tanda 1) |
| RC-29 | Puntos de extensión activables por configuración y medidos con R0: adaptadores de fuente y conversores, modos de búsqueda (`fts`, `exact`, `hybrid`, `changes`), embedders y rerankers (locales por defecto; externos solo con aprobación) | Acuerdo 2026-10-13; patrón de motores de WeKnora | RNF-K8 | E2b en adelante (design §17.7) | A | en curso (modo hybrid y embedders por configuración hechos en R2; rerankers en R3) |
| RC-30 | Búsqueda por significado en la copia local (`kb.db`) cuando el servidor no responde: vectores de cada sección bajados ya calculados en el feed `/sync`, modelo de embeddings en la PC (el **mismo** modelo y versión del servidor, unos 100 a 500 MB) para vectorizar la pregunta, y `sqlite-vec` o comparación en memoria. Solo es posible con un modelo local, no con una API externa | Pregunta del usuario (2026-10-13) | Respaldo sin red con paráfrasis | Después de R2, solo si el piloto muestra mucho uso sin conexión | B | decidido: no por ahora. En R2 los embeddings van solo en el servidor; la copia local sigue con BM25, que ya acierta más en paráfrasis que el servidor sin vectores (57 % frente a 0 %, 2b tanda 2) |
| RC-31 | Interfaz `VectorStore` con pgvector como primer adaptador, para poder sumar otro motor de vectores (Qdrant, Milvus, Elasticsearch) sin tocar la búsqueda, igual que `Embedder` con los proveedores. Referencia: los motores de recuperación de WeKnora (Go, MIT), a reescribir para los espacios, permisos por ámbito y auditoría de Nexo, no a copiar | WeKnora (motores de recuperación por base de vectores); pregunta del usuario (2026-10-13) | Cambiar de base de vectores sin rehacer la búsqueda | Solo si pgvector no alcanza (escala o servicio administrado sin pgvector) | B | decidido: no por ahora. Postgres estándar con pgvector cubre el volumen esperado y evita otro servicio que operar (RC-17) |

## 2. Ingesta y formatos

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| IN-01 | Conversores Word y Excel con bloques y procedencia (hoja, filas) | Plan K2, Utopia | Archivos variados | K2 | A | Word hecho (2b tanda 1); Excel en K2 |
| IN-02 | Detección de PDF escaneado por ausencia de operadores de texto, con mensaje claro | Onyx / Utopia, WeKnora (clasificación por página según el área de imagen) | No indexar PDFs vacíos | K3 | A | pendiente |
| IN-03 | Conversores pesados fuera del proceso del servidor (con límites) | Onyx, WeKnora (docreader por gRPC) | Aislar fallas y memoria | K2 | M | pendiente |
| IN-04 | Conversión de Office vía LibreOffice como respaldo | RAG-Anything | Formatos raros | K2/K3 | B | pendiente |
| IN-05 | Imágenes y diagramas con modelos de visión | RAG-Anything | Diagramas buscables | Posterior | B | pendiente |
| IN-06 | Guardar originales (`BlobStore`) para reconvertir y descargar | D-K11 (diferido) | Reindexar al mejorar un conversor | Cuando se decida | M | pendiente de decisión |
| IN-07 | `source_location` con `quote` (300 caracteres como máximo), `source_hash` (= `source_sha256` de la versión) y `partial` cuando parte de la sección no se pudo mapear al original | WeKnora (`internal/types/source_locator.go`, `internal/sourceloc`) | RN-03, HU-05 c1; cita verificable | R1 (solo `quote`), K2 (el resto) | M | quote hecho (2b tanda 1, al consultar); el resto en K2 |
| IN-08 | Adaptador de fuente para la wiki de Azure DevOps: título desde el nombre de archivo decodificado (`%2D`, `%7C`, guion = espacio); jerarquía de carpetas como ruta de títulos y orden según `.order`; se quitan `[[_TOC_]]` y la numeración de títulos en el ancla; tablas HTML a Markdown; `::: mermaid` como bloque de código; páginas vacías saltadas con aviso; referencias a `/.attachments` conservadas; enlace permanente a la página configurable por organización, proyecto y wiki (RC-08) | Corpus real (design §17.8) | Publicar la wiki de arquitectura tal cual | E2b / R0 | A | hecho (2b tanda 1) |

## 3. Producto de conocimiento

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| KP-01 | Tope de seguridad en la poda (no retirar más de un porcentaje del espacio por ejecución sin confirmación) | Hallazgo de la comparación | Evitar retiros masivos por un lote mal armado | E2b | A | hecho (2b tanda 1) |
| KP-02 | Registro de ejecuciones de publicación (`nexo_kb_publish_runs`) | Onyx (index attempts), WeKnora (pasos por documento e intento) | Observar cada corrida del CI | E2b | A | hecho (2b tanda 1) |
| KP-03 | Metadatos de aplicabilidad (stack, área) e inyección al iniciar sesión solo de lo que aplica al repo | Nexo §14.6 | RNF-05 contexto breve, HU-05 c3 | E3 | A | pendiente |
| KP-04 | Detección de contradicciones entre memorias de proyecto y estándares, evaluadas contra la regla vigente en la fecha de la decisión y contra la de hoy ("contradecía cuando se decidió" frente a "contradice hoy") | Nexo §14.6; bitemporalidad E2a | RN-02, HU-07 c2 | Después de R2 | M | pendiente |
| KP-05 | Adjuntos descargables: definir si esperan a `BlobStore` o se guardan solo diagramas | Contradicción K3 vs D-K11 | Coherencia del plan | Antes de K3 | M | pendiente de decisión |
| KP-06 | Revisión por segunda persona opcional en espacios críticos (`require_second_reviewer`) | ISO 27002 5.3, NIST AC-5 | Separación de funciones | E2b o posterior | B | pendiente |
| KP-07 | La copia local guarda las vigencias e incluye las versiones programadas; filtra al consultar con el reloj local. Sin conexión no responde reglas vencidas, y una programada entra sola en su fecha. Requiere ajustar RF-40 (design §17.9) | Bitemporalidad E2a | HU-08, HU-28, RF-42 | E2b | A | hecho (feed en 2b tanda 1; copia local con vigencia al consultar en tanda 2) |
| KP-08 | Propuestas con fecha de entrada en vigencia (`valid_from` y `valid_until` opcional), que quien revisa puede ajustar al aprobar | Bitemporalidad E2a | HU-29 c1 | E2b | M | hecho (2b tanda 1) |
| KP-09 | Avisos al responsable en `nexo_alerts` (S4): documento por vencer (`valid_until` dentro de N días) y documento sin revisión en `review_every_days` del espacio | Bitemporalidad E2a, alertas S4 | HU-29, HU-30 (mantener vigente) | E2b / K6 | M | hecho (2b tanda 1) |
| KP-10 | Importar el historial git de la wiki como versiones sucesivas de cada página, con la fecha y el autor del commit en `origin` y en `valid_from`; así el eje temporal tiene datos desde el inicio. Pendiente decidir el reloj de registro (design §17.9) | Corpus real (unos 4 300 commits, 2021-2026) | HU-20 c2, preguntas temporales de R0 | E2b / R0 | M | descartado (2026-10-13: el flujo es subir archivos; el historial de la wiki no es requisito) |
| KP-11 | Las carpetas `[TO_REMOVE]` y `≤-2022` se publican y quedan retiradas o vencidas, en vez de omitirlas (`[TO_TIDY]` queda vigente con una etiqueta); el adaptador lo marca con `Hints` | Corpus real | HU-08 (se ve que no está vigente) | E2b | M | hecho (2b tanda 1) |
| KP-12 | Pantalla de miembros de un espacio en el panel: ver quién tiene cada rol y que el responsable o el administrador asigne y quite miembros (hoy solo por la API `/nexo/authz/v1` o la CLI) | Hallazgo de los manuales (2026-10-13) | Administrar espacios sin la API | K6 | M | pendiente |
| KP-13 | Propuestas en el panel: listar, ver, aprobar y descartar (hoy por la API, la CLI o las herramientas `nexo_*`) | Hallazgo de los manuales (2026-10-13) | Revisión sin la terminal | K6 | M | pendiente |

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
| SG-07 | Pantallas del panel para auditor, espacios y revisión | K6 | Usabilidad | K6 | M | en curso (subida, documentos y retiro en el panel, 2b tanda 1; revisión y auditor pendientes) |
| SG-08 | Tokens de MCP y API con alcance restringido a espacios y lista de herramientas permitidas (CI que solo publica en un espacio; asistente de solo lectura) | WeKnora (API keys por base, endpoints MCP con lista de herramientas) | HU-17 c2; menos daño si un token se filtra | E3 (dentro de K4, NX-06) | M | pendiente |
| SG-09 | Escaneo de secretos obligatorio al publicar y al proponer, como control general para toda fuente. Amplía `ingest.DetectSecrets` con: cadenas de conexión de tipo clave=valor (`Password=`, `Pwd=`), pares "usuario/contraseña" en texto, encabezados `Bearer` y `api-key`, y un control de entropía. Por defecto rechaza el documento; enmascarar solo con una opción explícita y un evento (design §17.9). La wiki de arquitectura sirve como caso de prueba porque contiene credenciales de ejemplo (de demostración); nunca se copian a documentos ni preguntas | RN-K4, HU-17 c1; corpus real | No publicar secretos | E2b | A | en curso (detección ampliada en 2b tanda 1; política rechazar o enmascarar pendiente de decisión) |
| SG-10 | Cuentas de servicio para CI y bots: alta desde el panel o la CLI, token sin caducidad o de larga duración emitido solo por superadmin, rotación y vista de su último uso | Hallazgo de los manuales (2026-10-13) | Publicar desde CI sin usar el token de una persona | E3 (K4, NX-06) | M | pendiente |

## 6. Distribución y operación

| ID | Idea | Origen | Mejora | Etapa | Prior. | Estado |
|---|---|---|---|---|---|---|
| OP-01 | Binario propio de Engram para los devs, con aviso de actualización apuntando al fork | D-R6 | Herramientas `nexo_*` en los asistentes | E3 | A | pendiente (requisito para que el kit use las herramientas `nexo_*` de la tanda 2) |
| OP-02 | Instalador que inscribe proyectos según roles (`/nexo/authz/v1/me`) | Hallazgo E2E trazabilidad | Evitar autosync trabado | E3 / E4 | A | pendiente |
| OP-03 | Etiquetar versiones estables `v3.4.0-nexo.N` desde `develop` y pasarlas a `main` (rama estable de Nexo desde el 2026-10-13); la copia del Engram original vive en `engram-upstream` | Flujo del fork | Versiones estables identificables | Al cerrar cada bloque | M | pendiente |
| OP-04 | Ajuste de Postgres para búsqueda a escala (`shared_buffers`, memoria) si p95 supera 300 ms | Medición 2a (312 ms con 1 millón) | Rendimiento | Producción | B | pendiente |
| OP-05 | Migraciones condicionales de extensiones (`vector`): verificar `pg_available_extension_versions` y la versión mínima (0.8), no dejar la migración a medias si falta, índices HNSW grandes con `CONCURRENTLY` fuera de la transacción; confirmar con Real Plaza el proveedor y la versión de Postgres y pgvector | WeKnora (migraciones 000059, 000112, 000115) | R2 en un Postgres administrado | Consulta a Real Plaza ahora; migración en R2 | A | pendiente (consultar) |
| OP-06 | Textos del panel de Engram que siguen en inglés con la marca Nexo: "Create User", "Create Token", "Expires in days (default 90)" y otros de las páginas de usuarios y tokens. Pasarlos por `nx()` exige regenerar `components_templ.go` (templ renumera sus variables: unas 800 líneas de diff en un archivo de Engram) | Hallazgo de los manuales (2026-10-13) | Panel en español completo | Junto con otro cambio que ya regenere `components_templ.go`, o al integrar una versión nueva de Engram | B | pendiente |

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
  de texto del híbrido. Regla general desde el 2026-10-13 (decisión del usuario): solo Postgres oficial, sus
  módulos contrib y pgvector; ninguna extensión de nicho.
- Rerank que rebaja el umbral o conserva el mejor resultado aunque no lo pase (WeKnora): rompe HU-06.
- Documento sin versiones, activación de fragmentos fuera de la versión y Excel con una fila por fragmento
  (WeKnora).

## 8. Decisiones pendientes del usuario

| Tema | Opciones | Recomendación |
|---|---|---|
| Registro de proyectos (PR-01) | (a) rechazar no registrados, (b) registrarlos solos | (a), con registro automático único de los existentes |
| Adjuntos vs originales (KP-05) | Esperar a `BlobStore` o guardar solo diagramas | Esperar a `BlobStore` |
| Modelos de embeddings (RC-12) | Locales o API externa | **Decidido** el 2026-10-13 por el usuario: proveedores externos (OpenAI o Gemini, por configuración). Implica que las secciones y las preguntas se envían al proveedor: informar a Real Plaza antes de producción |
| Contenido para R0 (RC-01) | Lineamientos reales y 10-15 preguntas por dev | Usar la wiki de arquitectura como base y pedir a 3-4 devs que validen las preguntas borrador |
| Reloj de registro del historial importado (KP-10) | Sin efecto | **Descartado** el 2026-10-13 junto con KP-10 |
| Secretos detectados (SG-09) | Rechazar o enmascarar | Rechazar por defecto; enmascarar solo con una opción explícita y un evento |
| Proveedor de Postgres de producción (OP-05) | Versión de Postgres, pgvector >= 0.8, `CREATE EXTENSION`, `CONCURRENTLY` | Consultar a Real Plaza antes de R2 |
| Paso a R2 (vectores) | Empezar R2 o seguir con R1 | R0 (2b tanda 1) muestra la condición de §17.3: paráfrasis 0 % y 3 falsos "no hay" con el umbral. Recomendado empezar R2 tras validar las preguntas con 3-4 devs y la consulta OP-05 |
| Búsqueda por significado en la copia local (RC-30) | En R2 o después | **Decidido** el 2026-10-13: no en R2; embeddings solo en el servidor y la copia local con BM25. Revisar tras el piloto |
| Extensiones de Postgres (RC-17) | Extensiones BM25 de terceros o solo estándar | **Decidido** el 2026-10-13: solo Postgres oficial, contrib y pgvector |
| Umbral de "no hay estándar" (RC-07) | Mantener 0,3 (no hay respuesta 50 %, paráfrasis 0 %) o bajarlo (0,2: no hay respuesta 12 %, menos falsos "no hay") | 0,3 hasta R2; revisar con las preguntas validadas |

## 9. Qué tomar de cada referencia por fase

Informes de origen: `Onyx_vs_Utopia_conocimiento.md`, `Utopia.md` y `WeKnora_conocimiento.md` (en
`nexo_ia/05-investigacion/analisis/`). Entre paréntesis, el código de este backlog.

### 9.1 Por fase pendiente

| Fase | Onyx | Utopia | WeKnora | Otros |
|---|---|---|---|---|
| E2b (propuestas, copia local, MCP, CLI) | Enlace a la fuente en cada resultado y en MCP (RC-08); registro de intentos de indexación (KP-02) | Registro de ejecuciones de sincronización y alertas de fallo (KP-02) | Pasos por documento e intento (KP-02); `source_query` y `match_type` por resultado; endpoint MCP con lista de herramientas (SG-08) | Escaneo de secretos (SG-09); adaptador de la wiki de Azure DevOps (IN-08); vigencias en la copia local (KP-07) |
| R0 (medición) | `run_search_eval.py`: precisión en los primeros k y posición media | Preguntas de competencia (como forma, no el juez LLM) | Métricas nDCG, MAP, MRR, precisión y recall en Go (RC-01); **no** el emparejamiento por subcadena | Corpus de la wiki de arquitectura; corridas guardadas (RC-28); preguntas temporales (RC-25) |
| R1 (sin modelos) | Prefijo de título y metadatos en el texto indexado (RC-03) | Tablas por filas con leyenda y cabecera; un fragmento nunca mezcla procedencias (RC-04) | Tablas por filas con cabecera también en Markdown (RC-04); MMR por Jaccard (RC-09); vecinas para secciones cortas (RC-10); `grep_chunks` (RC-22); `quote` (IN-07) | graphiti: MMR (RC-09) |
| R2 (híbrido) | Ciclo de vida del índice con cambio atómico (RC-16) | `hnsw.iterative_scan = relaxed_order` (ADR 0035); hash por fragmento para reutilizar vectores (RC-05) | Consulta pgvector filtrada e índices HNSW parciales (RC-19); RRF normalizado (RC-11); cola de indexación (RC-20); migraciones condicionales (OP-05); un modelo por consulta (RC-23) | — |
| R3 (reordenamiento) | Nada (no tiene cross-encoder activo) | — | Pasaje con título y ruta de títulos; resultado registrado y caída segura (RC-13, RC-21); **no** la degradación del umbral | Modelo local tipo bge-reranker-v2-m3 (RC-13) |
| R4 (varias formulaciones) | Pesos por tipo de consulta (semántica 1,3; keyword 1,0; original 0,5) (RC-14) | — | Expansión local sin LLM solo cuando hay pocos resultados; `source_query` por resultado | — |
| R5 (grafo) | — | Hechos con relojes del registro y del mundo (como modelo, no como fuente de verdad) | Combinación: el grafo solo aporta secciones y el reordenador decide; **no** su Neo4j sin tiempo | graphiti: aristas `valid_at`/`invalid_at`/`expired_at` (RC-18) |
| K2/K3 (formatos) | pypdfium2 en proceso aislado (IN-03); filas `campo=valor` | Parsers DOCX y XLSX propios: celdas combinadas, cabeceras apiladas, tope de filas con aviso; PDF escaneado por Tj/TJ (IN-01, IN-02) | Escaneado por página (IN-02); docreader aparte por gRPC (IN-03); `source_locators` con hoja, filas, diapositiva y página (IN-07); **no** Excel con una fila por fragmento | RAG-Anything: LibreOffice como respaldo (IN-04); visión para diagramas (IN-05) |
| E3 (cliente y MCP) | Recursos MCP además de herramientas | Herramientas MCP por base de conocimiento | Servidor MCP con mcp-go en HTTP streamable sin estado (mismo stack); citas verificadas contra los resultados (RC-08); tokens limitados a bases (SG-08) | — |
| E4 (roles en proyectos) | Grupos y ACL por documento: **no** copiar (plan de pago, otra granularidad) | Permiso por base de conocimiento | Roles de tenant y bases compartidas con permiso (como contraste); el permiso sobre la base se comprueba antes de buscar | Modelo propio NX-05 (PR-01, PR-02) |

### 9.2 Código reutilizable de WeKnora

**Licencia** (verificada en `repository/WeKnora/LICENSE`): MIT, "Copyright (C) 2026 Tencent. All
rights reserved". Los componentes de terceros listados ahí (OpenCC, anydoc y Wails) conservan su
propia licencia; ninguno está entre los candidatos.

Obligación al portar:
- Cada archivo portado o derivado conserva el aviso de copyright y el permiso MIT de Tencent en un
  comentario de cabecera, con la ruta y el commit de origen (`809bda7`).
- Se agrega una entrada en `NEXO_CHANGES.md` y en un `THIRD_PARTY_NOTICES.md` del fork, con el texto
  MIT completo.

Los archivos de WeKnora no traen cabecera propia, así que el aviso sale de su `LICENSE`.

Rutas relativas a `repository/WeKnora/`.

| Candidato | Origen (archivo:líneas) | Qué hace | Port | Qué cambiar para Nexo |
|---|---|---|---|---|
| Fusión RRF normalizada | `internal/application/service/knowledgebase_search_fusion.go:58-234` | RRF ponderado (k=60; 0,7/0,3), mejor rango por lista, normalizado a [0, 1]; BM25 escalado por el máximo de su lista | Casi tal cual | Tipos de Nexo (sección, ancla, versión); pesos y k desde `nexo_kb_index_versions.config`; el lado de texto es `ts_rank` de `nexo_es`, no BM25 |
| Consulta pgvector filtrada | `internal/application/repository/retriever/postgres/repository.go:291-469` | Subconsulta con LIMIT ampliado (100-200), `SET LOCAL hnsw.ef_search` e `hnsw.iterative_scan`, umbral en la consulta externa, reintento sin los parámetros | Adaptar | pgx en vez de GORM; filtros comunes de Nexo (`space_id = ANY`, vigencia o `is_current`, `at`/`known_at`); índice por `index_version` en vez de por dimensión; comparar `strict_order` con `relaxed_order` en R0 |
| Índices HNSW parciales y migraciones condicionales | `migrations/versioned/000002_embeddings.up.sql:57-76`, `000059_embeddings_hnsw_1024.up.sql:24-45`, `000112:15-24`, `000115_pg_search_02511.up.sql:5-34` | Índice parcial sobre la expresión exacta de la consulta; comprueban que la tabla y la versión de la extensión existan antes de actuar | Adaptar (SQL) | Solo la extensión `vector` (sin `pg_search`); `CONCURRENTLY` fuera de la transacción; registrar el modo en `nexo_kb_index_versions` como ya se hace con `unaccent` |
| Troceo de tablas por filas con cabecera | `internal/infrastructure/chunker/header_tracker.go:1-290`; `internal/infrastructure/chunker/splitter.go:395-575` | Detecta la cabecera de una tabla Markdown y la repite en cada trozo; nunca corta una fila; corta entre tablas | Adaptar | Integrarlo en el `Sectioner` por bloques (no por runas): una tabla se parte en bloques de N filas que conservan `heading_path` y `line_start`/`line_end`; tamaño en palabras como hoy |
| Ubicación en el original | `internal/types/source_locator.go:12-117`; `internal/sourceloc/assign.go:13-42` | Tipos de ubicación (página y caja, bloque, diapositiva, hoja y filas, texto) con `quote` ≤ 300, `source_hash` y `partial`; fusión de ubicaciones contiguas | Adaptar | Nexo ya tiene `source_location`; tomar solo `quote`, `source_hash` y `partial`, y la fusión de rangos. `office.go` y `remap.go` solo si K2 lo necesita |
| Métricas de recuperación | `internal/application/service/metric/{precision,recall,mrr,map,ndcg}.go` (41-78 líneas cada uno) | Precision, Recall, MRR, MAP y nDCG@k con relevancia binaria | Casi tal cual | Identificadores = ancla y versión; corregir MAP para dividir por el total de relevantes (WeKnora divide por los encontrados); sin jieba |
| Cola de indexación | `migrations/versioned/000041_task_queue_and_wiki_indexes.up.sql:43-64` (cola), `:89-111` (cartas muertas); `internal/middleware/asynqdl/asynqdl.go:66-124` | Cola durable con clave de deduplicación, `fail_count`, consumo `FOR UPDATE SKIP LOCKED` y recuperación de trabajos abandonados; cartas muertas con la carga intacta | Adaptar (solo el diseño SQL; asynq no se usa) | `nexo_kb_index_jobs` y `nexo_kb_index_dead` (design §17.5); sin `tenant_id`; alerta en `nexo_alerts` |
| Búsqueda por expresión regular | `internal/mcpserver/tools_retrieve.go:197-264`; filtro en `internal/agent/tools/search_knowledge.go:118-137` | Extrae literales, busca por texto y filtra los resultados con la expresión (sin distinguir mayúsculas) | Adaptar | Modo `exact` del `Retriever`; preselección con FTS `simple` o `ILIKE` dentro de los espacios visibles; límite de longitud de la expresión |
| MMR | `internal/reranking/mmr.go:11-124` | Selección por Maximal Marginal Relevance con Jaccard de tokens e índice invertido (sin embeddings) | Casi tal cual | Tokenizar con la normalización de Nexo (minúsculas, sin tildes) en vez de jieba; tope por documento (RC-09) como alternativa más simple |
| Pasaje para el reordenador | `internal/reranking/passage.go:115-187`; `internal/application/service/knowledge_index_content.go:12-21` | Título + ruta de títulos + cuerpo como texto indexado y como pasaje del reordenador | Idea (pocas líneas) | Usar `heading_path` y el título de la versión |
