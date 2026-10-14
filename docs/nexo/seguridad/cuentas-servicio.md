# Cuentas de servicio (SG-10): nota de diseño

| | |
|---|---|
| Backlog | `docs/nexo/mejoras-y-referencias.md`, SG-10 |
| Requisitos | RS-20..RS-24 (`requirements.md`), DS-2, D-S3-5, D-S3-6 (`design.md`); R-10 (`docs/nexo/roles/requirements.md`) |
| Fecha | 2026-10-13 |
| Manual | `docs/nexo/manuales/administracion.md` §3.8 |


## 0. Interruptor para la beta (2026-10-14)

Las cuentas de servicio están **apagadas por defecto** (`NEXO_SERVICE_ACCOUNTS_ENABLED=false`): no hay
automatizaciones previstas y apagarlas elimina el riesgo R2 (un administrador sin rol en un espacio emite el
token de una cuenta que sí tiene rol publicador y lo usa para leer o modificar sus documentos). Apagadas:
crear la cuenta, habilitarla de nuevo, emitir su token (API, `POST /nexo/authz/v1/tokens`, rutas de Engram,
panel, CLI) y rotarlo responden `403` `service_accounts_disabled`, quedan auditados
(`nexo.service_accounts.denied`) y no crean nada; los tokens existentes no autentican (falla cerrado, con aviso al
arrancar con el número de cuentas habilitadas); consultar, revocar y deshabilitar siguen permitidos. Un valor
inválido impide arrancar. Con `true` rige todo lo que sigue, con el riesgo R2 documentado: el administrador puede
emitir tokens de cuentas con roles de contenido. Solución futura: credenciales de cliente OAuth o federación de
identidad de carga (propuesta de autenticación, sección 4.3). Backlog: SG-17.
## 1. Objetivo

Que un pipeline de CI o un bot publique en los espacios de conocimiento con su propia identidad, sin
el token de una persona, con responsable humano, caducidad, rotación sin corte y vista de su último
uso.

## 2. Datos

Sin tablas nuevas de identidad: una cuenta de servicio es una fila de `cloud_principals` de Engram
con `kind = 'service_account'` (valor que Engram ya admitía), rol de Engram `member`. Lo que Engram
no guarda va en una tabla lateral de Nexo, creada por `authz.Migrate` (aditiva, idempotente, dentro
de la transacción de migraciones con el advisory lock de siempre):

| `nexo_service_accounts` | Tipo | Nota |
|---|---|---|
| `principal_id` | BIGINT PK, FK `cloud_principals` ON DELETE CASCADE | La cuenta |
| `slug` | TEXT UNIQUE, `^[a-z0-9][a-z0-9-]{1,62}$` | Nombre corto; la API lo acepta donde acepta un usuario |
| `description` | TEXT, hasta 500 | Para qué se usa |
| `owner_principal_id` | BIGINT NOT NULL, FK `cloud_principals` | Persona responsable (obligatoria) |
| `created_by_principal_id` | BIGINT, FK ON DELETE SET NULL | Quién la creó |
| `created_at`, `updated_at` | TIMESTAMPTZ | |

El trigger `nexo_service_accounts_validate` exige que la fila sea de una cuenta de servicio y que el
responsable sea una persona (`kind = 'human'`), también por SQL directo. Es capa de identidad: no hay
FK hacia las tablas de conocimiento ni de proyectos.

Tokens: los de siempre (`cloud_principal_tokens`, con `nexo_expires_at` y `nexo_scope` de S3). No se
agregan columnas.

## 3. Reglas

| Regla | Dónde se cumple |
|---|---|
| R-10: solo `lector`, `proponente` o `publicador` en ámbitos; nunca `responsable` ni rol de plataforma | Servicio (`SetScopeMember`, `SetPlatformRole`, `checkOwnerCandidate`) y trigger `nexo_role_bindings_validate` (sin cambios) |
| RS-20 / DS-2: caducidad por defecto; sin caducidad solo cuenta de servicio y solo superadmin | `security.ResolveIssue` y trigger `nexo_token_expiry_guard` (sin cambios); la ruta de la cuenta reutiliza `nexoTokenIssueContext` |
| Alta, cambio, deshabilitar, habilitar | `platform.users.manage` (admin o superadmin) |
| Emitir, rotar, revocar | `platform.tokens.manage` (admin o superadmin; R-7 no aplica: la cuenta es `miembro` de plataforma) |
| Listado y detalle | `platform.directory.read` (también el auditor) |
| Negar por defecto | Toda ruta pasa por `nexoAuthzRoute` (identidad administrada) y `Engine.Require`; una denegación deja `authz.denied` |
| Token una sola vez | Solo en la respuesta de emisión o rotación (`raw_token`); la auditoría guarda id y prefijo |

Deshabilitar pone `cloud_principals.enabled = false`: la resolución de tokens de Engram
(`MarkPrincipalTokenUsed`) rechaza todos sus tokens en la siguiente petición. Habilitar los recupera
si siguen vigentes.

## 4. Rotación

1. Se emite el token nuevo (mismo nombre y alcance salvo que se pidan otros; caducidad por las reglas
   de siempre).
2. El anterior: con `grace_minutes = 0` se revoca (`revocation_reason = 'rotated'`); con gracia
   (1..10080) su `nexo_expires_at` pasa a `LEAST(caducidad actual, now() + gracia)`, y vence solo.
3. Evento `service_account.token_rotated` con el id nuevo, el anterior y la gracia, en la misma
   transacción que el cambio del anterior.

El token nuevo se crea primero: si el paso 2 falla, la respuesta igual entrega el token nuevo (se
muestra una sola vez) con `old_token.error`, y el anterior queda como estaba para revocarlo a mano.

## 5. Último uso

Sin escrituras nuevas por petición (no suma costo al camino caliente). `security.LoadTokensLastSeen`
reúne, por token:

- `last_used_at` de `cloud_principal_tokens` (Engram lo actualiza en cada petición autenticada);
- la última acción registrada con ese token, con IP y cliente: `nexo_actor_presence`
  (`presence_key = principal|token`, sincronización), `nexo_kb_events` (subidas, publicaciones,
  denegaciones) y `nexo_authz_events` (administración). Una consulta con `LATERAL` y `LIMIT 1` por
  fuente, por los índices de actor existentes.

Límite: una petición de solo lectura que no deja evento (búsqueda) mueve `last_used_at` pero no la IP.

### Inactividad: aviso, nunca deshabilitar (decisión)

- `NEXO_DISABLE_INACTIVE_DAYS` (S4-04) deshabilita **solo personas** (`kind = 'human'`). Una cuenta
  de servicio nunca se deshabilita por inactividad.
- `NEXO_SA_STALE_DAYS` (por defecto 180; `0` = desactivado): una cuenta de servicio habilitada sin
  uso en ese plazo deja la alerta `service_account_stale` en `nexo_alerts` (capa plataforma,
  severidad `warning`; se apaga o cambia con `NEXO_ALERT_RULES`), con su nombre corto y su
  responsable, y la marca "sin uso" en la API (`stale`, `idle_days`, `last_activity_at`), en
  `engram nexo sa list|show`, en el panel y en la revisión de accesos (marca `stale`).
- **Último uso**: función SQL `nexo_principal_last_use(bigint)`, el último `last_used_at` de sus
  tokens o la última acción registrada con su identidad (las mismas fuentes que arriba). Si nunca se
  usó, el plazo cuenta desde el alta. Emitir o rotar un token no cuenta como uso.
- **Sin repetición**: una alerta por cuenta y período sin uso (la clave de deduplicación lleva la
  fecha del último uso o del alta). La pasada diaria siguiente no la repite; reconocerla la cierra.
  Si la cuenta vuelve a usarse y otra vez queda sin uso, avisa de nuevo.

**Por qué.** Un pipeline de CI que publica cada pocos meses es un uso normal: deshabilitarlo en
silencio rompe el CI sin que nadie lo note. Las cuentas de servicio ya tienen sus propias defensas:
tokens con caducidad (90 días por defecto; sin caducidad solo un superadmin), rotación con gracia y
revocación con evento, y una persona responsable obligatoria. El aviso le pide a esa persona que
confirme si la cuenta sigue en uso; si no, se revocan sus tokens o se deshabilita a mano.

## 6. Eventos

En `nexo_authz_events` (encadenada por S4), ámbito plataforma, objetivo la cuenta:
`service_account.created`, `.updated`, `.disabled`, `.enabled`, `.token_issued`, `.token_rotated`,
`.token_revoked`. La emisión además deja `token.create` en `cloud_auth_audit_log` (como cualquier
token). Las asignaciones de rol dejan `binding.*` como siempre.

## 7. Superficies

| Superficie | Archivo |
|---|---|
| Casos de uso y migración | `internal/nexo/authz/serviceaccounts.go` |
| Último uso | `internal/nexo/security/tokenusage.go` |
| Cuentas sin uso (alerta `service_account_stale`, `NEXO_SA_STALE_DAYS`) | `internal/nexo/security/evidence/serviceaccounts.go` |
| API `/nexo/authz/v1/service-accounts` | `internal/cloud/cloudserver/nexo_service_accounts.go` |
| Panel `/dashboard/admin/service-accounts` | `cloudserver/nexo_service_accounts_dashboard.go`, `dashboard/nexo_service_accounts.templ` |
| CLI `engram nexo sa` | `internal/nexo/nexocli/serviceaccounts.go` |

Rutas nuevas (para el registro de rutas de S5):

| Método | Ruta | Capacidad |
|---|---|---|
| GET | `/nexo/authz/v1/service-accounts` | `platform.directory.read` |
| POST | `/nexo/authz/v1/service-accounts` | `platform.users.manage` |
| GET | `/nexo/authz/v1/service-accounts/{sa}` | `platform.directory.read` |
| PATCH | `/nexo/authz/v1/service-accounts/{sa}` | `platform.users.manage` |
| POST | `/nexo/authz/v1/service-accounts/{saAction}` (`<sa>:disable`, `<sa>:enable`) | `platform.users.manage` |
| POST | `/nexo/authz/v1/service-accounts/{sa}/tokens` | `platform.tokens.manage` |
| POST | `/nexo/authz/v1/service-accounts/{sa}/tokens/{tokenAction}` (`<id>:rotate`, `<id>:revoke`) | `platform.tokens.manage` |
| GET | `/dashboard/admin/service-accounts` | `platform.directory.read` (sesión del panel) |
| POST | `/dashboard/admin/service-accounts` | `platform.users.manage` (sesión + CSRF) |
| POST | `/dashboard/admin/service-accounts/{sa}/{op}` (`disable`, `enable`, `token`, `rotate`, `revoke`) | `platform.users.manage` o `platform.tokens.manage` (sesión + CSRF) |

## 8. Decisiones abiertas

- **Desactivación por inactividad (S4-04).** Resuelta: `NEXO_DISABLE_INACTIVE_DAYS` solo alcanza a
  personas y las cuentas de servicio sin uso solo avisan con `NEXO_SA_STALE_DAYS` (ver §5,
  "Inactividad").
- **IP de peticiones de solo lectura.** Si se quiere la IP de cada uso (no solo de las acciones
  registradas), haría falta una escritura por petición para las cuentas de servicio.
