# Espacios de conocimiento (NX-10): tareas

Estado: borrador para revisión. Fecha: 2026-10-12. Rama: `feat/conocimiento` (desde `develop`).
Documentos: `requirements.md`, `design.md`; roles en `docs/nexo/roles/` (NX-05, con sus propias
tareas en `docs/nexo/roles/tasks.md`).

Reglas de trabajo (las mismas de la trazabilidad): código en paquetes `internal/nexo/...`; en archivos
de Engram solo enganches de una línea marcados `// NX-05` o `// NX-10`; tests contra Postgres real con
`CLOUDSTORE_TEST_DSN`; sin commits ni push sin revisión del usuario; sin atribución.

Dependencia: la etapa R1 de roles (motor `internal/nexo/authz`, plataforma y API `/nexo/authz/v1`)
va antes de la fase C.

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
| C-06 | Búsqueda `pgfts` con vigencia y `at`, lectura de sección y documento | CA-K09, RF-20..RF-24 |
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
