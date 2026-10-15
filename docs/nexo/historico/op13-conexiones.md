# OP-13: conexiones a Postgres y límite del pool

> **Informe histórico archivado el 2026-10-15.** Conserva la evidencia original, incluidos
> bloques repetidos y fragmentos de comandos incompletos; no es un procedimiento ejecutable.
> Los estados de rama y pruebas pertenecen a esa entrega. Consulta el
> [resumen](../README.md#3-conexiones-postgresql-op-13) y el [manual de operación](../manuales/servidor-produccion.md).

Corrección del 2026-10-14 en `fix/op13-conexiones`, desde `develop` (`5f7b565`).
Tres defectos, cinco rutas. Las cinco regresiones fallaron por bloqueo sobre el código original
y pasan después de corregir. Sin cambios al esquema SQLite.

## Casos encontrados

Las líneas corresponden al código corregido; se indica expresamente cuando son del original.

| Caso | Ubicación y cadena | Corrección |
|---|---|---|
| Publicación de conocimiento | `internal/nexo/kb/service/publish.go:416`, `publish.go:490` → `internal/nexo/kb/store/store.go:535` → catálogo en `internal/nexo/kb/store/migrate.go:289` | `Service.Initialize` (`service.go:248`) resuelve el modo antes de entregar el servicio; `inTx` (`service.go:253`) lo verifica antes de abrir tx. `UnaccentVector` recibe el mismo `Querier` de `InsertSections` |
| Aprobación de propuestas | `internal/nexo/kb/service/proposals.go:285`, `proposals.go:302` → mismo `writeVersion` e `InsertSections` | Mismo arreglo compartido, con regresión independiente que verifica aprobación y publicación |
| Mutex del modo frío | Original: `internal/nexo/kb/store/store.go:45` retenía `modeMu` durante la consulta con `s.DB`; `IndexOpts` lo esperaba dentro de tx | `foldInApp` (`store.go:51`) libera el mutex antes de consultar (`store.go:59`). Solo lo adquiere para leer/publicar la caché. `IndexOpts` (`store.go:469`) ya no espera un mutex retenido durante SQL |
| Fallback tras fallar `hnsw.ef_search` | `internal/nexo/kb/search/vector.go:120` → `searchPlain` → consulta del pool en `vector.go:141`; el rollback diferido retenía la primera conexión | Rollback explícito y completo en `vector.go:122` antes del fallback; devuelve el error si rollback falla |
| Fallback tras fallar `hnsw.iterative_scan` | `internal/nexo/kb/search/vector.go:128` → mismo `searchPlain` | Rollback completo en `vector.go:130` antes del fallback |
| Relleno de sesiones al migrar | Original: `internal/cloud/cloudstore/cloudstore.go:523` abría un cursor y `cloudstore.go:535` insertaba con `cs.db` sin cerrarlo | Delegación marcada en `cloudstore.go:523`. `internal/cloud/cloudstore/nexo_op13_pool.go` lee páginas de 512 claves `(project_name, chunk_id)`, cierra y comprueba el cursor, y por cada clave lee su payload solo, lo indexa y lo suelta (`nexoBackfillChunk`). Sin OFFSET ni acumulación de payloads; marca `nexo_backfill_marks` al terminar una pasada completa |
| Choque de unicidad en `WriteChunk` | `internal/cloud/cloudstore/cloudstore.go:253`, `cloudstore.go:273`, `cloudstore.go:275` → `resolveChunkConflict` | Caso histórico ya corregido: rollback y `tx = nil` antes de otra consulta. Conserva `NX-07 (pool)`; cubierto también en la batería mixta |

El mutex amplifica el defecto compartido de publicación/aprobación; no es un cuarto defecto.
No se encontraron otros casos activos del patrón en los caminos Postgres revisados.

## Inventario verificado y descartes

Se contrastó el inventario previo con los puntos de apertura de transacciones, consultas,
conexiones fijas, locks y cachés en los caminos Postgres de `internal/cloud` e `internal/nexo`,
siguiendo helpers y callbacks. Los 28 puntos de `Begin`/`BeginTx` del inventario se mantienen;
el cambio añade la lectura paginada en un archivo Nexo.

| Punto revisado | Evidencia |
|---|---|
| `VectorSupport` | `internal/nexo/kb/store/vectors.go:203`, `vectors.go:209`: libera el mutex antes de SQL. `SaveVectors` consulta en `vectors.go:620` antes de abrir tx en `vectors.go:629`; los demás invocadores tampoco retienen tx/cursor. El resultado disponible no expira por el temporizador de cinco minutos |
| Lista de tokens → hechos Nexo | `internal/cloud/cloudstore/identity.go:730`, `identity.go:746`: cursor agotado antes de `nexoFillTokenFacts` |
| Lista de cuentas de servicio → bindings | `internal/nexo/authz/serviceaccounts.go:313`, `serviceaccounts.go:336`: cursor agotado antes de la segunda consulta |
| `BackfillMutationChunks` | `internal/cloud/cloudstore/cloudstore.go:1018`, `cloudstore.go:1072`: EOF antes de abrir tx |
| Comparación de exportaciones | `internal/nexo/security/evidence/export.go:492`, `export.go:513`: EOF antes de consultar otra vez |
| Reindexado de texto | `internal/nexo/kb/store/reindex.go:139`, `reindex.go:147`, `reindex.go:155`: cursor cerrado antes del modo y de la tx |
| Cola e índices vectoriales | `EnsureVectorIndex` libera tx antes de `findVectorIndex`; `FailVectorJobs` confirma antes de escribir alertas. Helpers con el mismo ejecutor |
| Autorización y cachés | SQL antes de tx; denegaciones después de retornar `inTx`. Caché de identidad sin mutex durante SQL |
| Auditoría y deduplicación | `cloud_auth_audit_log` y `nexo_authz_events` conservan el ejecutor recibido, incluso para resúmenes. Cadena y locks con la misma tx |
| Actividad, alertas y webhooks | Actividad después de `WriteChunk`/`InsertMutationBatch`; evaluación con la misma tx y entrega de webhooks después de confirmar |
| Actor y middleware | `internal/cloud/cloudstore/nexo_actor.go:104`: `set_config(..., true)` con la tx de escritura. Sin middleware que retenga una conexión durante el handler |
| Otras cachés | Read model del dashboard, sinónimos, índice vectorial activo, monitor y flusher de rechazos consultan fuera de sus mutex |
| Conexiones y locks | Sin `db.Conn` ni locks consultivos de sesión en los caminos Postgres de producción. Locks consultivos transaccionales con la misma tx |
| Pepper y blobs | Pepper de configuración y `NoopBlobStore` sin SQL |
| SQLite local | Fuera del pool Postgres: `internal/store` y `internal/nexo/kb/client/local.go`; sin cambios |

EOF libera la conexión de `database/sql.Rows`: un `defer rows.Close()` pendiente
después de agotar el cursor no significa que la conexión siga retenida.

## Pruebas y evidencia de regresión

Se usó la variable exacta `CLOUDSTORE_TEST_DSN`, la base exclusiva `nexoop13`
y el puerto 55437. La cadena de conexión y su contraseña no se registran.
Las regresiones aplican `db.SetMaxOpenConns(1)` después de preparar los datos.
Un temporizador externo cancela solo para limpiar; timeout, error de contexto o cuelgue son fallos.

| Prueba nueva | Sin corrección | Corregida |
|---|---|---|
| `TestNexoOP13PublishColdModeSingleConnection` | Código original: bloqueo hasta el temporizador de 5 s | Publicación creada con caché fría al iniciar, contexto vigente y pool libre |
| `TestNexoOP13ApproveColdModeSingleConnection` | Código original: bloqueo de 5 s | Propuesta aprobada y documento creado, contexto vigente y pool libre |
| `TestNexoOP13VectorFallbackSingleConnection/SET_LOCAL_hnsw.ef_search` | Código original: bloqueo de 5 s | Wrapper del driver de prueba inyecta error SQL real `SELECT 1/0` con contexto vigente; fallback devuelve el vector esperado y libera el pool |
| `TestNexoOP13VectorFallbackSingleConnection/SET_LOCAL_hnsw.iterative_scan` | Código original: bloqueo de 5 s en la segunda rama | Primera sentencia válida y error real en la segunda; mismo resultado correcto |
| `TestNexoOP13BackfillSingleConnection` | Código original: bloqueo de 5 s; confirmado nuevamente con el temporizador definitivo de 15 s | 514 chunks en dos proyectos, mismas claves entre proyectos, varias páginas, 514 sesiones; referencias de prompts/observaciones sin sesiones implícitas; repetición idempotente y pool libre |
| `TestNexoOP13ColdModeDoesNotLockPoolWait` | Prueba adicional del ciclo mutex/pool | Tx retiene la única conexión; `WaitCount` confirma una consulta fría externa esperando el pool. `IndexOpts` y el modo operan mediante tx sin otra adquisición; ambas operaciones terminan |
| `TestNexoOP13ConcurrentMixedPoolFour` | Batería adicional | 24 goroutines, barrera de preparación/arranque, pool 4, cachés frías: cuatro publicaciones, cuatro aprobaciones, cuatro búsquedas híbridas con vectores activos, cuatro subidas del mismo chunk, cuatro tokens con auditoría y cuatro auditorías directas. Efectos persistidos correctos y cero conexiones en uso |

Archivos nuevos: `nexo_op13_pool_test.go` en `kb/service`, `kb/search`,
`kb/store` y `cloudstore`. También se actualizaron pruebas de configuración,
log y estado del pool, incluida la conservación del `0` explícito.

Comando de las regresiones:

```text
go test -p 1 -count=1 -timeout=120s -run TestNexoOP13 -v ./internal/nexo/kb/store ./internal/nexo/kb/service ./internal/nexo/kb/search ./internal/cloud/cloudstore
```

Se ejecutó primero el código original con las cinco regresiones iniciales:
las dos pruebas de servicio y las dos ramas vectoriales fallaron a los 5 s;
el backfill también a los 5 s, y en una segunda comprobación a los 15 s.
Las mismas rutas corregidas pasan. La comprobación posterior al cambio global
a 25 también pasó en los seis paquetes afectados.

## Cambio en Engram y configuración

Único archivo original modificado: `internal/cloud/cloudstore/cloudstore.go:523`,
marca literal `// NX-07 (OP-13)`, listado en `NEXO_CHANGES.md` §4.
La implementación vive en `internal/cloud/cloudstore/nexo_op13_pool.go`.
El resto del código nuevo/modificado pertenece a Nexo (`internal/nexo/...` o `nexo_*`).

`NEXO_DB_MAX_OPEN_CONNS` pasa a 25 por defecto; `0` explícito sigue sin límite
y el mínimo configurable sigue en 4. Reposo 10, vida máxima 30m y reposo máximo 5m.
Actualizados `dbpool.go`, sus pruebas, `nexo_db_status_test.go`,
`nexo_dbconn_test.go`, manual 4.1.1, `NEXO_CHANGES.md` §3/§4/§5 y OP-11/OP-13.

```text
max_connections >= réplicas × 25 + margen
```

El margen cubre migraciones, CLI, administración, respaldos y otros consumidores.

## Validación de entrega

Se ejecutaron las comprobaciones sobre el candidato:

| Comprobación | Resultado |
|---|---|
| `go build ./...` | Pasa |
| `go vet ./internal/nexo/... ./internal/cloud/... ./cmd/engram/` | Pasa |
| `bash scripts/deadcode-ratchet.sh` | Pasa: `no newly unreachable functions`; sin modificar el baseline |
| `tr -d '\r' < f \| gofmt -l` | Sin salida en los 15 archivos Go tocados |
| `git diff --check` | Pasa |
| Regresiones OP-13 y configuración/estado del pool después de fijar 25 | Pasan en los seis paquetes afectados |
| Suite completa con Postgres y `-p 1` | Ejecutada hasta terminar: 34 paquetes, 27 pasan inicialmente, 5 sin pruebas y 2 fallan; autosync pasa en su repetición completa. CLI conserva siete fallos previos de permisos, además del caso conocido permitido en la primera ejecución |

Comando de la suite completa (captura JSON para conservar el avance):

```text
go test -json -p 1 -count=1 -timeout=15m ./internal/cloud/... ./internal/nexo/... ./cmd/engram/
```

Pasaron todas las pruebas de `cloudserver`, `cloudstore` y todos los paquetes Nexo,
incluidas publicación/aprobación, vectores, roles, tokens, auditoría y las cinco regresiones
con una conexión. Las pruebas Postgres dentro de CLI también pasaron.

Se observaron y atendieron condiciones del entorno:

- La terminal nativa no iniciaba procesos y la caché global de Go denegaba escritura.
  Se usó la herramienta Node disponible con caché y temporales dentro del repositorio.
- Una llamada larga agotó el límite de cinco minutos de la herramienta. La ejecución válida
  se repitió con seguimiento persistente, llegó a terminar y permitió registrar todos los paquetes.
- `TestManagerPushQuarantinesOnlyConfiguredTarget` falló por `TempDir RemoveAll cleanup:
  El directorio no está vacío`. La repetición completa de `./internal/cloud/autosync` pasó (3,661 s).
- El `jq` global no se podía ejecutar. Se descargó temporalmente el ejecutable de la
  [publicación oficial de jq 1.8.1](https://github.com/jqlang/jq/releases/tag/jq-1.8.1),
  verificando su SHA-256 contra `sha256sum.txt`; las pruebas de hooks pasaron al repetir CLI.
- Se normalizaron `TMP`, `TEMP` y `GOTMPDIR` a rutas Windows. Se repitió todo
  `./cmd/engram/`, excluyendo únicamente el caso autorizado
  `TestCompatibilityPublicBinary/database_alias`, que falla por falta del privilegio de symlink:

```text
go test -json -p 1 -count=1 -timeout=15m -skip '^TestCompatibilityPublicBinary$/^database_alias

## Riesgos y límites

- 25 es un presupuesto inicial por réplica. Observar esperas y dimensionar `max_connections`
  según réplicas y consumidores reales.
- Corregido en la revisión adversarial del 2026-10-14: el relleno pagina solo claves `(project_name, chunk_id)` y lee un payload a la vez (antes retenía una página de 128 payloads, cerca de 1 GiB con subidas de 8 MiB); al terminar una pasada completa deja la marca `nexo_backfill_marks` y los arranques siguientes no releen los payloads. Medido: 66 MiB de pico con 24 chunks de 7 MiB (345 MiB antes).
- El modo de texto queda cacheado: cambios del catálogo requieren la política existente de
  reindexado/reinicio. Consultas frías concurrentes pueden repetir una lectura, sin mutex durante SQL.
- El fallback vectorial devuelve el error si no logra completar rollback.
- La validación amplia de CLI conserva siete fallos previos por permisos de resolución de rutas;
  deben contrastarse en un entorno sin esa restricción antes de afirmar una suite completa en verde.
- Nuevas operaciones deberán conservar el ejecutor transaccional y cerrar cursores antes de
  adquirir otra conexión; la revisión corresponde a los caminos presentes en esta versión.

Trabajo sin commits, merges, push ni cambios de rama.
 ./cmd/engram/
```

**La suite completa no está en verde en este entorno.** Tras esa repetición quedan exactamente
siete fallos de CLI; todos se reprodujeron también en una copia temporal de `HEAD` (`5f7b565`),
con el código original y las mismas condiciones:

| Fallo previo | Archivo:línea | Causa observada |
|---|---|---|
| `TestCompatibilityPublicBinary/fresh` | `cmd/engram/compatibility_test.go:255` | Rechazo de metadata; `filepath.EvalSymlinks` devuelve `Acceso denegado` |
| `TestCompatibilityPublicBinary/legacy_schema` | `cmd/engram/compatibility_test.go:255` | Mismo rechazo |
| `TestCompatibilityPublicBinary/current_header_unknown_shape` | `cmd/engram/compatibility_test.go:255` | Mismo rechazo |
| `TestCompatibilityPublicBinary/future_schema` | `cmd/engram/compatibility_test.go:255` | Mismo rechazo |
| `TestCmdSaveCreatesManualSessionWithCWDDirectory` | `cmd/engram/main_extra_test.go:1068` | `resolve cwd symlinks: Acceso denegado` |
| `TestDoctorDiscardEmptyPromptApply` | `cmd/engram/prompt_discard_test.go:221` | `resolve backup parent: Acceso denegado` |
| `TestDoctorDiscardEmptyPromptBackupErrors` | `cmd/engram/prompt_discard_test.go:401` | Mismo error al resolver el padre del respaldo |

Un diagnóstico independiente sobre un directorio temporal vacío confirmó:
ruta absoluta y limpia, `Lstat` correcto, pero `EvalSymlinks` denegado.
El guard de compatibilidad está en `internal/store/compatibility.go:38`, sin cambios.
Docker también denegó acceso a su API, por lo que no se pudo contrastar estos casos en Linux.
Los siete fallos siguen pendientes de un entorno con permisos suficientes; no se cambiaron
sus pruebas ni el código de SQLite para ocultarlos.

Comando de contraste ejecutado sobre la copia temporal del original:

```text
go test -json -p 1 -count=1 -timeout=10m -run '^TestCompatibilityPublicBinary$|^TestCmdSaveCreatesManualSessionWithCWDDirectory$|^TestDoctorDiscardEmptyPrompt(Apply|BackupErrors)

## Riesgos y límites

- 25 es un presupuesto inicial por réplica. Observar esperas y dimensionar `max_connections`
  según réplicas y consumidores reales.
- Corregido en la revisión adversarial del 2026-10-14: el relleno pagina solo claves `(project_name, chunk_id)` y lee un payload a la vez (antes retenía una página de 128 payloads, cerca de 1 GiB con subidas de 8 MiB); al terminar una pasada completa deja la marca `nexo_backfill_marks` y los arranques siguientes no releen los payloads. Medido: 66 MiB de pico con 24 chunks de 7 MiB (345 MiB antes).
- El modo de texto queda cacheado: cambios del catálogo requieren la política existente de
  reindexado/reinicio. Consultas frías concurrentes pueden repetir una lectura, sin mutex durante SQL.
- El fallback vectorial devuelve el error si no logra completar rollback.
- Nuevas operaciones deberán conservar el ejecutor transaccional y cerrar cursores antes de
  adquirir otra conexión; la revisión corresponde a los caminos presentes en esta versión.

Trabajo sin commits, merges, push ni cambios de rama.
 -skip '^TestCompatibilityPublicBinary$/^database_alias

## Riesgos y límites

- 25 es un presupuesto inicial por réplica. Observar esperas y dimensionar `max_connections`
  según réplicas y consumidores reales.
- Corregido en la revisión adversarial del 2026-10-14: el relleno pagina solo claves `(project_name, chunk_id)` y lee un payload a la vez (antes retenía una página de 128 payloads, cerca de 1 GiB con subidas de 8 MiB); al terminar una pasada completa deja la marca `nexo_backfill_marks` y los arranques siguientes no releen los payloads. Medido: 66 MiB de pico con 24 chunks de 7 MiB (345 MiB antes).
- El modo de texto queda cacheado: cambios del catálogo requieren la política existente de
  reindexado/reinicio. Consultas frías concurrentes pueden repetir una lectura, sin mutex durante SQL.
- El fallback vectorial devuelve el error si no logra completar rollback.
- Nuevas operaciones deberán conservar el ejecutor transaccional y cerrar cursores antes de
  adquirir otra conexión; la revisión corresponde a los caminos presentes en esta versión.

Trabajo sin commits, merges, push ni cambios de rama.
 ./cmd/engram/
```

La base fue exclusivamente `nexoop13` en 55437, con la variable exacta `CLOUDSTORE_TEST_DSN`.
Los resultados persistidos temporales se revisaron para confirmar que no contenían esa DSN
ni su contraseña. Caché, binario jq, diagnóstico y copia de HEAD se retiraron al entregar.


## Riesgos y límites

- 25 es un presupuesto inicial por réplica. Observar esperas y dimensionar `max_connections`
  según réplicas y consumidores reales.
- Corregido en la revisión adversarial del 2026-10-14: el relleno pagina solo claves `(project_name, chunk_id)` y lee un payload a la vez (antes retenía una página de 128 payloads, cerca de 1 GiB con subidas de 8 MiB); al terminar una pasada completa deja la marca `nexo_backfill_marks` y los arranques siguientes no releen los payloads. Medido: 66 MiB de pico con 24 chunks de 7 MiB (345 MiB antes).
- El modo de texto queda cacheado: cambios del catálogo requieren la política existente de
  reindexado/reinicio. Consultas frías concurrentes pueden repetir una lectura, sin mutex durante SQL.
- El fallback vectorial devuelve el error si no logra completar rollback.
- Nuevas operaciones deberán conservar el ejecutor transaccional y cerrar cursores antes de
  adquirir otra conexión; la revisión corresponde a los caminos presentes en esta versión.

Trabajo sin commits, merges, push ni cambios de rama.
