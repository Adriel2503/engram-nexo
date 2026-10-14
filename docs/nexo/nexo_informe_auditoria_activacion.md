# Informe de auditoría y activación — Nexo IA

Rama `feat/auditoria-activacion`. Trabajo limitado a este repositorio. Sin commits, cambios de rama, merges ni push. Sin cambios a SQLite, al bootstrap ni a emergencia.

## Archivos

### Engram: ediciones mínimas marcadas

- `internal/cloud/cloudserver/admin_handlers.go:231`: enganche NX-07 de activación para otra persona.
- `internal/cloud/cloudserver/dashboard_admin_users.go:312`: mismo enganche NX-07 en el panel.
- `internal/cloud/dashboard/login.templ:64`: enlace público NX-07 a Activar mi cuenta.
- `internal/cloud/dashboard/login_templ.go`: generado con el comando autorizado.

Las marcas están inventariadas en NEXO_CHANGES §4.

### Nexo: nuevos y modificados

- `NEXO_CHANGES.md`
- `deploy/nexo/compose.example.yaml`
- `docs/nexo/manuales/administracion.md`
- `docs/nexo/manuales/instalacion-dev.md`
- `docs/nexo/manuales/servidor-local.md`
- `docs/nexo/manuales/servidor-produccion.md`
- `docs/nexo/nexo_informe_auditoria_activacion.md`
- `internal/cloud/cloudserver/nexo_activation.go`
- `internal/cloud/cloudserver/nexo_activation_test.go`
- `internal/cloud/cloudserver/nexo_admin_routes.go`
- `internal/cloud/cloudserver/nexo_admin_routes_test.go`
- `internal/cloud/cloudserver/nexo_audit_dashboard.go`
- `internal/cloud/cloudserver/nexo_audit_dashboard_evidence.go`
- `internal/cloud/cloudserver/nexo_audit_dashboard_test.go`
- `internal/cloud/cloudserver/nexo_audit_visibility.go`
- `internal/cloud/cloudserver/nexo_audit_visibility_test.go`
- `internal/cloud/cloudserver/nexo_authz_routes.go`
- `internal/cloud/cloudserver/nexo_continuity_test.go`
- `internal/cloud/cloudserver/nexo_evidence.go`
- `internal/cloud/cloudserver/nexo_flow_test.go`
- `internal/cloud/cloudserver/nexo_leakscan_test.go`
- `internal/cloud/cloudserver/nexo_panel_es_test.go`
- `internal/cloud/cloudserver/nexo_response_fields_test.go`
- `internal/cloud/cloudserver/nexo_responses.go`
- `internal/cloud/cloudserver/nexo_routes_test.go`
- `internal/cloud/cloudserver/nexo_security.go`
- `internal/cloud/cloudserver/nexo_security_test.go`
- `internal/cloud/cloudserver/nexo_tokens.go`
- `internal/cloud/cloudserver/nexo_users_dashboard.go`
- `internal/cloud/cloudstore/nexo_activation.go`
- `internal/cloud/cloudstore/nexo_audit.go`
- `internal/cloud/cloudstore/nexo_migrations.go`
- `internal/cloud/cloudstore/nexo_queries.go`
- `internal/cloud/dashboard/nexo_activation.go`
- `internal/cloud/dashboard/nexo_activation.templ`
- `internal/cloud/dashboard/nexo_activation_templ.go`
- `internal/cloud/dashboard/nexo_brand.go`
- `internal/cloud/dashboard/nexo_users.go`
- `internal/cloud/dashboard/nexo_users.templ`
- `internal/cloud/dashboard/nexo_users_templ.go`
- `internal/nexo/kb/client/nexo_activation.go`
- `internal/nexo/kb/service/evalrun.go`
- `internal/nexo/kb/service/nexo_audit_visibility.go`
- `internal/nexo/kb/service/r2_status.go`
- `internal/nexo/kb/service/spaces.go`
- `internal/nexo/kb/service/upload.go`
- `internal/nexo/kb/store/status.go`
- `internal/nexo/nexocli/connection.go`
- `internal/nexo/nexocli/nexo_activation_test.go`
- `internal/nexo/nexocli/nexocli.go`
- `internal/nexo/security/evidence/alerts.go`
- `internal/nexo/security/evidence/export.go`
- `internal/nexo/security/evidence/nexo_export_projection.go`
- `internal/nexo/security/evidence/nexo_export_projection_test.go`
- `internal/nexo/security/evidence/webhook.go`
- `internal/nexo/security/nexo_activation.go`
- `internal/nexo/security/nexo_activation_test.go`
- `internal/nexo/security/nexo_audit_visibility.go`
- `internal/nexo/security/nexo_audit_visibility_test.go`
- `internal/nexo/security/ratelimit.go`

### Archivos de la pasada de correcciones (2026-10-14)

Nuevos: `internal/nexo/redact/redact.go` y `redact_test.go`; `internal/cloud/cloudserver/nexo_correcciones_test.go`; `internal/cloud/cloudserver/nexo_activation_concurrency_test.go`; `internal/nexo/kb/ingest/nexo_activationcode_test.go`.

Engram original, con marca `// NX-07` y listado en `NEXO_CHANGES.md` §4: `internal/cloud/cloudstore/identity.go` (importación, validación común del nombre, `project` y `reason_code` redactados al escribir, metadata y nombres redactados al leer, nombre validado en la recuperación) y las pruebas `admin_handlers_test.go` y `dashboard_admin_users_test.go` (dobles sin Nexo).

Nexo modificados: `cloudstore/nexo_activation.go`, `nexo_actor.go`, `nexo_activity.go`, `nexo_dbconn.go`, `nexo_dbconn_test.go`, `nexo_emergency.go`, `nexo_queries.go`; `cloudserver/nexo_activation.go`, `nexo_audit_visibility.go`, `nexo_audit_dashboard_evidence.go`, `nexo_clientctx.go`, `nexo_evidence.go`, `nexo_service_accounts.go`, `nexo_tokens.go` y las pruebas `nexo_audit_dashboard_test.go`, `nexo_panel_es_test.go`, `nexo_search_status_test.go`, `nexo_secrets_test.go`; `internal/nexo/authz/pgstore.go`; `kb/ingest/secrets.go`; `kb/service/documents.go`, `nexo_audit_visibility.go`, `proposals.go`, `publish.go`; `kb/store/proposals.go`, `runs.go`, `store.go`; `security/nexo_activation.go`, `nexo_activation_test.go`, `nexo_audit_visibility.go`, `nexo_audit_visibility_test.go`; `security/evidence/alerts.go`, `nexo_export_projection.go`, `nexo_export_projection_test.go`, `review.go`, `review_test.go`, `webhook.go`; `deploy/nexo/compose.example.yaml`; manuales `administracion.md`, `instalacion-dev.md`, `servidor-local.md`, `servidor-produccion.md`; `docs/nexo/mejoras-y-referencias.md` (SG-17); `NEXO_CHANGES.md`; `.deadcode-baseline.txt` (se quitó `authz/roles.go ProjectScope`, que ya es alcanzable).

## Decisiones

- R-6: `project.read` por fila de historial; `space.read` para evidencia de conocimiento. Auditor, admin y superadmin sin rol no ven contenido. Proyectos abiertos y permisos de Engram solo cuentan cuando el motor los resuelve como rol de proyecto.
- Sin permiso, título y payload se omiten y el historial marca `payload_withheld`. El panel no muestra payload, aunque permita títulos de proyectos autorizados. Texto libre de metadatos sin lectura se retiene mediante proyección explícita.
- Otras personas humanas reciben código, no token: cambio deliberado de contrato de las dos API y el panel. La emisión no crea un token. Los clientes antiguos deben usar `credential_type`, `activation_code` y el canje.
- Código aleatorio de 256 bits, HMAC con el pepper existente y separación de dominio. Solo hash y metadatos en una tabla Nexo. TTL 24 h por defecto (decisión del coordinador/usuario), configurable de 1 h a 7 días; un valor inválido impide arrancar el servidor, con un error que nombra `NEXO_ACTIVATION_TTL` y no muestra el valor.
- El canje fija alcance y vida solicitados, consume y crea el token en una transacción. Orden de bloqueos: persona, guarda de emisión, código. Reclamo condicional verifica vencimiento con reloj actual; los fallos se confirman con evidencia. Revocar toma persona y código en el mismo orden relativo.
- Emisión propia, cuentas de servicio, bootstrap del primer admin, recuperación y emergencia siguen con entrega directa. El canje público no establece sesión automáticamente.
- El canje comparte límite login y bloqueo por IP; cuenta cada fallo. Reutilización y fallos repetidos de códigos conocidos insertan la alerta `activation_repeated` con deduplicación.
- Exportación v2 sin contenido: fila proyectada, digest y enlaces originales, firma del archivo proyectado. Verificación compatible con v1 y v2; v1 ya emitidos se conservan. Webhook sin resumen libre ni detalles.

## Inventario de caminos revisados

| Camino | Hallazgo y resultado |
|---|---|
| `GET /nexo/admin/history/{entityKey}` | Título y payload accesibles por rol de plataforma. Corregido: decisión por cada proyecto de fila, incluso mezclados; `payload_withheld` |
| `GET /nexo/admin/audit` | Metadatos libres podían incluir títulos/contenido. Proyección por proyecto o espacio de fila y retirada recursiva de credenciales |
| `GET /nexo/admin/authz-events`, `GET /nexo/authz/v1/events` | Detalles libres. Proyección con ámbito del evento |
| `GET /nexo/admin/activity`, `/presence` | Actor, proyecto, conteos, conexión y referencia de token; sin memoria/documento ni títulos de contenido |
| `/dashboard/admin/audit` (seguridad y roles) | Usa las mismas proyecciones; detalle expandible no recibe contenido retenido |
| `/dashboard/admin/audit/history` | Pedía payload falso pero mostraba título. Corregido por proyecto; payload siempre ausente |
| `/dashboard/admin/audit/activity` | Metadatos de actividad/presencia; sin contenido |
| `/dashboard/admin/audit/integrity`, `/nexo/admin/audit/verify` | Hashes, identificadores, conteos y diagnósticos de integridad; sin filas de contenido |
| `/nexo/admin/alerts`, `/dashboard/admin/audit/alerts` y detalle | Alertas de conocimiento guardaban título en details. Proyección por espacio/proyecto; sin permiso se omite todo resumen libre |
| Reconocimiento de alertas API/panel | Devuelve/guarda datos administrativos del reconocimiento, no documentos ni memorias |
| `/nexo/admin/access-review`, CSV/JSON; panel y `/export`; registro de revisión | Personas, roles, proyectos, metadatos de tokens, notas administrativas y huellas. No genera memoria/documento ni valor de credencial |
| `/nexo/admin/database`, salud y readiness | Estado técnico y conteos; sin contenido ni DSN |
| `/dashboard/admin/audit-log` y `/list` de Engram | Consulta cloud_sync_audit_log. La plantilla no renderiza metadata: solo operación, motivo, proyecto y conteos |
| `/nexo/kb/v1/spaces/{slug}/events` | platform.audit.read permitía detalles del espacio. Corregido: lectura de contenido exige space.read |
| `/nexo/kb/v1/spaces/{slug}/publish-runs` | Rechazos/origen/subida podían transportar texto de documentos. Retenidos sin space.read; se conservan contadores |
| `/nexo/kb/v1/eval-runs` | Tener algún espacio visible entregaba resultados de corridas de otros espacios. Corregido: resultados/notas/configuración libre requieren lectura de todos los espacios de esa corrida |
| `/dashboard/kb/search-status` | Evidencia agregada de evaluaciones y errores de indexación. Nombre/resultados de evaluación requieren lectura de todos sus espacios; cada error libre requiere lectura de su espacio |
| Exportación automática diaria y `engram nexo audit export` | Serializaba row completo. Corregido con proyección v2 siempre sin contenido; se conserva cadena/firma |
| `engram nexo audit verify-export`, `verify`, `verify-review` | Verifican evidencia, hashes y metadatos; no entregan contenido |
| Webhook de alertas | Resumen libre potencialmente con títulos. Ahora entrega regla, severidad, capa y sujeto |
| Punto común de cloud_auth_audit_log y logs de error Nexo | Se retiran formatos de código/token/hash de textos libres, filtros y agente; sin registrar credenciales |
| Navegación, páginas y APIs de lectura de conocimiento | Las rutas de contenido pasan por autorización del espacio; el rol de plataforma no reemplaza space.read |

## Inventario de emisión de credenciales

| Camino | Resultado |
|---|---|
| `POST /nexo/authz/v1/tokens` | Otra persona: código. Propio o cuenta de servicio: token una vez |
| `POST /admin/users/{id}/tokens` | Otra persona: código; propio: token una vez |
| `POST /dashboard/admin/users/{id}/tokens` | Otra persona: código con instrucciones; propio: token una vez |
| Emisión y rotación de cuentas de servicio, API/panel/CLI | Directa, como antes, con auditoría; un bot no activa una persona |
| `POST /nexo/authz/v1/activate`, `/dashboard/activate` | Token después de un canje confirmado; nunca en URL, redirect o logs; respuesta no-store |
| `engram nexo connect --activate`, `--activate-stdin` | Canje sin redirección, guarda token en cloud.json y marcador sin secreto; sin imprimir código/token |
| Bootstrap CLI/panel, recover-token y emergency-token | Entrega directa al operador para sí mismo; no modificados |

## Pruebas escritas y adaptadas

- Postgres: roles auditor/admin/superadmin sin lectura, lector, dos proyectos mezclados y panel sin payload ni títulos ajenos; eventos y alertas de documentos sin lectura.
- Postgres: emisión/canje, token no creado antes del canje, code inválido como Bearer, alcance y vida, doble canje, vencido, revocado, persona deshabilitada, pendientes, revocación API/panel, IP/agente en auditoría y alerta de reutilización.
- Postgres: dos canjes simultáneos, bloqueo por repeticiones del mismo código y límite de peticiones.
- Postgres: panel no entrega token ajeno, pantalla pública, autoemisión y cuentas de servicio; regresiones previas pasan a canjear antes de usar el token.
- Sin Postgres: TTL/generación, proyección recursiva, secretos en escritura/log, exportación, JSON inválido sin eco de código, CLI con argumento/entrada oculta y guardado sin secretos en salida/marcador.
- Contratos: rutas Nexo registradas, tipos JSON nuevos revisados con excepción de entrega de una sola vez limitada a tipos concretos; buscador de fugas ampliado a códigos.

## Correcciones de la revisión adversarial (2026-10-14)

Diez puntos de la revisión de Codex y del coordinador. Para cada uno: casos encontrados (todos los del mismo patrón), corrección, archivos y pruebas. Las pruebas de Postgres se corrieron contra Postgres 18 (`CLOUDSTORE_TEST_DSN`, base `nexoact`).

### 1. Canje por el emisor (decisión: riesgo aceptado, documentado)

- **Casos.** (1) El emisor canjea por la API con su propio token; (2) con su sesión del panel (`/dashboard/activate`); (3) el mismo principal con otro token o sesión; (4) el emisor que canje sin autenticarse, desde otra IP o máquina: **no se puede detectar**; (5) el emisor canjea desde su misma IP en el minuto siguiente: indicio.
- **Corrección.** No se implementó una solución (SSO o canal independiente): se documentó como PROBLEMA CONOCIDO con los cinco apartados pedidos (qué pasa, qué piden OWASP/ASVS/NIST/ISO, qué se cumple, mitigaciones, solución antes de producción) en `administracion.md` §3.3, `NEXO_CHANGES.md` §3, este informe y el backlog (SG-17). Mitigaciones nuevas: el canje autenticado como quien emitió el código (persona o token) responde `403 activation_self_redeem`, se audita con la identidad del emisor y no consume el código ni suma al bloqueo; alerta alta `activation_same_ip` si el canje llega desde la IP del emisor en menos de 1 minuto (heurística; documentado que solo es un indicio). Columnas nuevas `issued_by_token_id` e `issued_ip`. La API pública detecta al llamador con token en `Authorization` o con la cookie de sesión del panel (`nexoRedeemCaller`); una credencial inválida se ignora.
- **Archivos.** `cloudstore/nexo_activation.go`, `cloudserver/nexo_activation.go`, `evidence/alerts.go`, `cloudserver/nexo_audit_dashboard_evidence.go`.
- **Pruebas.** `TestNexoActivationIssuerCannotRedeemPG` (API, panel, auditoría, sin consumir, la destinataria canjea) y `TestNexoActivationSameIPAlertPG` (misma IP alerta; IP distinta y pasado el minuto no).

### 2. Credenciales en campos externos

- **Campos externos y dónde se guardaban sin redactar.** User-Agent: `nexoTrimUserAgent` (origen, contexto de la petición), `cloud_auth_audit_log.metadata.user_agent`, `nexo_authz_events.user_agent`, `nexo_kb_events.user_agent`, `nexo_activity_log.user_agent`, `nexo_actor_presence.last_user_agent`, `nexo_alert_acks.user_agent`, `nexo_access_reviews.user_agent`, `nexo_kb_publish_runs.user_agent`. Declaración del cliente (`client_claim`): `NexoTruncateClaim` (actor de mutaciones: `cloud_mutations.actor_client_claim`), `nexo_activity_log`, `nexo_actor_presence`, `nexo_authz_events`, `nexo_kb_events`, propuestas. Nombres y etiquetas: etiqueta del actor en eventos de roles, eventos de conocimiento, ejecuciones de publicación y propuestas, `MutationActor.Name`, etiquetas de reconocimiento y revisión. Otros: proyecto (actividad y auditoría), `reason_code`, `target_id` de eventos de conocimiento, `subject`/`dedup_key`/`summary`/`details` de alertas, origen/subida/rechazos/fuente de ejecuciones de publicación, notas de reconocimiento y de revisión.
- **Corrección.** Paquete hoja `internal/nexo/redact` (sin dependencias; lo usan el motor de roles, el almacén de conocimiento y la seguridad sin ciclos) con detección de formato (`nxa_`, `egc_`, `hmac-sha256:v1:`, `Bearer x`, `Authorization: x`, cadenas `postgres://`). Se redacta **antes de recortar** y **antes de guardar** en todos los puntos anteriores, y **otra vez al leer** (lista completa: `ListAuthAudit`, `ListAuthAuditEvents`, `ListActivity`, `ListPresence`, `MutationHistory`, eventos de roles, eventos de conocimiento, alertas y su reconocimiento, revisiones y reporte de accesos con CSV/JSON, ejecuciones de publicación, exportación diaria y por CLI v2 sobre toda la fila, webhook, panel). `nexoFilterHistory` (`ClientClaim`) y `nexoFilterSecurityAudit` (`UserAgent`) redactan de forma explícita. La palabra `authorization` suelta dejó de contar como credencial (hacía falta `Authorization: valor`) para no alterar títulos legítimos.
- **Archivos.** `internal/nexo/redact/*`, `security/nexo_audit_visibility.go`, `authz/pgstore.go`, `kb/store/store.go`, `runs.go`, `proposals.go`, `cloudstore/nexo_actor.go`, `nexo_activity.go`, `nexo_queries.go`, `identity.go`, `evidence/alerts.go`, `review.go`, `webhook.go`, `nexo_export_projection.go`, `cloudserver/nexo_clientctx.go`, `nexo_audit_visibility.go`.
- **Pruebas.** `TestNexoExternalTextRedactedOnWritePG` (token y código `nxa_` en User-Agent, notas y declaración por las siete tablas y rutas; el texto legítimo se conserva), `TestNexoExternalTextRedactedOnReadPG` (filas históricas en todas las tablas leídas por API y panel, exportación diaria de cada tabla y webhook), `redact_test.go`, `TestNexoExportProjectionRedactsCredentialsInRow`.

### 3. Nombres de token

- **Casos.** `POST /nexo/authz/v1/tokens` (otra persona, autoemisión), `POST /admin/users/{id}/tokens` (otra persona, autoemisión), `POST /dashboard/admin/users/{id}/tokens`, emisión y rotación de cuenta de servicio (API y panel), `RecoverStrandedAdminTokenWithAudit`, `emergency-token`, canje (nombre heredado de la activación, rotación con el nombre del token anterior).
- **Corrección.** `security.NexoCheckTokenName` (hasta 256 caracteres, sin control, sin credenciales ni cadenas de conexión) en el punto común `normalizeCreatePrincipalTokenParams`, en recuperación y emergencia, y antes de las excepciones en `nexoMaybeActivation` y `nexoCreateManagedToken` (respuesta 400 que no repite el valor). Los nombres históricos se redactan al devolverlos: `scanPrincipalToken` (listados, panel, respuestas), actividad, presencia, historial, reporte de accesos y su CSV/JSON.
- **Pruebas.** `TestNexoTokenNameValidatedOnEveryRoutePG` (6 rutas API, 2 del panel, 5 nombres inválidos, sin tokens ni códigos creados, guardián del almacén), `TestNexoHistoricTokenNamesRedactedPG`, `TestNexoCheckTokenName`.

### 4. Notas libres

- **Casos.** `Ack.Note` de alertas (lista API, panel y respuesta del reconocimiento); notas de revisiones de accesos (`CompleteReview`, `last_review`, `recent_reviews`, panel de revisión, CSV/JSON).
- **Corrección.** `AlertAck.NoteWithheld`/`WithholdNote`: la nota se retiene si falta lectura del ámbito de la alerta (como `Summary` y `Details`) y siempre se redacta; la respuesta del reconocimiento no repite la nota. `ReviewRecord.NoteWithheld`: sin ámbito verificable, la nota de una revisión nunca sale en `last_review`, `recent_reviews`, la respuesta de `access-review:complete`, el panel ni las exportaciones. La base conserva la nota (redactada).
- **Pruebas.** `TestNexoFreeNotesWithheldWithoutScopePG`; ajustadas por cambio deliberado `TestNexoAuditPanelAlerts`, `TestNexoAuditPanelAccessReview` y `TestAccessReviewReportAndCompletion`.

### 5. El canje revalida la política vigente

- **Casos.** Días pedidos por encima del `NEXO_TOKEN_MAX_DAYS` actual; alcance ya no permitido (`ResolveIssue` valida alcance y días).
- **Corrección.** `ResolveIssue` dentro de la transacción de canje, antes de consumir: `409 activation_policy_changed`, auditado, sin consumir ni sumar intentos ni alerta de repetición (no cuenta para el bloqueo por IP).
- **Prueba.** `TestNexoActivationPolicyRevalidatedAtRedeemPG` (se baja el máximo entre emisión y canje; vuelve a subir y el mismo código se canjea).

### 6. Detector de secretos sin `nxa_`

- **Casos.** `ingest.DetectSecrets` no conocía el código; `doc_key`, ruta, origen (repo, rama, commit, ruta, autor, aprobador, CI, archivo, fuente, URL, títulos de ruta), nota de cambio, responsable, etiquetas, categoría; propuestas (clave, categoría, etiquetas); retiro y reemplazo (clave, reemplazo, nota); asunto, clave de deduplicación, `target_id`, resumen y detalles de alertas de conocimiento; webhook.
- **Corrección.** Patrón `activation_code` en `DetectSecrets` (sin `\b` inicial, también pegado a claves) y `nxa_` ampliado en `NexoAuditHasCredential`/redacción. `nexoCredentialIn` valida los campos externos al publicar, proponer y retirar; el rechazo (`secret`) nombra el campo y no repite el valor (la clave y la ruta salen redactadas en el resultado y en la ejecución). `InsertAlert` redacta asunto, clave, resumen y detalles; `ListAlerts`, `ListEvents` y `Payload` (webhook) redactan lo histórico.
- **Pruebas.** `TestNexoKBMetadataRejectsCredentialsPG` (código en contenido, título, `doc_key`, ruta, origen y nota de cambio; token en `doc_key`; propuesta y retiro), `TestNexoKBAlertSubjectsRedactedPG` (el evaluador con un documento histórico de clave peligrosa), `TestDetectSecretsActivationCode`.

### 7. `NEXO_ACTIVATION_TTL` inválido

- **Corrección.** `security.ParseActivationTTL`/`ActivationTTLFromEnv`; se valida en `cloudstore.nexoOpenDB` (arranque del servidor, junto a las variables del pool): el error nombra la variable y no muestra el valor. Defecto 24 h (decisión del usuario). Se retiró la función antigua que usaba 48 h en silencio.
- **Pruebas.** `TestNexoActivationTTL`, `TestNexoActivationTTLInvalidFailsToStart` (cinco valores inválidos; el error no repite el valor).

### 8. `nexoMaybeActivation` falla cerrado

- **Casos.** `POST /nexo/authz/v1/tokens`, `POST /admin/users/{id}/tokens` y `POST /dashboard/admin/users/{id}/tokens` sin motor de Nexo.
- **Corrección.** Sin motor, otra persona recibe `503` (API) o página 503 (panel) y no se entrega ni crea ningún token; se mantiene la autoemisión (el actor es el destino), que no depende de Nexo. Dobles de prueba de Engram ajustados: `admin_handlers_test.go` (cinco pruebas) y `dashboard_admin_users_test.go` (cuatro) ahora emiten para el propio actor y comprueban el 503 para otra persona.

### 9. Fallos de Postgres 18 en `cloudserver`

| Prueba | Causa raíz | Corrección |
|---|---|---|
| `TestNexoAuditPanelLogFilters` | Expectativa obsoleta por el cambio deliberado: la proyección explícita retiene el texto libre de claves desconocidas (`nota`, `nota2`, `ok`) sin lectura del ámbito | La prueba usa campos conocidos (`reason`, `status`, `outcome`) y comprueba que una clave desconocida se retiene |
| `TestNexoPanelPagesAreInSpanish` | El formulario de token de otra persona ahora crea un código ("Crear código de activación", "Para otra persona se entrega un código de un solo uso") | Expectativa actualizada; se agrega la página propia ("Crear mi token") |
| `TestNexoSearchStatusWithProvider` | Los textos libres del diagnóstico (errores de indexación, nombre de evaluación) exigen lectura del espacio y la prueba sembraba una carta muerta con un espacio inexistente y a carla solo con rol de plataforma | La prueba crea el espacio `arq`, hace lectora a carla y enlaza la carta muerta al espacio |
| `TestNexoNoSecretsInLogsAndEvents` | `nexo_activation_codes.code_hash` guarda a propósito el HMAC del código (como `token_hash`) y el escáner de tablas lo marcaba | La columna `code_hash` se excluye igual que `token_hash`; el valor del código sigue prohibido en cualquier tabla |

Además, por el punto 4: `TestNexoAuditPanelAlerts`, `TestNexoAuditPanelAccessReview` (cloudserver) y `TestAccessReviewReportAndCompletion` (evidence) esperaban ver notas libres.

### 10. Concurrencia

- **Pruebas** (`nexo_activation_concurrency_test.go`): `TestNexoActivationRedeem32ConcurrentSingleWinnerPG` (32 canjes simultáneos del mismo código: uno gana, 31 reciben `activation_used`, un token, un canje auditado), `TestNexoActivationRevokeAndRedeemRacePG` (6 rondas, 16 canjes y 16 revocaciones a la vez: el código termina canjeado o revocado, nunca ambos, y el token existe solo si se canjeó), `TestNexoActivationDisableAndRedeemRacePG` (5 rondas: nunca un código consumido sin token ni lo contrario; si ganó la deshabilitación, `activation_disabled`), `TestNexoActivationPool4Concurrent32PG` (pool de 4 conexiones: 16 canjes y 16 emisiones a la vez por HTTP, con plazo de 45 s; `DeadlineExceeded` es falla) y `TestNexoActivationStorePool4Concurrent32PG` (lo mismo directo contra el almacén).
- **Revisión de OP-13.** Ninguna transacción de emisión, canje o revocación pide otra conexión del pool mientras está abierta: todo usa `tx` (`lockPrincipalTokenTargetTx`, `insertAuthAuditEvent`, `InsertAlert`, `nexoApplyTokenIssue`); la lectura de `RulesFromEnv` es de entorno. La detección del llamador (`nexoRedeemCaller`) y el hash se hacen antes de abrir la transacción. Las pruebas con pool 4 lo confirman.

## Validación

| Comando | Resultado |
|---|---|
| `go tool templ generate -path ./internal/cloud/dashboard` | Correcto; se restauraron archivos con cambios solo de finales de línea |
| `go build ./...` | Correcto sobre el código final |
| `go vet ./internal/nexo/... ./internal/cloud/... ./cmd/engram/` | Correcto sobre el código final |
| `go test ./internal/nexo/... ./internal/cloud/...` | Correcto sobre el código final; Postgres omitido automáticamente |
| `go test ./internal/nexo/... ./internal/cloud/... ./cmd/engram/` | Nexo y cloud correctos; cmd/engram falla en pruebas de archivos/symlinks y compatibilidad de Windows, ajenas a los archivos modificados |
| `go test ./...` | No pasó: fallos en cmd/engram, internal/llm, internal/mcp, internal/server, internal/setup, internal/store, internal/tui, plugin, scripts y tools |
| `git diff --check` | Correcto |

Los comandos se ejecutaron con CLOUDSTORE_TEST_DSN vacío y con cachés, HOME/USERPROFILE y temporales dentro de `.validation` en este repositorio. La caché aislada se retira antes de entregar. No se ejecutaron pruebas Postgres ni se accedió al demo.

Las fallas de suite completa incluyen permisos denegados en EvalSymlinks y respaldos, privilegios de symlink ausentes, errores de sockets de Windows, limpieza de temporales, procesos que mantienen archivos abiertos y herramientas faltantes (`jq`, `pwsh`). Se repitieron aisladamente `TestProjectResolvingReadHandlersExceptStatsPreserveAmbiguityRecoveryMetadata`, `TestStartAcceptsOnlySameInstanceBindLoser` y `TestExposeHookToolPreservesLookupName`: vuelven a fallar. No se atribuyen a esta implementación sin ejecutar una base comparable en un entorno habilitado; sus archivos no tienen cambios en este diff.

Postgres de la entrega anterior: las pruebas se escribieron sin ejecutarse; ver la validación de las correcciones más abajo.

### Validación de la pasada de correcciones (2026-10-14)

| Comando | Resultado |
|---|---|
| `go build ./...` | Correcto |
| `go vet ./internal/nexo/... ./internal/cloud/... ./cmd/engram/` | Correcto |
| `bash scripts/deadcode-ratchet.sh` | Correcto: sin funciones nuevas inalcanzables. La deuda bajó en una entrada (`authz/roles.go ProjectScope`, ahora alcanzable) y se quitó solo esa de `.deadcode-baseline.txt` |
| gofmt de los archivos tocados (`tr -d '\r' < f \| gofmt -l`) | Sin diferencias |
| `go test ./internal/nexo/... ./internal/cloud/...` sin Postgres | Correcto |
| `CLOUDSTORE_TEST_DSN=... go test -p 1 ./internal/cloud/... ./internal/nexo/... ./cmd/engram/` (Postgres 18, base `nexoact`) | Todo correcto salvo el fallo conocido de Windows `TestCompatibilityPublicBinary/database_alias` (un `symlink` exige un privilegio que este equipo no tiene). `internal/cloud/cloudserver` 184 s, `internal/cloud/cloudstore` 76 s |
| Paquete `plugin` | No se ejecutó: falta `jq`/`pwsh` en este equipo (ignorado según el encargo) |

Las pruebas largas se corrieron en segundo plano y se revisó su salida completa. No quedó ningún token ni código `nxa_` real, ni cadena de conexión con contraseña, en archivos nuevos o modificados: las credenciales de las pruebas se generan al vuelo y la salida de las pruebas nunca las imprime (solo "contiene la credencial #n").


## Riesgos y decisiones abiertas

- **PROBLEMA CONOCIDO (riesgo aceptado, no resuelto): el administrador que emite el código lo conoce.** (a) Qué pasa: el administrador ve el código y, mientras esté vigente, podría canjearlo él mismo antes que la persona y obtener un token a nombre de ella. (b) Qué piden los estándares: OWASP Forgot Password Cheat Sheet y ASVS 4.0.3 V2.5 (V6 en ASVS 5.0), NIST SP 800-63B (el secreto de activación va a la dirección registrada de la persona y quien emite no lo conoce) e ISO/IEC 27002:2022 5.17 (entrega segura de la autenticación temporal y confirmación de recepción). (c) Qué se cumple y qué no: aleatorio de 256 bits, un solo uso, vencimiento, solo hash, intentos limitados y auditoría, sí; emisor sin conocimiento del secreto, no. (d) Mitigaciones actuales: rechazo del canje si llega autenticado como quien lo emitió (mismo token o sesión del panel), alerta alta `activation_same_ip` por canje desde la IP del emisor en menos de 1 minuto (solo un indicio), vencimiento de 24 h, revocación y auditoría con IP y agente. No detectan a un emisor que canje sin autenticarse desde otra máquina o IP. (e) Solución requerida antes de producción: SSO corporativo (OIDC con el proveedor de identidad de la empresa) o, sin SSO, envío del código por el servidor al correo corporativo de la persona sin que el administrador lo vea; backlog SG-17 (`docs/nexo/mejoras-y-referencias.md`, prioridad A). Detalle en `administracion.md` 3.3 y `NEXO_CHANGES.md` §3.
- Límites y bloqueo son por réplica, igual que el mecanismo existente. Despliegues con varias réplicas requieren protección compartida en el proxy.
- Un canje confirmado puede perder su respuesta o el guardado local. No se puede volver a usar: emita código nuevo y revoque el token huérfano.
- Rehabilitar una persona permite sus códigos pendientes aún vigentes. Para baja definitiva se deben revocar también.
- Archivos v1 históricos pueden contener títulos/texto. No se reescribe evidencia firmada; revisar acceso y retención de esos archivos. V2 demuestra integridad de la proyección y conserva el digest original, no publica la fila original.
- La alerta de activación se inserta directamente en Postgres para verla en API/panel; no se promete envío por el webhook del evaluador periódico.
- Pendiente del coordinador: probar la migración idempotente (`ALTER TABLE ... ADD COLUMN IF NOT EXISTS` en `nexo_activation_codes`) y los privilegios de la cuenta de aplicación con dos cuentas de base de datos; las pruebas de esta pasada corrieron con una sola cuenta (modo degradado) sobre Postgres 18. No se modificaron demos ni bases externas.
- La redacción es por formato (`nxa_`, `egc_`, huellas, cabeceras, cadenas postgres://), no una detección general de secretos: una contraseña o clave de otro sistema pegada en un User-Agent o una nota no se reconoce. La nota de una revisión de accesos y los textos retenidos siguen en la base para quien tenga acceso a ella.
- La alerta `activation_same_ip` es una heurística: NAT, proxies o un equipo compartido la disparan, y un emisor que cambie de IP o canje sin autenticarse no la dispara.
- Una revisión de accesos histórica cuyo nombre de token o de persona traía una credencial ahora se redacta al construir el reporte: su huella (`report_sha256`) ya guardada puede no coincidir con la recalculada. Solo afecta a filas con credenciales en esos nombres.
- Las rutas de Engram sin Nexo (dobles de prueba o un despliegue sin el motor de roles) ya no emiten tokens para otra persona (503); una instalación así solo puede usar la autoemisión.

Referencias de diseño: [OWASP Logging Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html) y [OWASP Forgot Password Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Forgot_Password_Cheat_Sheet.html), usadas para minimizar datos de registro y emplear credenciales aleatorias, temporales y de un solo uso. Las referencias ISO/NIST de la tarea orientan los controles; no se declara certificación.

## Fallos principales de la suite completa

- `TestCompatibilityPublicBinary`
- `TestClaudeAdapterPersistsWritesForDistinctSameWorktreeHosts`
- `TestCmdSaveCreatesManualSessionWithCWDDirectory`
- `TestDoctorDiscardEmptyPromptApply`
- `TestDoctorDiscardEmptyPromptBackupErrors`
- `TestSessionStartWithoutDirectoryUsesCurrentWorkingDirectory`
- `TestSessionStartWithWhitespaceDirectoryUsesCurrentWorkingDirectory`
- `TestMemSave_AmbiguousEnvelope`
- `TestAmbiguousRecoveryTokenBindings`
- `TestAmbiguousRecoveryConcurrentSameChoiceBootstrap`
- `TestAmbiguousUnknownSessionRecoveryWriteTools`
- `TestMemSave_AmbiguousUnknownSessionRecovery`
- `TestMemSave_AmbiguousWithValidUserChoiceSucceeds`
- `TestMemSave_AmbiguousRecoveryRejectsStaleToken`
- `TestMemSave_AmbiguousRecoveryRejectsTokenForDifferentProject`
- `TestMemSave_AmbiguousChoiceRequiresExactAvailableProject`
- `TestMemSave_AmbiguousRecoveryRequiresExactAvailableProjectRegression`
- `TestMemSavePrompt_AmbiguousWithValidUserChoiceSucceeds`
- `TestMemCurrentProject_ExplicitWorkspace`
- `TestHandleGetObservation_AmbiguousCwdNilProjectRecoveryEnvelope`
- `TestProjectResolvingReadHandlersExceptStatsPreserveAmbiguityRecoveryMetadata`
- `TestGetObservationAndReviewPreserveAmbiguityRecoveryMetadata`
- `TestStartAcceptsOnlySameInstanceBindLoser`
- `TestSyncStatus_IncludesDeferredAndDeadCounts`
- `TestExposeHookToolPreservesLookupName`
- `TestCompatibilityOfflineNeverAuthorizes`
- `TestCompatibilityProbeAcceptsRealWALStore`
- `TestLegacyEmptyPromptDiscardInboxIdentity`
- `TestLegacyEmptyPromptDiscardBackupRedirect`
- `TestLegacyEmptyPromptDiscardRollback`
- `TestLegacyEmptyPromptDiscardRevalidationErrors`
- `TestLegacyEmptyPromptDiscardPostBackupStale`
- `TestLegacyEmptyPromptDiscardSuccess`
- `TestSessionIdentityRepairCountsOnlyCommittedRetry`
- `TestSetupAllowlistPromptFlow`
- `TestLifecycleScriptsUseCanonicalProjectResolution`
- `TestCodexRegisteredSessionHandoff`
- `TestCodexHandoffTransportBoundary`
- `TestCodexSubagentStopAlwaysEmitsHookEnvelope`
- `TestCodexSubagentStopExplicitEmptyMessageSelection`
- `TestCodexUnixSessionEndAdapter`
- `TestCodexUnixUserPromptSubmitValidatesObservationsAndFirstSaveThreshold`
- `TestCodexUnixUserPromptSubmitDetachesPromptPersistencePipes`
- `TestCodexUnixUserPromptSubmitDetachesPromptPersistenceStdin`
- `TestCodexWindowsSessionEndAdapter`
- `TestCodexWindowsNativeUserPromptAcceptsSlashFormHomes`
- `TestCodexWindowsNativeUserPromptRejectsPartialProfiles`
- `TestDeadcodeRatchetToolchain`
- `TestCloudSyncWrappers`
- `TestCloudSyncPowerShellMissingEngram`

HEAD permanece en `5f7b565`, rama `feat/auditoria-activacion`. Sin commits.

## Adenda: segunda revisión adversarial (2026-10-14, rama fix/revision-nexo3)

Dos revisiones adversariales de Codex sobre `develop` (seguridad y concurrencia) dejaron once puntos (diez del
encargo más el interruptor de cuentas de servicio). Detalle, diseño de la proyección por esquema, de la
barrera de lectura (`redact.Struct`) y de la prueba canario, y los comandos de verificación: `NEXO_CHANGES.md`
(entrada "Correcciones de las dos revisiones adversariales sobre develop"). Resumen de lo que cambia respecto de
este informe:

- La proyección de metadatos ya no usa una lista global de nombres: cada flujo declara los campos que salen
  (`security.NexoProject`) y lo desconocido se omite sin lectura del ámbito. Las exportaciones (diaria y CLI v2)
  conservan solo las columnas declaradas por tabla.
- Los textos externos (usuario, correo, nombres, descripciones, notas, motivos, ajustes de espacio, corridas de
  evaluación) se validan al escribir y se limpian al leer, incluidas las filas históricas.
- Emisión, revocación y canje de activaciones bloquean a la persona con `FOR NO KEY UPDATE`: dos
  administradores que se emiten códigos recíprocos ya no se interbloquean (antes `40P01`).
- La CLI guarda el token canjeado antes de cualquier otra operación y `engram nexo connect --resume` continúa.
- Los errores transaccionales de la activación se registran con su SQLSTATE; la respuesta pública no cambia.
- `NEXO_SERVICE_ACCOUNTS_ENABLED` (defecto `false`) apaga las cuentas de servicio durante la beta.

El problema conocido (el administrador que emite el código puede canjearlo; SG-17) no cambia.
