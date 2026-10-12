# Roles: tareas

Estado: borrador para revisión. Fecha: 2026-10-12. Rama: `feat/conocimiento`.
Documentos: `requirements.md`, `design.md` (NX-05). Se implementa antes y junto con los espacios
(`docs/nexo/conocimiento/tasks.md`), porque estos dependen del motor de roles.

## Etapa R1. Motor y plataforma

| ID | Tarea | Resultado |
|---|---|---|
| R1-01 | `internal/nexo/authz`: tipos, tabla de capacidades por nivel, herencia de roles, alcance de token | Tests de la matriz completa |
| R1-02 | Migraciones `nexo_scopes`, `nexo_role_bindings`, `nexo_authz_events`; triggers de combinaciones e inmutabilidad; fila de plataforma; arranque del superadmin | Tests de store, idempotencia |
| R1-03 | `EffectiveRole`, `Can`, `VisibleScopes` con compatibilidad de Engram (admin, `cloud_project_grants`) | Tests R-1..R-15 |
| R1-04 | Adaptador de identidad en `cloudserver` (`nexo_authz_bridge.go`) | Identidad en contexto para Nexo |
| R1-05 | API `/nexo/authz/v1` (me, plataforma, ámbitos, miembros, recuperación, eventos) | CA-R01, CA-R03, CA-R04, CA-R08, CA-R10 |
| R1-06 | Enganches `// NX-05` en la administración de Engram (`requireManagedAdmin`, crear usuario, habilitar y deshabilitar, panel) | CA-R02 |

## Etapa R2. Proyectos: lectura y escritura

| ID | Tarea | Resultado |
|---|---|---|
| R2-01 | Variante de `authorizeProjectScope` con capacidad; `project.read` en descargas, `project.write.own` en subidas (paquetes y cambios) | CA-R07 |
| R2-02 | Descarga de cambios filtrada por `VisibleScopes` con `project.read` | Lector no recibe proyectos sin rol |
| R2-03 | Cliente Nexo: inscribe según `/me` y marca conflicto local si pierde escritura | Autosync sin trabas |
| R2-04 | E2E de trazabilidad sin cambios con cliente oficial 3.3.2 | CA-R11 |

## Etapa R3. Proyectos: propiedad de memorias

| ID | Tarea | Resultado |
|---|---|---|
| R3-01 | `project.write.any` en subida de cambios: autor original por `entity_key` | Proponente no cambia memorias de otros |
| R3-02 | Lo mismo en subida de paquetes (decodificación existente) | Ambas rutas cubiertas |
| R3-03 | Cliente Nexo: separa entradas sobre memorias ajenas sin permiso y las deja como conflicto local | Sin lotes rechazados |

## Etapa R4. Tokens (NX-06)

| ID | Tarea | Resultado |
|---|---|---|
| R4-01 | `scope` y `expires_at` en `cloud_principal_tokens`, emisión con 90 días por defecto, validación en cada petición | Token `read` sin escritura; token vencido rechazado |

## Estimación orientativa

| Etapa | Esfuerzo |
|---|---|
| R1 | 3 días |
| R2 | 2 días |
| R3 | 2 días |
| R4 | 1 día |
