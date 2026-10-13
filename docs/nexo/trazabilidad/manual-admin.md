# Manual de administración — Trazabilidad en Engram Cloud

> **Documento reemplazado (2026-10-13).** Este manual describe la base Engram 3.3.2 y solo la
> trazabilidad (NX-01 a NX-04). Para la versión `3.4.0-nexo.1` o posterior use
> [`docs/nexo/manuales/administracion.md`](../manuales/administracion.md) (roles, tokens con
> caducidad y alcance, espacios de conocimiento, auditoría y revisión de accesos) y
> [`docs/nexo/manuales/servidor-produccion.md`](../manuales/servidor-produccion.md) (variables del
> servidor). Se conserva como referencia histórica de la API NX-03.

**Proyecto:** Nexo IA (fork `engram-nexo`, base Engram v3.3.2) · **Cambios:** NX-01 a NX-04
**Referencias:** `requirements.md`, `design.md`, `tasks.md` · **Fecha:** 2026-10-12

Este manual explica al administrador cómo nombrar los tokens, qué configurar en el servidor y cómo consultar la trazabilidad por la API JSON.

---

## 1. Tokens: uno por persona y por máquina o uso (RF-23, RN-A2, RN-A5)

La única identidad es el **token asignado** por el administrador (RN-A1). Lo que declara la PC (usuario de Windows o nombre del equipo) se guarda solo como referencia (`client_claim`) y nunca como autor.

### Convención de nombres

Al emitir un token (en el panel, desde el detalle del usuario administrado, o con `POST /admin/users/{principalID}/tokens` y el cuerpo `{"name": "..."}`), el campo **name** identifica la **máquina o el uso**, no a la persona (la persona ya la da el usuario dueño del token):

| Uso | Formato del nombre | Ejemplos |
|---|---|---|
| Equipo personal | `<tipo>-<usuario>` | `laptop-ana`, `pc-luis`, `mac-ana` |
| Segundo equipo de la misma persona | `<tipo>-<usuario>-<n>` | `laptop-ana-2` |
| Integración continua o automatizaciones | `ci-<sistema>` | `ci-pipeline`, `ci-github` |
| Agente o servicio | `agente-<nombre>` | `agente-revisor` |

Reglas:

- Solo minúsculas, números y guiones; sin espacios ni datos sensibles (nunca el valor del token, contraseñas ni correos).
- Un token por máquina o uso: así la actividad y la presencia muestran desde qué equipo se conectó cada persona (CA-06).
- Si se pierde o se reasigna un equipo, se **revoca** su token y se emite uno nuevo con el mismo nombre; el historial conserva el token anterior.
- El token compartido de Engram (`ENGRAM_CLOUD_TOKEN`) lleva un valor aleatorio largo que **no se entrega a nadie** (RN-A3). Todo lo que llegue con él figura como `no-identificado`.

## 2. Configuración del servidor

| Variable | Valor | Para qué |
|---|---|---|
| `NEXO_TRUSTED_PROXIES` | IP o CIDR del proxy HTTPS, separados por comas (por ejemplo, la red Docker de Caddy `172.18.0.0/16`) | Tomar la IP real del cliente de `X-Forwarded-For` **solo** si la conexión viene de ese proxy (RF-21). Sin valor, se registra la IP de la conexión y la cabecera se ignora (CA-22) |
| `NEXO_ACTIVITY_RETENTION_DAYS` | Días (por defecto `180`; `0` = no depurar) | Conservación del registro detallado de conexiones (RF-15). La presencia se conserva siempre |

La IP y el cliente (User-Agent) quedan en:

- `nexo_activity_log` y `nexo_actor_presence` (NX-02);
- la metadata de cada evento de `cloud_auth_audit_log` (`client_ip`, `user_agent`): logins y rechazos del panel, rechazos de sincronización, alta y baja de usuarios, tokens y permisos, y las propias consultas de trazabilidad (NX-04).

## 3. API de trazabilidad (NX-03)

Rutas de **solo lectura** para administradores. Requieren `Authorization: Bearer <token>` de un usuario con rol `admin` **y** token asignado:

| Situación | Respuesta |
|---|---|
| Sin token o token inválido | `401` |
| Usuario `member`, token compartido, admin heredado del entorno o servidor sin autenticación | `403` |
| Parámetro inválido (fecha, límite, operación, cursor) | `400` |
| La lectura no se pudo registrar en la auditoría | `500` y no se entregan datos |

Cada consulta exitosa queda en `cloud_auth_audit_log` con la acción `nexo.audit.read`, `nexo.activity.read`, `nexo.presence.read` o `nexo.history.read`, el admin como actor, los filtros usados y la cantidad de filas devueltas (RF-36).

### 3.1 Forma común de la respuesta

```json
{ "items": [ ... ], "next_cursor": "123456", "limit": 100 }
```

- Orden: de lo más reciente a lo más antiguo.
- `limit`: por defecto 100, máximo 200 (si se pide más, se aplica 200 y la respuesta lo indica).
- `next_cursor`: `null` si no hay más páginas. Para la siguiente página se repite la consulta con los mismos filtros y `cursor=<next_cursor>`. La paginación es por llave: los eventos que llegan entre páginas no duplican ni saltan filas.
- Fechas (`from`, `to`, `since`): RFC 3339 (`2026-10-12T08:00:00Z`) o `AAAA-MM-DD` (UTC). `from` y `since` son inclusivos; `to` es exclusivo, y con una fecha sola cubre el día completo.

### 3.2 `GET /nexo/admin/audit` — auditoría de seguridad

Filtros: `from`, `to`, `action` (exacta, o prefijo terminado en `*`, por ejemplo `nexo.*` o `token.*`), `actor` (id de principal, usuario, email, nombre mostrado o `actor_source` como `request`), `outcome` (`success`, `denied`), `cursor`, `limit`.

Campos: `id`, `occurred_at`, `actor_principal_id`, `actor_username`, `actor_display_name`, `actor_email`, `actor_source`, `target_principal_id`, `target_username`, `project`, `action`, `outcome`, `reason_code`, `client_ip`, `user_agent`, `metadata`.

```text
GET /nexo/admin/audit?outcome=denied&from=2026-10-05
→ {"items":[{"id":"812","occurred_at":"2026-10-12T14:03:11Z","actor_source":"request","action":"sync.auth",
   "outcome":"denied","reason_code":"unknown_token","client_ip":"10.0.0.15","user_agent":"Go-http-client/1.1",...}],
   "next_cursor":null,"limit":100}
```

### 3.3 `GET /nexo/admin/activity` — conexiones

Filtros: `from`, `to`, `actor` (id, usuario, email, nombre mostrado o `no-identificado`), `token` (id, nombre o prefijo), `project`, `operation` (`push_chunk`, `push_mutations`, `pull_chunk`, `pull_mutations`), `cursor`, `limit`.

Campos: `id`, `occurred_at`, `actor`, `actor_email`, `actor_principal_id`, `actor_username`, `actor_verified`, `token_id`, `token_prefix`, `token_name`, `operation`, `project`, `item_count`, `client_ip`, `user_agent`, `client_claim`.

```text
GET /nexo/admin/activity?actor=luis&project=nexo-transversal&limit=50
→ {"items":[{"id":"5120","actor":"luis","token_name":"laptop-luis","operation":"pull_mutations",
   "project":"nexo-transversal","item_count":3,"client_ip":"10.0.0.20","client_claim":"",...}],
   "next_cursor":"5071","limit":50}
```

### 3.4 `GET /nexo/admin/presence` — quién se conecta

Filtros: `actor`, `since` (última conexión desde), `cursor`, `limit`. El cursor de presencia es opaco: se usa tal cual se recibe.

Campos: `presence_key`, `actor`, `actor_email`, `actor_principal_id`, `actor_username`, `actor_verified`, `token_id`, `token_prefix`, `token_name`, `first_seen_at`, `last_seen_at`, `last_ip`, `last_user_agent`, `last_client_claim` (lo declarado por la PC), `last_project`, `push_count`, `pull_count`.

### 3.5 `GET /nexo/admin/history/{entityKey}` — historial de un recuerdo

`entityKey` es el `sync_id` del recuerdo (o el id de la sesión o relación). Filtros: `project`, `entity`, `include_payload=true` (incluye el contenido completo de cada versión; por defecto solo el título y los metadatos, RF-34), `cursor`, `limit`.

Campos: `seq`, `version` (1 = la primera, por proyecto y tipo de entidad), `project`, `entity`, `entity_key`, `op`, `occurred_at`, `actor`, `actor_email` (copia al momento del cambio), `actor_verified`, `actor_principal_id`, `actor_username` y `current_email` (datos actuales), `token_id`, `token_prefix`, `token_name`, `client_claim`, `title`, `payload` (solo con `include_payload=true`).

```text
GET /nexo/admin/history/obs-1?project=nexo-transversal
→ {"items":[{"seq":"42","version":2,"op":"upsert","actor":"luis","actor_email":"luis@rp.pe","token_name":"laptop-luis",...},
            {"seq":"40","version":1,"op":"upsert","actor":"ana","actor_email":"ana@rp.pe","token_name":"laptop-ana",...}],
   "next_cursor":null,"limit":100}
```

### 3.6 Ejemplo con PowerShell

```powershell
$h = @{ Authorization = "Bearer $env:NEXO_ADMIN_TOKEN" }
Invoke-RestMethod -Headers $h "https://<servidor>/nexo/admin/presence"
Invoke-RestMethod -Headers $h "https://<servidor>/nexo/admin/audit?outcome=denied&from=2026-10-05"
```

El token de administrador se toma de una variable de entorno; no se escribe en scripts ni en archivos versionados.
