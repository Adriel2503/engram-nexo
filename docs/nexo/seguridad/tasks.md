# Endurecimiento de seguridad (NX-07): tareas

Estado: borrador para revisión. Fecha: 2026-10-12. Rama: `feat/seguridad` (desde `develop`).
Reglas de trabajo: código en `internal/nexo/security` y archivos `nexo_*.go`; enganches de una línea
marcados `// NX-07`; pruebas contra Postgres real; ediciones con la herramienta Edit; sin commits ni
push sin revisión del usuario; sin atribución.

Orden: S1 -> S2 -> S3 -> S4 -> S5. Cada fase termina con pruebas en verde, verificación y commit
antes de empezar la siguiente.

## Fase S1. Base de datos (2 días)

| ID | Tarea | Criterios |
|---|---|---|
| S1-01 | Inventario: qué tablas actualiza o borra Engram y en qué caminos (para la lista de evidencia) | Documento en design §2.2 |
| S1-02 | `NEXO_MIGRATION_DATABASE_URL`: migrar con la cuenta dueña y operar con la de aplicación; verificación de la cuenta de aplicación al iniciar; aviso de seguridad degradada | CA-S03 |
| S1-03 | `nexoApplyGrants` idempotente con lista de tablas de evidencia | CA-S01 |
| S1-04 | Retención con función `SECURITY DEFINER` y trigger reforzado; quitar el bypass por variable de sesión | CA-S02 |
| S1-05 | `deploy/nexo/postgres-init`, `compose.example.yaml` y guía de TLS | Despliegue reproducible |
| S1-06 | Prueba E2E de trazabilidad con dos cuentas | CA-S15 parcial |

Estado S1 (2026-10-12): S1-01..S1-06 implementadas en `feat/seguridad`, pendientes de revisión y
commit. Inventario en `design.md` §2.2.1, desviaciones en §8, despliegue en `guia-despliegue.md`,
prueba E2E en `06-pruebas/seguridad` del repositorio de Nexo.

## Fase S2. Abuso y escalada (1,5 días)

| ID | Tarea | Criterios |
|---|---|---|
| S2-01 | Límite de peticiones por grupo, IP y token; configuración; 429 con `Retry-After` | CA-S04, RNF-S1 |
| S2-02 | Bloqueo por fallos de autenticación con evento | CA-S05 |
| S2-03 | Sin auto-asignación en `/admin/*`, panel y API Nexo | CA-S06 |
| S2-04 | Redes permitidas para administración de plataforma | CA-S07 |

Estado S2 (2026-10-12): S2-01..S2-04 implementadas en `feat/seguridad`, pendientes de revisión y
commit. Desviaciones en `design.md` §8.2 (D-S2-1..D-S2-7), variables en `guia-despliegue.md` §6,
pruebas: `internal/nexo/security/*_test.go`, `authz/network_test.go`,
`cloudserver/nexo_security_test.go` y E2E `06-pruebas/seguridad` (CA-S04..CA-S07).

## Fase S3. Tokens, NX-06 (1,5 días)

| ID | Tarea | Criterios |
|---|---|---|
| S3-01 | Columnas `nexo_expires_at`, `nexo_scope`; validación en la resolución del token | CA-S08 |
| S3-02 | Alcance `read` en rutas de escritura de Engram y en `authz` | CA-S08 |
| S3-03 | Emisión con caducidad y alcance en `/admin/*`, panel, `/nexo/authz/v1` y bootstrap | RS-20, RS-24 |
| S3-04 | Ajustar la prueba E2E de trazabilidad a tokens con caducidad | CA-S15 |

Estado S3 (2026-10-12): S3-01..S3-04 implementadas en `feat/seguridad`, pendientes de revisión y
commit. Cubre la etapa R4 de roles (NX-06). Desviaciones en `design.md` §8.2 (D-S3-1..D-S3-7).
E2E: `06-pruebas/seguridad/Run-Seguridad.ps1` (CA-S08 con el cliente oficial 3.3.2 y la imagen
`nexo-engram-cloud:3.3.2-nexo.2-dev`; los tokens de las fases nuevas se emiten con caducidad y los
anteriores a NX-07 siguen funcionando sin ella, RS-23).

## Fase S4. Evidencia y monitoreo (3 días)

| ID | Tarea | Criterios |
|---|---|---|
| S4-01 | Cadena de hashes en `cloud_auth_audit_log` y `nexo_authz_events`; verificación por comando y ruta | CA-S09, RNF-S2, RNF-S3 |
| S4-02 | Exportación diaria firmada y su verificación | CA-S10 |
| S4-03 | `nexo_alerts`, evaluador, reglas iniciales, webhook y API | CA-S11 |
| S4-04 | Revisión de accesos, registro de revisiones, deshabilitar inactivos (opcional) | CA-S12 |
| S4-05 | Retención por tabla con funciones seguras | RS-34 |

Estado S4 (2026-10-12): S4-01..S4-05 implementadas en `feat/seguridad` (sobre `develop` db1739e),
pendientes de revisión y commit, junto con los arreglos previos de la revisión: **A1** (un token
revocado o de una persona deshabilitada ya no bloquea a la persona; queda auditado y alerta
`revoked_token_used`) y **A2** (rechazos repetidos: una fila completa y un resumen con el conteo por
ventana, `NEXO_AUDIT_DEDUP_SECONDS`). La cadena cubre también `nexo_kb_events` (NX-10).
Desviaciones en `design.md` §8.4 (A1, A2, D-S4-1..D-S4-14), variables en `guia-despliegue.md` §7,
pruebas: `internal/nexo/security/dedup_test.go`, `internal/nexo/security/evidence/*_test.go`
(CA-S09..CA-S12, RNF-S2, RNF-S3 con `NEXO_CHAIN_PERF_EVENTS`), `auditcli/auditcli_test.go`,
`cloudserver/nexo_evidence_test.go` (HTTP, A1, A2, monitor) y E2E `06-pruebas/seguridad`
(S4-PRIV, A1, A2, CA-S09..CA-S12). `TestPGAuthorizationCostWith100kBindings` (NX-05) pasa a activarse
con `NEXO_AUTHZ_PERF=1`.

## Fase S5. Respuestas y pruebas (1,5 días)

| ID | Tarea | Criterios |
|---|---|---|
| S5-01 | Revisión de respuestas por campo y tipos explícitos | CA-S13 |
| S5-02 | Registro de rutas con capacidad y prueba de cobertura | CA-S14 |
| S5-03 | Prueba de secretos en logs y eventos | CA-S13 |
| S5-04 | Workflow de CI de seguridad | RS-42 |

## Cierre

| ID | Tarea |
|---|---|
| SX-01 | E2E completo (CA-S01..CA-S15) en compose propio `06-pruebas/seguridad` |
| SX-02 | `NEXO_CHANGES.md` (NX-07), imagen `3.3.2-nexo.2` |
| SX-03 | Actualizar `docs/nexo/roles` (R4 cubierta) y `docs/nexo/conocimiento` (tablas de evidencia, límites, registro de rutas) |

**Total orientativo: unos 9,5 días.**
