# Manual de administración de Nexo IA

| | |
|---|---|
| Versión de Nexo | `3.4.0-nexo.1` (base Engram v3.4.0) |
| Para quién | Administradores de la plataforma, auditores y responsables de espacios de conocimiento |
| Reemplaza a | `docs/nexo/trazabilidad/manual-admin.md` (base 3.3.2, solo trazabilidad) |
| Fecha | 2026-10-13 |

Este manual cubre el trabajo diario: personas, tokens, espacios de conocimiento, documentos,
propuestas, proyectos, auditoría y revisión de accesos. La instalación del servidor está en
[`servidor-produccion.md`](servidor-produccion.md); la conexión de la PC de un desarrollador, en
`instalacion-dev.md` (en esta carpeta).

---

## 1. Antes de empezar

### 1.1 Dos formas de trabajar

| Herramienta | Para qué | Dónde |
|---|---|---|
| Panel web | Usuarios y tokens, cuentas de servicio, permisos de proyecto, crear espacios, subir y retirar documentos, buscar | `https://<dominio>/dashboard/` (entrar con su token). Conocimiento en `https://<dominio>/dashboard/kb`; cuentas de servicio en `https://<dominio>/dashboard/admin/service-accounts` |
| API con `curl` | Todo lo anterior y además: roles de plataforma, miembros de espacios, propuestas, avisos, auditoría y revisión de accesos | `https://<dominio>/...` con `Authorization: Bearer <token>` |
| CLI `engram nexo` | Subir carpetas o la wiki, ver cambios, reindexar, verificar la auditoría, cuentas de servicio (`engram nexo sa`) | En su PC conectada (`engram nexo connect`) o dentro del contenedor del servidor |

No hay pantalla para gestionar miembros de espacios ni roles de plataforma: se hace con la API
`/nexo/authz/v1` (secciones 2.4 y 4.3).

### 1.2 Preparar la terminal

Los ejemplos usan `bash` (Linux, macOS o Git Bash en Windows). El token se lee sin mostrarlo y sin
dejarlo en el historial:

```sh
export NEXO_URL="https://<dominio>"
read -rs NEXO_TOKEN        # pegue su token y presione Enter
export NEXO_TOKEN
H="Authorization: Bearer $NEXO_TOKEN"
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/me"
```

En PowerShell use `curl.exe` (no el alias `curl`) y comillas dobles escapadas en el JSON, o
`Invoke-RestMethod`:

```powershell
$h = @{ Authorization = "Bearer $env:NEXO_TOKEN" }
Invoke-RestMethod -Headers $h "https://<dominio>/nexo/authz/v1/me"
Invoke-RestMethod -Method Put -Headers $h -ContentType "application/json" `
  -Body '{"role":"publicador"}' "https://<dominio>/nexo/authz/v1/scopes/space/<espacio>/members/<usuario>"
```

Nunca escriba un token en un script, un archivo versionado, un ticket o un chat.

### 1.3 Cómo responde la API

Los errores llegan como `{"error_class": "...", "error_code": "...", "error": "..."}`:

| Código HTTP | Significa |
|---|---|
| `400` | Cuerpo o parámetros mal formados. La API de administración rechaza campos JSON que no conoce |
| `401` | Sin token, token desconocido, revocado o vencido (`token_expired`) |
| `403` | Sin la capacidad necesaria, token de solo lectura, red no permitida o regla de no auto-asignación. Queda un evento `authz.denied` |
| `404` | El espacio no existe o usted no lo puede ver (un espacio restringido no se revela) |
| `409` | Regla de negocio: último superadmin, último responsable, el espacio ya existe, propuesta ya cerrada, versión cambiada |
| `413` | Petición o archivo demasiado grande |
| `422` | Validación del contenido (categoría, secretos, formato) |
| `429` | Límite de peticiones (`rate_limited`, con `Retry-After`) o IP bloqueada por fallos (`auth_locked`) |

Los listados vienen como `{"items": [...], "next_cursor": "...", "limit": N}`. Para la siguiente
página repita la consulta con `cursor=<next_cursor>`. Máximo 200 por página.

En las rutas, `<usuario>` puede ser el nombre de usuario o el id numérico de la persona en
`/nexo/authz/v1/*`; en `/admin/users/<id>/*` es siempre el id numérico (`principal_id`).

## 2. Roles y capacidades

Un rol es un conjunto de capacidades. Cada rol incluye lo del anterior. Lo que puede hacer una
persona en una petición es su rol **y** el alcance de su token (un token `read` solo permite leer).

### 2.1 Roles de plataforma

| Rol | Para quién | Capacidades que agrega |
|---|---|---|
| miembro | Todos (es el rol por defecto, no se guarda) | `self.read`: ver sus propios roles y tokens |
| auditor | Seguridad o cumplimiento | `platform.audit.read` (auditoría, alertas, revisión de accesos) y `platform.directory.read` (usuarios y asignaciones) |
| admin | Equipo de plataforma | `platform.users.manage` (crear, habilitar y deshabilitar miembros; permisos de proyecto), `platform.tokens.manage` (tokens de otros), `platform.scope.create`, `platform.scope.archive`, `platform.scope.assign_owner` (crear y archivar espacios y proyectos, nombrar responsable al crear) |
| superadmin | Dueños técnicos (se recomiendan dos) | `platform.admins.manage` (crear, cambiar y deshabilitar admins, superadmins y auditores, y gestionar sus tokens), `platform.settings` (configuración, reindexado), `platform.scope.recover` (nombrar responsable en un ámbito que se quedó sin ninguno) |

Reglas que conviene recordar:

- Los roles de plataforma **no dan acceso al contenido**. Un admin que necesita publicar en un
  espacio recibe un rol en ese espacio, como cualquiera.
- Solo un superadmin gestiona admins, superadmins y auditores, y sus tokens.
- Si `NEXO_SUPERADMIN_ALLOWED_CIDRS` está configurada, `platform.admins.manage` y
  `platform.settings` solo funcionan desde esas redes (fuera de ellas: 403 con motivo `network`).
- Siempre queda al menos un superadmin habilitado: el último no se puede degradar ni deshabilitar
  (`409 last_superadmin`).
- Nadie se asigna a sí mismo un rol mayor al que tiene (`403 self_promotion`).

### 2.2 Roles en proyectos y espacios

| Rol | En un espacio de conocimiento | En un proyecto de memoria |
|---|---|---|
| lector | `space.read`: buscar y leer documentos vigentes, historial y copia local | `project.read` |
| proponente | Además `space.propose`: enviar propuestas (no son oficiales hasta revisarse) | Además `project.write.own` |
| publicador | Además `space.publish` (subir, actualizar, retirar, reemplazar), `space.review` (aprobar o descartar propuestas) y `space.audit.read` (eventos y últimas subidas) | Además `project.write.any` y `project.audit.read` |
| responsable | Además `space.members.manage` (asignar roles) y `space.configure` (nombre, descripción, categorías, visibilidad, rol implícito, ajustes) | Además `project.members.manage` y `project.configure` |

Importante: en esta versión los roles de **proyecto** de Nexo todavía no controlan la
sincronización de memorias. El acceso a un proyecto se sigue dando con el permiso de proyecto de
Engram (sección 8). Los roles de **espacio** sí están activos.

### 2.3 Visibilidad de los espacios

| Visibilidad | Quién lo ve |
|---|---|
| `open` (abierto, por defecto) | Toda persona habilitada, con el rol implícito del espacio (`default_role`: `lector` por defecto, o `proponente`). Quien tenga un rol asignado usa ese rol |
| `restricted` (restringido) | Solo sus miembros. Para los demás no aparece en listados ni búsquedas y pedirlo por nombre responde 404 |

Un espacio archivado deja de aparecer en listados y búsquedas, pero conserva todo su contenido y
sus eventos.

### 2.4 Ver y cambiar roles de plataforma

```sh
# Mis roles, mi token y mis espacios
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/me"
# Quién tiene rol de plataforma (auditor, admin o superadmin). Filtro opcional: ?role=admin
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/platform/members"
# Asignar un rol de plataforma (solo superadmin): superadmin, admin, auditor o miembro
curl -fsS -X PUT -H "$H" -H "Content-Type: application/json" -d '{"role":"auditor"}' \
  "$NEXO_URL/nexo/authz/v1/platform/members/<usuario>"
# Quitar el rol (vuelve a miembro)
curl -fsS -X DELETE -H "$H" "$NEXO_URL/nexo/authz/v1/platform/members/<usuario>"
```

La respuesta trae `role_before`, `role_after` y `changed`. Cada cambio queda en los eventos de roles
(`binding.granted`, `binding.changed`, `binding.revoked`) y dispara la alerta
`platform_role_changed`.

## 3. Personas y tokens

La única identidad en Nexo es el **token asignado** por un administrador. Lo que declara la PC
(usuario de Windows, nombre del equipo) se guarda solo como referencia.

### 3.1 Crear una persona

En el panel: **Administración > Usuarios** (`/dashboard/admin/users`), formulario "CREATE MANAGED
USER": usuario, correo (opcional), nombre mostrado (opcional) y rol (`Member` o `Admin`). Por API:

```sh
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"username":"<usuario>","email":"<correo>","display_name":"<Nombre Apellido>","role":"member"}' \
  "$NEXO_URL/admin/users"
```

La respuesta trae `principal_id` (el id numérico). Crear con `"role":"admin"` solo lo puede hacer un
superadmin. Para dar el rol de auditor, cree la persona como `member` y asígnele `auditor` (2.4).

Una persona nueva no sincroniza ningún proyecto hasta que se le da permiso (sección 8), y ve los
espacios abiertos con el rol implícito.

Para listar personas: `curl -fsS -H "$H" "$NEXO_URL/admin/users"`.

### 3.2 Convención de nombres de tokens

El nombre del token identifica la **máquina o el uso**, no a la persona (la persona ya es la dueña
del token). Así la auditoría muestra desde qué equipo se conectó cada uno.

| Uso | Formato | Ejemplos |
|---|---|---|
| Equipo personal | `<tipo>-<usuario>` | `laptop-ana`, `pc-luis`, `mac-ana` |
| Segundo equipo | `<tipo>-<usuario>-<n>` | `laptop-ana-2` |
| Integración continua | `ci-<sistema>` | `ci-github`, `ci-pipeline` |
| Agente o servicio | `agente-<nombre>` | `agente-revisor` |

Solo minúsculas, números y guiones; sin datos sensibles. Un token por máquina o uso. Si se pierde o
se reasigna un equipo, revoque su token y emita uno nuevo con el mismo nombre.

### 3.3 Emitir un token

| Opción | Valor | Por defecto |
|---|---|---|
| Caducidad (`expires_in_days`) | De 1 al máximo del servidor (`NEXO_TOKEN_MAX_DAYS`, 365 por defecto) | 90 días |
| Alcance (`scope`) | `write` (lee y escribe) o `read` (solo descarga y consulta) | `write` |
| Sin caducidad (`expires_in_days: 0`) | Solo cuentas de servicio y solo un superadmin (desde las redes permitidas) | No |

En el panel: detalle de la persona (`/dashboard/admin/users/<id>`), campos "Token label", "Expires in
days (default 90)" y "Scope", botón "Create Token". El token aparece **una sola vez**.

Por API (cualquiera de las dos rutas):

```sh
# API de Nexo: acepta el nombre de usuario
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"principal":"<usuario>","name":"laptop-<usuario>","expires_in_days":90,"scope":"write"}' \
  "$NEXO_URL/nexo/authz/v1/tokens"
# API de administración de Engram: usa el id numérico
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"laptop-<usuario>","expires_in_days":90,"scope":"read"}' \
  "$NEXO_URL/admin/users/<id>/tokens"
```

La respuesta trae `raw_token` (el token, solo esta vez) y `token` (id, nombre, `expires_at`, `scope`).
Entréguelo por un canal seguro; la persona lo instala con `engram nexo connect` (ver
`instalacion-dev.md`).

Cuándo usar `read`: personas que solo consultan el conocimiento y la memoria, sesiones de panel
para mirar, auditores. Con un token `read` no se puede subir memoria, publicar, administrar ni
pausar la sincronización (403 "read-only").

Los tokens de admins, superadmins y auditores solo los emite o revoca un superadmin. No puede
emitir tokens para una persona deshabilitada (`409 principal_disabled`).

### 3.4 Ver los tokens

```sh
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/tokens?principal=<usuario>"   # sin ?principal: los suyos
curl -fsS -H "$H" "$NEXO_URL/admin/users/<id>/tokens"
```

Cada token muestra caducidad, alcance y último uso. `expires_at: null` significa sin caducidad:
son tokens emitidos antes de esta versión y deben rotarse.

### 3.5 Revocar un token

En el panel: detalle de la persona, botón "Revoke" del token. Por API:

```sh
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"reason":"equipo reasignado"}' "$NEXO_URL/admin/tokens/<token-id>/revoke"
```

Qué pasa cuando alguien usa un token revocado:

- Cada petición recibe `401 unauthorized` y queda en la auditoría con el motivo `token_revoked`, la
  IP y el cliente. Los rechazos repetidos se resumen cada 10 minutos.
- Si tenía el panel abierto, la sesión se cierra en la siguiente petición.
- Se crea la alerta `revoked_token_used` (severidad warning, como máximo una por día por caso).
- No bloquea a la persona: su token nuevo sigue funcionando (el bloqueo por persona está desactivado
  por defecto).

Un token vencido recibe `401 unauthorized: token_expired: ...` y genera la alerta
`expired_token_used` (info). La persona necesita un token nuevo.

### 3.6 Renovar antes del vencimiento

1. Revise los tokens por vencer en la revisión de accesos (`tokens_expiring_30d`, sección 9.3).
2. Emita un token nuevo con el mismo nombre de máquina.
3. La persona lo instala (`engram nexo connect --server https://<dominio> --token-stdin`).
4. Revoque el token anterior cuando el nuevo aparezca con `last_used_at`.

### 3.7 Deshabilitar y habilitar personas

No se borran personas: se deshabilitan. Todos sus tokens dejan de funcionar en la siguiente
petición y su historial se conserva.

```sh
curl -fsS -X POST -H "$H" "$NEXO_URL/admin/users/<id>/disable"
curl -fsS -X POST -H "$H" "$NEXO_URL/admin/users/<id>/enable"
```

En el panel: botones "Disable User" y "Enable User" del detalle. Deshabilitar a un admin, superadmin
o auditor solo lo puede hacer un superadmin; el último superadmin no se deshabilita (`409`). Si la
persona era el único responsable de un espacio, ese espacio queda sin responsable: un superadmin lo
recupera (4.4).

### 3.8 Cuentas de servicio (CI y bots)

Una **cuenta de servicio** es una identidad para un pipeline o un bot: publica en los espacios con su
propio token, sin usar el de una persona. La auditoría muestra la cuenta (por ejemplo `ci-wiki`)
como autora de cada publicación.

Reglas:

- Toda cuenta tiene una **persona responsable** (obligatoria): a quien se le pregunta por ella.
- Solo puede ser `lector`, `proponente` o `publicador` en espacios y proyectos. Nunca `responsable`
  ni un rol de plataforma (regla R-10; la base lo impide aunque se intente por SQL).
- Sus tokens vencen como los de las personas (90 días por defecto, máximo `NEXO_TOKEN_MAX_DAYS`).
  Un token **sin caducidad** solo lo emite un superadmin (`expires_in_days: 0`, `--days 0`).
- Deshabilitada, **todos** sus tokens se rechazan desde la siguiente petición; al habilitarla, los
  tokens vigentes vuelven a funcionar.
- El token se muestra **una sola vez** al emitirlo o rotarlo. Nexo nunca lo escribe en sus logs ni
  en la auditoría (solo el id y el prefijo).

| Quién | Qué puede hacer |
|---|---|
| admin o superadmin (`platform.users.manage`) | Crear, cambiar responsable o descripción, deshabilitar y habilitar |
| admin o superadmin (`platform.tokens.manage`) | Emitir, rotar y revocar tokens |
| auditor | Ver las cuentas, sus roles y el último uso de sus tokens |
| responsable del espacio | Darle o quitarle un rol en su espacio (igual que a una persona, 4.3) |

En el panel: pestaña **Cuentas de servicio** (`/dashboard/admin/service-accounts`, solo admins):
listado, alta, detalle con tokens y último uso, deshabilitar, emitir, rotar y revocar.

#### Alta, rol y token con la CLI

La CLI usa el servidor y el token de `engram cloud config` (o `ENGRAM_CLOUD_SERVER` y
`ENGRAM_CLOUD_TOKEN`) de quien administra.

```sh
# 1. Alta (admin): nombre corto, persona responsable y para qué sirve
engram nexo sa create --slug ci-wiki --owner <usuario> --description "Publica la wiki de arquitectura"
# 2. Rol en el espacio (lo da el responsable del espacio)
engram nexo sa grant ci-wiki --space arquitectura --role publicador
# 3. Token (admin): se muestra una sola vez; con --out va a un archivo con permisos 0600
engram nexo sa token issue ci-wiki --name ci-github --days 90 --out token-ci.txt
# Ver la cuenta, sus roles y sus tokens con el último uso
engram nexo sa show ci-wiki
engram nexo sa list --all
```

Cargue el contenido de `token-ci.txt` como **secreto** del CI y borre el archivo. Sin `--out`, el
token es lo único que se imprime en la salida estándar (los mensajes van a la salida de errores).

#### Por API

```sh
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"slug":"ci-wiki","owner":"<usuario>","description":"Publica la wiki de arquitectura"}' \
  "$NEXO_URL/nexo/authz/v1/service-accounts"
curl -fsS -X PUT -H "$H" -H "Content-Type: application/json" -d '{"role":"publicador"}' \
  "$NEXO_URL/nexo/authz/v1/scopes/space/arquitectura/members/ci-wiki"
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"ci-github","expires_in_days":90}' \
  "$NEXO_URL/nexo/authz/v1/service-accounts/ci-wiki/tokens"        # trae raw_token una sola vez
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/service-accounts/ci-wiki" # detalle con tokens y último uso
```

`POST /nexo/authz/v1/tokens` con `"principal":"ci-wiki"` también emite tokens para la cuenta.

#### Rotar un token

Rotar emite un token nuevo (mismo nombre y alcance, salvo que indique otros) y cierra el anterior:
sin gracia lo revoca en el acto; con `--grace-minutes M` (hasta 10080, una semana) el anterior
sigue valiendo M minutos para que el CI cambie el secreto sin cortar publicaciones.

```sh
engram nexo sa show ci-wiki                                          # id del token actual
engram nexo sa token rotate ci-wiki <token-id> --grace-minutes 60 --out token-ci.txt
# cargue el token nuevo en el secreto del CI; pasada la gracia el anterior vence solo, o:
engram nexo sa token revoke ci-wiki <token-id> --reason "rotado"
```

Por API: `POST /nexo/authz/v1/service-accounts/ci-wiki/tokens/<token-id>:rotate` con
`{"grace_minutes":60}` (opcional `name`, `expires_in_days`, `scope`) y
`POST .../tokens/<token-id>:revoke` con `{"reason":"..."}`. La respuesta de la rotación trae
`raw_token`, `token` y `old_token` (`revoked: true` o su nuevo `expires_at`).

#### Último uso

El detalle de la cuenta (panel, `engram nexo sa show` o `GET .../service-accounts/<cuenta>`) muestra
por token:

| Dato | De dónde sale |
|---|---|
| `last_used_at` | Cualquier petición con el token (lo actualiza el servidor en cada una) |
| `last_seen` (`at`, `ip`, `user_agent`, `source`) | La última acción registrada con ese token: subida o publicación de conocimiento (`kb`), sincronización (`sync`) o administración y denegaciones (`authz`) |

Una petición de solo lectura (por ejemplo una búsqueda) actualiza `last_used_at` pero no deja IP.

#### Cuentas sin uso

Una cuenta de servicio **nunca se deshabilita por inactividad** (`NEXO_DISABLE_INACTIVE_DAYS` solo
alcanza a personas, 9.4): un pipeline que publica cada pocos meses es un uso normal. Sus defensas son
la caducidad de sus tokens, la rotación y la persona responsable.

Lo que sí hace el servidor es avisar. Con `NEXO_SA_STALE_DAYS` (180 días por defecto; `0` lo
desactiva; lo fija quien opera el servidor), una cuenta habilitada sin uso en ese plazo:

- deja una alerta `service_account_stale` (9.2) con su nombre corto y su responsable, una sola vez
  por período sin uso (si se vuelve a usar y otra vez queda quieta, avisa de nuevo);
- aparece marcada en `engram nexo sa list` (estado `habilitada, SIN USO` y columna `ULTIMO USO`), en
  `engram nexo sa show`, en el panel (marca "Sin uso" y "sin uso desde hace N días" o "nunca usada
  (creada hace N días)") y en la API (`stale`, `idle_days`, `last_activity_at`, `stale_after_days`);
- lleva la marca `stale` en la revisión de accesos (9.3).

"Uso" es el último uso de cualquiera de sus tokens o la última acción registrada con la cuenta (lo
mismo que muestra el detalle). Si nunca se usó, el plazo cuenta desde el alta; emitir o rotar un token
no cuenta como uso.

Qué hacer con el aviso: pregunte al responsable si la cuenta sigue en uso. Si sigue, reconozca la
alerta con una nota. Si no, revoque sus tokens o deshabilítela (abajo).

#### Deshabilitar y habilitar

```sh
engram nexo sa disable ci-wiki      # todos sus tokens se rechazan (401) desde la siguiente petición
engram nexo sa enable ci-wiki
engram nexo sa update ci-wiki --owner <otro-usuario>   # cuando la persona responsable cambia de equipo
```

Cada acción deja un evento en la auditoría de roles (`GET /nexo/authz/v1/events?action=service_account.*`):
`service_account.created`, `.updated`, `.disabled`, `.enabled`, `.token_issued`, `.token_rotated` y
`.token_revoked`. Un intento sin permiso deja `authz.denied`.

#### Ejemplo: publicar una carpeta de la wiki desde GitHub Actions

Secretos y variables del repositorio (Settings > Secrets and variables > Actions):

| Tipo | Nombre | Valor |
|---|---|---|
| Secreto | `NEXO_TOKEN` | Token de la cuenta de servicio (`write`, rol `publicador` en el espacio) |
| Secreto | `NEXO_RELEASES_TOKEN` | Token de lectura de GitHub para descargar el release del fork (si el repositorio del fork es privado) |
| Variable | `NEXO_SERVER` | `https://<dominio>` |
| Variable | `NEXO_KB_SPACE` | Slug del espacio, por ejemplo `arquitectura` |

```yaml
name: Publicar la wiki en Nexo
on:
  push:
    branches: [main]
    paths: ["wiki/**"]
  workflow_dispatch:
permissions:
  contents: read
concurrency:
  group: nexo-wiki
  cancel-in-progress: false
jobs:
  publicar:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    env:
      NEXO_VERSION: 3.4.0-nexo.1          # versión del fork con engram nexo kb upload
      ENGRAM_CLOUD_SERVER: ${{ vars.NEXO_SERVER }}
      ENGRAM_NO_UPDATE_CHECK: "1"
    steps:
      - uses: actions/checkout@v4
      - name: Instalar engram (fork Nexo)
        env:
          GH_TOKEN: ${{ secrets.NEXO_RELEASES_TOKEN }}
        run: |
          gh release download "v$NEXO_VERSION" -R Adriel2503/engram-nexo \
            -p "engram_${NEXO_VERSION}_linux_amd64.tar.gz" -p checksums.txt -D "$RUNNER_TEMP"
          (cd "$RUNNER_TEMP" && sha256sum -c checksums.txt --ignore-missing)
          tar -xzf "$RUNNER_TEMP/engram_${NEXO_VERSION}_linux_amd64.tar.gz" -C "$RUNNER_TEMP" engram
      - name: Publicar la carpeta wiki
        env:
          ENGRAM_CLOUD_TOKEN: ${{ secrets.NEXO_TOKEN }}   # solo en este paso; nunca en la línea de comandos
        run: |
          "$RUNNER_TEMP/engram" nexo kb upload --space "${{ vars.NEXO_KB_SPACE }}" \
            --collection wiki --note "commit ${GITHUB_SHA::7}" ./wiki
```

Para revisar antes de publicar, agregue un job en `pull_request` con `--dry-run`. La plantilla
completa (dry-run en PR, compilación del fork) está en `docs/nexo/conocimiento/github-actions/`.

#### Ejemplo: publicar una carpeta de la wiki desde Azure Pipelines

En **Pipelines > Library** cree un grupo de variables `nexo` con `NEXO_TOKEN` marcada como
**secreta** (candado), `NEXO_SERVER` y `NEXO_KB_SPACE`. Las variables secretas no llegan solas al
entorno de los scripts: se pasan explícitamente en `env`.

```yaml
trigger:
  branches:
    include: [main]
  paths:
    include: [wiki/*]
pool:
  vmImage: ubuntu-latest
variables:
  - group: nexo
  - name: NEXO_VERSION
    value: 3.4.0-nexo.1
steps:
  - checkout: self
  - bash: |
      set -euo pipefail
      gh release download "v$NEXO_VERSION" -R Adriel2503/engram-nexo \
        -p "engram_${NEXO_VERSION}_linux_amd64.tar.gz" -p checksums.txt -D "$(Agent.TempDirectory)"
      cd "$(Agent.TempDirectory)" && sha256sum -c checksums.txt --ignore-missing
      tar -xzf "engram_${NEXO_VERSION}_linux_amd64.tar.gz" engram
    displayName: Instalar engram (fork Nexo)
    env:
      GH_TOKEN: $(NEXO_RELEASES_TOKEN)      # lectura de GitHub, si el fork es privado (también secreta)
  - bash: |
      "$(Agent.TempDirectory)/engram" nexo kb upload --space "$(NEXO_KB_SPACE)" \
        --collection wiki --note "build $(Build.BuildNumber)" ./wiki
    displayName: Publicar la carpeta wiki
    env:
      ENGRAM_CLOUD_SERVER: $(NEXO_SERVER)
      ENGRAM_CLOUD_TOKEN: $(NEXO_TOKEN)     # secreta: solo en este paso
      ENGRAM_NO_UPDATE_CHECK: "1"
```

Si la wiki es la de Azure DevOps (repositorio `<proyecto>.wiki`), haga `checkout` de ese repositorio
y agregue `--source azure-devops-wiki --wiki-url <url-de-la-wiki>` (5.6).

## 4. Espacios de conocimiento

Un espacio guarda los documentos oficiales de un tema (estándares, lineamientos, glosario). El
identificador (`slug`) va en la URL: minúsculas, números y guiones, de 2 a 63 caracteres, empezando
por letra o número (`^[a-z0-9][a-z0-9-]{1,62}$`). No se puede cambiar después.

### 4.1 Crear un espacio (admin o superadmin)

Todo espacio nace con un **responsable**, que no puede ser quien lo crea (regla de no
auto-asignación) ni una persona deshabilitada.

En el panel: **Conocimiento** (`/dashboard/kb`), formulario de creación: Identificador, Nombre,
Descripción, Responsable (usuario) y Visibilidad ("Abierto a todo el equipo" o "Restringido a sus
miembros"). Por API:

```sh
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" -d '{
  "slug": "arquitectura",
  "name": "Arquitectura",
  "description": "Estándares de arquitectura de software",
  "visibility": "restricted",
  "default_role": "lector",
  "owner": "<usuario-responsable>",
  "categories": ["estandar", "lineamiento", "glosario"]
}' "$NEXO_URL/nexo/kb/v1/spaces"
```

| Campo | Por defecto | Valores |
|---|---|---|
| `visibility` | `open` | `open` o `restricted` |
| `default_role` | `lector` | `lector` o `proponente` (rol implícito en un espacio abierto) |
| `categories` | `estandar`, `convencion`, `lineamiento`, `glosario`, `regla_negocio`, `catalogo` | Minúsculas, números, `_` y `-` (hasta 40 caracteres) |
| `name` | el `slug` | Texto libre |

Si el espacio ya existe: `409`. Si quien crea no tiene rol en un espacio restringido, no lo verá en
su lista (es lo esperado).

### 4.2 Configurar un espacio (responsable)

```sh
# Ver la configuración actual (incluye settings, su rol y sus capacidades)
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>"
# Cambiar nombre, descripción, categorías, visibilidad, rol implícito o ajustes
curl -fsS -X PATCH -H "$H" -H "Content-Type: application/json" \
  -d '{"visibility":"open","default_role":"proponente"}' "$NEXO_URL/nexo/kb/v1/spaces/<espacio>"
```

Campos de `PATCH`: `name`, `description`, `categories`, `visibility`, `default_role` y `settings`.
Ajustes que usa el servidor dentro de `settings`:

| Ajuste | Efecto |
|---|---|
| `review_every_days` | Plazo de revisión: si un documento vigente no tiene versión nueva en esos días, se crea el aviso `kb_doc_review_due`. Sin este ajuste no hay aviso |
| `expiry_warn_days` | Con cuántos días de anticipación avisar que un documento vence (`kb_doc_expiring`). Sin este ajuste: 30 días |

`settings` reemplaza el objeto completo: primero lea el espacio y envíe todos los ajustes que quiere
conservar, por ejemplo `{"settings":{"review_every_days":180,"expiry_warn_days":45}}`.

### 4.3 Miembros y roles de un espacio (responsable)

Solo el **responsable** del espacio asigna roles (capacidad `space.members.manage`). Un admin o
superadmin sin rol en el espacio no puede hacerlo: o lo nombran responsable, o el responsable actual
asigna los roles.

```sh
# Listar miembros (filtro opcional ?role=publicador)
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/scopes/space/<espacio>/members"
# Agregar a una persona o cambiar su rol: lector, proponente, publicador o responsable
curl -fsS -X PUT -H "$H" -H "Content-Type: application/json" -d '{"role":"publicador"}' \
  "$NEXO_URL/nexo/authz/v1/scopes/space/<espacio>/members/<usuario>"
# Quitar a una persona del espacio
curl -fsS -X DELETE -H "$H" "$NEXO_URL/nexo/authz/v1/scopes/space/<espacio>/members/<usuario>"
```

Reglas:

- Un rol por persona y espacio: asignar otro reemplaza el anterior.
- El último responsable habilitado no se puede quitar ni bajar (`409 last_owner`). Para cambiar de
  responsable, primero nombre al nuevo y después quite o baje al anterior.
- Nadie se sube a sí mismo de rol (`403 self_promotion`).
- Una cuenta de servicio no puede ser responsable.
- En un espacio abierto no hace falta agregar a los lectores: ya lo son por el rol implícito. Agregue
  solo a quienes publican o proponen (si el rol implícito es `lector`).
- En un espacio restringido, toda persona que deba verlo necesita un rol.

Auditores y admins pueden listar los miembros de cualquier espacio (`platform.directory.read`).

### 4.4 Archivar, restaurar y recuperar

```sh
# Archivar y restaurar (admin o superadmin)
curl -fsS -X POST -H "$H" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>:archive"
curl -fsS -X POST -H "$H" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>:restore"
# Nombrar responsable en un espacio que se quedó sin ninguno habilitado (solo superadmin)
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" -d '{"owner":"<usuario>"}' \
  "$NEXO_URL/nexo/authz/v1/scopes/space/<espacio>:recover"
```

`:recover` responde `409 scope_has_owner` si el espacio todavía tiene un responsable habilitado: en
ese caso, pídale a esa persona que asigne los roles.

## 5. Documentos

### 5.1 Formatos y reglas

| Tema | Detalle |
|---|---|
| Formatos | Markdown (`.md`, `.markdown`, `.mdx`), texto (`.txt`, `.text`) y Word (`.docx`). Un zip con una carpeta o una wiki exportada de Azure DevOps. Markdown y texto deben estar en UTF-8; el tipo se comprueba por el contenido, no por el nombre |
| Categoría | Obligatoria y debe ser una de las del espacio. Viene en el front matter del documento o en el campo "Categoría" de la subida. Si falta: rechazado con `category is required` |
| Identificador del documento (`doc_key`) | Sale de la ruta del archivo (o de `id` en el front matter). Volver a subir el mismo archivo con el mismo contenido no crea otra versión ("Sin cambios") |
| Secretos | Un documento con secretos evidentes (llaves privadas, tokens de GitHub, Slack, OpenAI o Engram, JWT, claves de Google o AWS, contraseñas en cadenas de conexión, `Bearer` o `api-key` con valor) se **rechaza** (`secret_detected`) con el tipo y la línea, sin mostrar el valor. Quite el secreto (o use un marcador como `<clave>`) y vuelva a subir |
| Originales | No se guardan: para reprocesar un archivo, súbalo de nuevo |
| Carpetas omitidas | `README.md`, archivos y carpetas ocultos, `node_modules` y `eval/` (CLI). En una wiki, `.attachments` y otros adjuntos no se indexan y las páginas vacías se omiten con aviso |

Front matter reconocido en Markdown (también en español):

```markdown
---
title: Nivel de log por tipo de error
category: lineamiento
owner: <responsable del contenido>
tags: [logging, backend]
valid_from: 2026-11-01
valid_until: 2027-11-01
---
```

| Clave | Alternativa en español | Uso |
|---|---|---|
| `id` o `doc_key` | | Identificador estable del documento |
| `title` | `titulo`, `título` | Título |
| `category` | `categoria`, `categoría` | Categoría |
| `owner` | `responsable` | Responsable del contenido (texto, no es un rol) |
| `tags` | `etiquetas` | Lista o texto separado por comas |
| `valid_from` | `vigente_desde` | Inicio de vigencia (`AAAA-MM-DD` o RFC 3339) |
| `valid_until` | `vigente_hasta` | Fin de vigencia |

### 5.2 Límites de subida (valores por defecto del servidor)

| Límite | Valor |
|---|---|
| Subida completa | 256 MB |
| Por archivo | 20 MB |
| Documento convertido | 2 MB |
| Archivos por subida | 2000 |
| Entradas de un zip | 10 000 |
| Descomprimido de un zip | 64 MB (además se rechaza una compresión mayor a 100:1 en entradas de más de 1 MB) |

Los cambia el administrador del servidor (`NEXO_KB_*`, ver [`servidor-produccion.md`](servidor-produccion.md) §4.6).

### 5.3 Subir desde el panel (publicador o responsable)

1. Abra el espacio en `https://<dominio>/dashboard/kb` y vaya a "Subir documentos".
2. Elija archivos sueltos, una carpeta (con subcarpetas) o un zip.
3. Escriba la categoría si los documentos no la traen.
4. En "Opciones avanzadas" puede indicar la colección, el tipo de fuente (Detectar, Archivos, Wiki de
   Azure DevOps) y "Retirar los documentos de esta colección que ya no vienen en la subida".
5. Pulse **Probar sin publicar**. El resultado muestra cada documento como Creado, Actualizado, Sin
   cambios, Rechazado (con el motivo), Retirado u Omitido. No se publica nada.
6. Si el resultado es el esperado, vuelva a subir con **Publicar**.

### 5.4 Subir con la CLI

Primero conecte su PC (una sola vez):

```sh
engram nexo connect --server https://<dominio> --token-stdin     # pega el token y Enter
```

Luego:

```sh
# Prueba sin publicar
engram nexo kb upload --space <espacio> --category lineamiento --dry-run ./lineamientos
# Publicar
engram nexo kb upload --space <espacio> --category lineamiento --note "revisión trimestral" ./lineamientos
# Una wiki de Azure DevOps exportada como zip
engram nexo kb upload --space arquitectura --source azure-devops-wiki --wiki-url <url-de-la-wiki> --dry-run wiki.zip
```

Opciones de `engram nexo kb upload`: `--space`, `--dry-run`, `--prune`, `--confirm-prune`,
`--source auto|dir|azure-devops-wiki` (por defecto `auto`), `--collection`, `--wiki-url`,
`--category`, `--owner`, `--note`, `--json`, `--server`.

Para publicar desde un repositorio en cada cambio (GitHub Actions), vea
`docs/nexo/conocimiento/github-actions/LEEME.md` y el comando `engram nexo kb publish`.

### 5.5 Subir por la API

```sh
curl -fsS -X POST -H "$H" \
  -F "file=@lineamiento-logs.md" -F "category=lineamiento" -F "dry_run=true" \
  "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/uploads"
# Una carpeta: un campo file por archivo, con la ruta relativa como nombre
curl -fsS -X POST -H "$H" \
  -F "file=@docs/backend/logs.md;filename=docs/backend/logs.md" \
  -F "file=@docs/frontend/estilos.md;filename=docs/frontend/estilos.md" \
  -F "category=lineamiento" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/uploads"
# Un zip
curl -fsS -X POST -H "$H" -F "zip=@wiki.zip" -F "source=azure-devops-wiki" -F "dry_run=true" \
  "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/uploads"
```

Campos del formulario: `file` (repetible), `zip`, `source`, `collection`, `wiki_url`, `category`,
`owner`, `change_note`, `tags` (separadas por comas), `dry_run`, `prune`, `confirm_prune`. También
se acepta `?dry_run=1` en la URL. Hace falta un token `write` con `space.publish`.

### 5.6 Wiki de Azure DevOps: carpetas especiales

| Carpeta en la wiki | Qué hace Nexo |
|---|---|
| `[TO_REMOVE]` | Sus páginas se publican **retiradas** (no aparecen en búsquedas, quedan en el historial) |
| `≤-AAAA` (por ejemplo `≤-2023`) | Sus páginas se publican **vencidas** (vigentes hasta el 1 de enero de AAAA+1) con la etiqueta `legado` |
| `[TO_TIDY]` | Se publican normales, con la etiqueta `to-tidy` para ordenarlas después |
| `.attachments` | No se indexa |

El orden de las páginas sale de los archivos `.order` y el título, del nombre de la página. No copie
en ningún documento, ticket o chat credenciales que encuentre en la wiki: si una página las tiene,
será rechazada por la detección de secretos.

### 5.7 Poda: retirar lo que ya no viene

Con "Retirar los documentos de esta colección que ya no vienen" (panel) o `--prune` (CLI), Nexo
retira los documentos de la **misma colección** que no vinieron en la subida. La colección es, por
defecto, el nombre de la carpeta o del zip; manténgala igual entre subidas.

Por seguridad, una poda no retira más del 25 % de los documentos activos de la colección (cuando son
más de 5) sin confirmación. Si se supera, nada se retira y el resultado lo explica (`prune_cap`).
Revise con `--dry-run` y, si es correcto, confirme con la CLI:
`engram nexo kb upload --space <espacio> --prune --confirm-prune ./carpeta` (el panel no tiene la
opción de confirmar).

### 5.8 Versiones y vigencias

Cada publicación con contenido o metadatos distintos crea una **versión** nueva e inmutable. Cambiar
solo las fechas de vigencia en el front matter también crea una versión.

| Estado en el panel | Significa |
|---|---|
| Vigente | Publicado y dentro de su vigencia |
| Programado | `valid_from` en el futuro: todavía no aparece en las búsquedas de hoy |
| Vencido | `valid_until` ya pasó |
| Retirado | Retirado a mano, por poda o por la carpeta `[TO_REMOVE]` |

Consultas útiles:

```sh
# Documentos de un espacio (filtros: status, category)
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/documents"
# Versiones de un documento (si el doc_key tiene "/", escríbalo como %2F)
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/documents/backend%2Flogs/versions"
# Una versión concreta
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/documents/backend%2Flogs?version=2"
# Buscar como si fuera otra fecha
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/search?q=nivel%20de%20log&spaces=<espacio>&at=2026-12-01"
```

En el panel, la página de un documento muestra sus versiones y el enlace "Qué cambió" (secciones
nuevas, quitadas y cambiadas respecto de la versión anterior).

### 5.9 Retirar y reemplazar

En el panel: botón **Retirar** junto al documento (publicador o responsable). Por API:

```sh
# Retirar
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" -d '{"note":"reemplazado por el estándar 2027"}' \
  "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/documents/<doc_key>:retire"
# Marcar como reemplazado por otro documento del espacio
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" -d '{"by":"<doc_key-nuevo>","note":"versión 2027"}' \
  "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/documents/<doc_key>:supersede"
```

Un documento retirado deja de aparecer en las búsquedas, pero su historial se conserva.

### 5.10 Qué cambió

Para saber qué secciones se agregaron, modificaron o retiraron entre dos fechas:

```sh
engram nexo kb changes --since 2026-10-01 --space <espacio>
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/changes?since=2026-10-01&spaces=<espacio>&clock=valid"
```

`clock=valid` (por defecto) muestra lo que empezó o dejó de regir en el periodo; `clock=recorded`
muestra lo que se publicó en el periodo. `until` es opcional (por defecto, ahora).

### 5.11 Últimas subidas y eventos del espacio

```sh
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/publish-runs"   # quién subió qué y el resumen
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/spaces/<espacio>/events"         # filtro opcional ?action=document.retired
```

Requieren `space.audit.read` (publicador o responsable) o `platform.audit.read` (auditor, admin,
superadmin).

### 5.12 Búsqueda por significado: índice vectorial (superadmin)

Esta sección aplica solo si el servidor tiene configurado un proveedor de embeddings
(`NEXO_KB_EMBEDDER`; ver el manual de producción, 4.6.1). Cada sección publicada se vectoriza en
segundo plano. Mientras una sección no tiene vector, se encuentra solo por texto.

```bash
# Estado: versión activa y en construcción, modelo, vectores, pendientes, cartas muertas,
# antigüedad del pendiente más viejo y feed_lag (cambios que el indexador todavía no leyó)
engram nexo kb reindex --status

# Reconstruir el índice vectorial completo en segundo plano (la búsqueda sigue con el actual
# hasta que el nuevo termine). Hace falta después de recalibrar la composición del texto o si
# se sospecha de vectores dañados. Cambiar el proveedor, el modelo o la dimensión no lo necesita:
# al reiniciar, el servidor crea el índice nuevo solo.
engram nexo kb reindex --vectors

# Volver a encolar las secciones que fallaron 5 veces (cartas muertas) de la versión activa
engram nexo kb reindex --vectors --requeue-dead
```

Por la API: `GET /nexo/kb/v1/admin/index` (campo `vector`) y
`POST /nexo/kb/v1/admin/index:reindex?kind=vector[&requeue_dead=1&version=N]`.

| Señal | Qué significa | Qué hacer |
|---|---|---|
| `pending` o `feed_lag` altos durante mucho tiempo | El proveedor no responde o la cuota se agotó | Revisar el log (`vectorización en pausa`); el indexador reintenta solo |
| `dead` mayor que 0, alerta `kb_index_dead_letter` en `nexo_alerts` | Una sección que el proveedor rechaza siempre (por ejemplo, un texto inválido) | Revisar `last_error` en `nexo_kb_index_dead`; corregir el documento o reencolar con `--requeue-dead` |
| La respuesta trae `vector.status = "failed"` y `notice` | El proveedor no respondió a la pregunta: se respondió solo por texto | Revisar la clave y la conectividad |
| La respuesta trae `vector.status = "unavailable"` | No hay índice activo (en construcción) o falta pgvector | Esperar a que termine, o instalar pgvector |

**Elegir el modo de búsqueda.**

- Por defecto, la búsqueda es híbrida (`hybrid`) cuando hay un índice activo.
- `mode=fts` busca solo por palabras y `mode=exact` busca literales o una expresión.
- El modo se elige en la API, en `nexo_search`, en el selector "Cómo buscar" del panel y con
  `engram nexo kb search --mode`.
- La copia local de la PC no tiene vectores: sin conexión, la búsqueda es solo por palabras.

**Medir antes de cambiar umbrales.** Compare los dos motores con el conjunto de preguntas del
espacio:

```bash
engram nexo kb eval preguntas.yaml --retriever pgfts --record
engram nexo kb eval preguntas.yaml --retriever hybrid --wait-index --record
```

`--wait-index` necesita el rol de superadmin. Las dos corridas quedan en `nexo_kb_eval_runs` para
compararlas.

### 5.13 Reordenamiento de los mejores resultados (superadmin)

Esta sección aplica solo si el servidor tiene configurado un reordenador (`NEXO_KB_RERANKER`; ver el
manual de producción, 4.6.2). Pone primero el resultado que mejor responde la pregunta. Solo
reordena lo que la búsqueda ya encontró con sus permisos y su umbral: no agrega resultados ni cambia
la respuesta "no existe un estándar oficial".

**Cómo se activa.**

1. Configure el proveedor y su clave en el entorno del servidor y reinicie (manual de producción,
   4.6.2). No hay migración ni índice que construir.
2. Compruebe el estado (superadmin): `GET /nexo/kb/v1/admin/index`, campo `rerank`.
   Muestra `enabled`, el proveedor y el modelo, las opciones (`top_n`, `blend`, `min_score`,
   `budget`, `default`) y el uso acumulado desde que arrancó el servidor (llamadas, reintentos,
   lotes fallidos y tokens).
3. Pruebe una consulta pidiendo el reordenamiento:

```bash
engram nexo kb search --rerank on --json "<pregunta>"
```

En la respuesta, `rerank.status` dice qué pasó:

| `rerank.status` | Significado | Qué hacer |
|---|---|---|
| `used` | Se reordenó. `rerank.latency_ms` es el tiempo que sumó; cada resultado trae `rerank_score` y `pre_rerank_position` | — |
| `skipped` | Había menos de 2 resultados, o el modo es `exact` | Nada: no hay qué reordenar |
| `unavailable` | Se pidió `rerank=on` y el servidor no tiene reordenador | Configurar `NEXO_KB_RERANKER` |
| `failed` o `partial` | El proveedor falló, tardó más que el presupuesto o respondió mal. La respuesta conserva el orden normal y trae `notice` | Revisar la clave, el modelo (un 404 es un modelo retirado) y la conectividad en el log (`reordenamiento fallido`) |

**Cómo se elige.**

- `rerank=on` u `off` en la API (`GET /search?rerank=on`), en `nexo_search` (parámetro `rerank`) y
  con `engram nexo kb search --rerank`. Sin el parámetro decide `NEXO_KB_RERANK_DEFAULT`
  (apagado por defecto).
- El panel no tiene selector: usa lo que decida el servidor.
- La copia local de la PC no reordena.

**Cómo se mide antes de activarlo por defecto.** Compare los mismos motores con las preguntas del
espacio; las dos corridas quedan en `nexo_kb_eval_runs`:

```bash
engram nexo kb eval preguntas.yaml --retriever hybrid --wait-index --record
engram nexo kb eval preguntas.yaml --retriever hybrid --rerank on --record
```

`--rerank` es `off` por defecto en `eval` (para que la comparación sea explícita), `on` mide el
motor `hybrid+rerank` y `auto` deja decidir al servidor. Al final, la CLI imprime una línea con las
consultas reordenadas, las preguntas que mejoraron y empeoraron, y la latencia agregada p50, p95 y
máxima; cada pregunta guarda su posición antes y después. Con la salida `--json` se ve el detalle.

Antes de activarlo por defecto compruebe, con las preguntas validadas del espacio:

1. El MRR y el nDCG@3 de `hybrid+rerank` son mejores que los de `hybrid`.
2. El acierto de "no hay respuesta" no baja (no debería: el reordenador no toca el umbral).
3. La latencia p95 agregada cabe en lo que acepta quien busca.
4. El costo por consulta (tokens en `GET /admin/index`) es aceptable.

En la wiki de arquitectura (50 preguntas borrador), `gemini-3.1-flash-lite` subió el MRR de 0,813 a
0,865 con 1,9 s de latencia p95, sin cambiar el acierto total ni el de "no hay" (diseño, `design.md`
§21.5). Si cumple, active `NEXO_KB_RERANK_DEFAULT=on` y reinicie. Para volver atrás, `off` o quite
`NEXO_KB_RERANKER`: la búsqueda queda como antes.

**Descarte por puntaje mínimo** (`NEXO_KB_RERANK_MIN_SCORE`). Está apagado. Descarta los resultados
con puntaje bajo y puede subir el acierto de "no hay", pero la escala cambia con el modelo: en la
medición, 0,4 corrigió un falso "hay" con `gemini-3.8-flash` y no hizo nada con
`gemini-3.1-flash-lite`, y 0,6 perdía aciertos. Actívelo solo después de medirlo con sus preguntas
validadas y con el modelo que use.

## 6. Propuestas

Un proponente envía una propuesta (un documento nuevo o un cambio). No es oficial hasta que un
publicador o responsable la aprueba. Los desarrolladores suelen proponer desde su asistente
(herramienta `nexo_propose`); quien revisa puede usar `nexo_proposals` y `nexo_review` desde su
asistente, o la API. El panel no tiene pantalla de propuestas.

```sh
# Propuestas abiertas de un espacio (estados: open, approved, discarded, withdrawn)
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/proposals?space=<espacio>&status=open"
# Ver una propuesta
curl -fsS -H "$H" "$NEXO_URL/nexo/kb/v1/proposals/<id>"
# Aprobar (publica la versión). Opcional: editar título o contenido, fijar vigencia, doc_key
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"note":"aprobado en comité","valid_from":"2026-11-01T00:00:00Z"}' \
  "$NEXO_URL/nexo/kb/v1/proposals/<id>:approve"
# Descartar: el motivo es obligatorio y lo ve el autor
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"reason":"ya existe el lineamiento de logs; proponer el cambio sobre ese documento"}' \
  "$NEXO_URL/nexo/kb/v1/proposals/<id>:discard"
```

Campos de `:approve`: `content`, `title`, `doc_key` (si la propuesta es de un documento nuevo y no
lo trae), `valid_from`, `valid_until`, `note` y `force`. Si el documento cambió después de que se hizo
la propuesta, la aprobación responde `409 version_conflict`: revise la versión actual y apruebe con
`"force":true` solo si el cambio sigue siendo válido. El autor puede retirar su propuesta con
`:withdraw`. Una propuesta con secretos evidentes se rechaza al crearla.

## 7. Avisos de vencimiento y revisión

El servidor crea dos tipos de aviso de la capa conocimiento, una vez por documento y estado:

| Aviso | Cuándo |
|---|---|
| `kb_doc_expiring` | Un documento vigente vence dentro de `expiry_warn_days` del espacio (30 días si no está definido) |
| `kb_doc_review_due` | Un documento vigente no tiene versión nueva en `review_every_days` del espacio |

Los avisos se consultan en `GET /nexo/admin/alerts?layer=conocimiento` (auditor, admin o
superadmin) y llegan al webhook del equipo si está configurado. Los responsables de espacio que no
tienen rol de plataforma no pueden consultar esa ruta: acuerde que el admin les reenvíe los avisos
(o que reciban el canal del webhook) y, mientras tanto, revisen en el panel los documentos marcados
como Vencido.

## 8. Proyectos de memoria

Un proyecto es la memoria compartida de un equipo (por ejemplo `nexo-transversal`). En esta versión:

| Tema | Cómo funciona |
|---|---|
| Qué proyectos acepta el servidor | Los de `ENGRAM_CLOUD_ALLOWED_PROJECTS` (lo define quien opera el servidor) |
| Quién sincroniza un proyecto | La persona con **permiso de proyecto** (de Engram). Equivale a publicador: puede escribir y cambiar memorias de otros |
| Dar permiso | Panel: detalle de la persona, campo "Project name" y botón "Grant Project". API: `POST /admin/users/<id>/grants` con `{"project":"<proyecto>"}` |
| Quitar permiso | Panel: botón "Revoke" del permiso. API: `POST /admin/users/<id>/grants/<proyecto>/revoke` |
| Ver permisos | `GET /admin/users/<id>/grants` |
| Auto-asignación | Nadie se da permiso a sí mismo (`403 self_grant_forbidden`). Hace falta `platform.users.manage` (admin o superadmin) |
| Pausar la sincronización | Panel: **Administración > Proyectos** (requiere token `write`) |

```sh
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" -d '{"project":"nexo-transversal"}' \
  "$NEXO_URL/admin/users/<id>/grants"
```

Los roles de proyecto de Nexo (lector, proponente, publicador, responsable con
`/nexo/authz/v1/scopes/project/...`) ya se pueden registrar, pero la sincronización todavía no los
usa (etapa R2 pendiente). No los use para dar o quitar acceso a la memoria.

Por verificar: si `ENGRAM_CLOUD_ALLOWED_PROJECTS` también limita a las personas con token asignado.
En el código revisado, la autorización de una persona con token asignado consulta solo sus permisos
de proyecto; mantenga la lista del servidor y los permisos alineados.

### 8.1 Registro: quién se conecta y quién cambió qué

| Pregunta | Ruta | Filtros |
|---|---|---|
| Qué pasó en la seguridad (logins, rechazos, usuarios, tokens, permisos) | `GET /nexo/admin/audit` | `from`, `to`, `action` (exacta o prefijo con `*`, por ejemplo `token.*`), `actor`, `outcome` (`success`, `denied`), `cursor`, `limit` |
| Quién subió o descargó memoria | `GET /nexo/admin/activity` | `from`, `to`, `actor` (o `no-identificado`), `token`, `project`, `operation` (`push_chunk`, `push_mutations`, `pull_chunk`, `pull_mutations`), `cursor`, `limit` |
| Quién se conecta y desde dónde | `GET /nexo/admin/presence` | `actor`, `since`, `cursor`, `limit` |
| Historial de una memoria | `GET /nexo/admin/history/<sync_id>` | `project`, `entity`, `include_payload=true`, `cursor`, `limit` |

Fechas en RFC 3339 o `AAAA-MM-DD` (UTC); `to` es exclusivo. Requieren auditor, admin o superadmin.
Cada consulta queda auditada (`nexo.audit.read`, `nexo.activity.read`, etc.).

```sh
curl -fsS -H "$H" "$NEXO_URL/nexo/admin/audit?outcome=denied&from=2026-10-05"
curl -fsS -H "$H" "$NEXO_URL/nexo/admin/presence"
```

## 9. Auditoría y revisión de accesos

### 9.1 Qué se registra

| Registro | Contenido | Dónde se consulta |
|---|---|---|
| Auditoría de seguridad (`cloud_auth_audit_log`) | Logins y rechazos del panel y de la sincronización, alta y baja de personas, tokens, permisos, límites (`nexo.rate_limited`), bloqueos (`auth.lockout`) y las propias consultas de auditoría; con IP y cliente | `GET /nexo/admin/audit`; panel **Administración > Auditoría** (`/dashboard/admin/audit-log`) |
| Eventos de roles (`nexo_authz_events`) | Asignaciones, cambios y quitas de roles, espacios creados, archivados y recuperados, denegaciones (`authz.denied`), depuraciones, cuentas deshabilitadas por inactividad | `GET /nexo/authz/v1/events` o `GET /nexo/admin/authz-events` |
| Eventos de conocimiento (`nexo_kb_events`) | Espacios, publicaciones, retiros, reemplazos, propuestas y subidas | `GET /nexo/kb/v1/spaces/<espacio>/events` |
| Actividad y presencia | Subidas y descargas de memoria; última conexión por persona y token | `GET /nexo/admin/activity` y `/presence` |
| Alertas | Ver [`servidor-produccion.md`](servidor-produccion.md) §11.3 | `GET /nexo/admin/alerts` |

Filtros de los eventos de roles: `scope_type` (`platform`, `project`, `space`), `scope_key`,
`action` (por ejemplo `binding.granted`, `authz.denied` o un prefijo con `*`), `principal`, `from`,
`to`, `cursor`, `limit`.

```sh
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/events?action=authz.denied&from=2026-10-01"
curl -fsS -H "$H" "$NEXO_URL/nexo/admin/alerts?unacked=true"
```

La auditoría, los eventos de roles y los de conocimiento están encadenados con hashes: cualquier
alteración se detecta (`GET /nexo/admin/audit/verify`).

### 9.2 Alertas

```sh
curl -fsS -H "$H" "$NEXO_URL/nexo/admin/alerts?unacked=true&severity=high"
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" -d '{"note":"revisado con la persona"}' \
  "$NEXO_URL/nexo/admin/alerts/<id>:ack"
```

Reconocer una alerta exige un token `write` (también para el auditor) y solo se puede hacer una vez.

### 9.3 Revisión de accesos

La revisión muestra, por persona: rol de plataforma (con fecha y quién lo asignó), roles en
proyectos y espacios, permisos de proyecto, tokens (caducidad, alcance, último uso), días sin
actividad y marcas `inactive`, `token_no_expiry` y `token_never_used`. El resumen incluye personas
habilitadas, inactivas, tokens activos, tokens sin caducidad, tokens nunca usados y tokens que vencen
en 30 días.

```sh
# Generar el reporte (umbral de inactividad: 90 días por defecto; de 1 a 3650)
curl -fsS -H "$H" "$NEXO_URL/nexo/admin/access-review?inactive_days=90" > revision-$(date +%F).json
# Registrar que se hizo la revisión (quién, cuándo, notas y hash del reporte)
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"notes":"Q4 2026: 3 tokens rotados, 2 personas deshabilitadas","inactive_days":90}' \
  "$NEXO_URL/nexo/admin/access-review:complete"
```

Qué hacer con lo que aparece:

| Marca | Acción |
|---|---|
| `inactive` | Confirme con el área si la persona sigue; si no, deshabilítela (3.7) |
| `stale` | Cuenta de servicio sin uso en `NEXO_SA_STALE_DAYS` días (aparece con su nombre corto como usuario): confirme con su responsable; si sobra, revoque sus tokens o deshabilítela (3.8). Nunca se deshabilita sola |
| `token_no_expiry` | Token anterior a esta versión: emita uno con caducidad y revoque el viejo |
| `token_never_used` | Token entregado que nunca se usó: confirme y revoque si sobra |
| Rol de plataforma o responsable que ya no corresponde | Ajuste el rol (2.4, 4.3) |

### 9.4 Usuarios inactivos

Si quien opera el servidor configura `NEXO_DISABLE_INACTIVE_DAYS` (mínimo 7; desactivado por
defecto), el servidor deshabilita una vez por día las **personas** sin uso en ese plazo, nunca al
último superadmin. Cada caso deja el evento `principal.disabled_inactive` y la alerta
`inactive_account_disabled`. Para devolver el acceso, habilite la cuenta (3.7).

Las cuentas de servicio no se deshabilitan por inactividad: con `NEXO_SA_STALE_DAYS` (180 días por
defecto, `0` = sin aviso) solo dejan la alerta `service_account_stale` y la marca "sin uso" (3.8).

| Variable (la fija quien opera el servidor) | Por defecto | Alcance |
|---|---|---|
| `NEXO_DISABLE_INACTIVE_DAYS` | vacía (desactivado) | Deshabilita personas sin uso (mínimo 7 días) |
| `NEXO_SA_STALE_DAYS` | `180` | Avisa de cuentas de servicio sin uso; nunca las deshabilita (`0` = sin aviso) |

## 10. Tareas periódicas recomendadas

| Frecuencia | Tarea | Quién | Cómo |
|---|---|---|---|
| Diaria (o al recibir el webhook) | Revisar alertas no reconocidas y reconocerlas con una nota | Admin o auditor | 9.2 |
| Semanal | Revisar rechazos de autenticación y denegaciones repetidas (PCs con tokens viejos o mal pegados) | Admin | `GET /nexo/admin/audit?outcome=denied&from=...`, `GET /nexo/authz/v1/events?action=authz.denied` |
| Semanal | Revisar propuestas abiertas de cada espacio | Publicadores y responsables | 6 |
| Mensual | Renovar tokens que vencen en los próximos 30 días | Admin | 3.6 y `tokens_expiring_30d` |
| Mensual | Revisar avisos de vencimiento y revisión de documentos y actualizar o retirar | Responsables | 7, 5.9 |
| Mensual | Confirmar que la cadena de auditoría verifica y que la exportación firmada se copió fuera del host | Admin (con quien opera el servidor) | `GET /nexo/admin/audit/verify`; `servidor-produccion.md` §11.5 |
| Trimestral | Revisión de accesos registrada | Admin y auditor | 9.3 |
| Trimestral | Revisar miembros y responsables de cada espacio restringido | Responsables | 4.3 |
| Trimestral | Revisar quién tiene rol de plataforma (dos superadmins, sin roles de más) | Superadmin | 2.4 |

## 11. Problemas frecuentes

| Síntoma | Causa probable | Qué hacer |
|---|---|---|
| `401 unauthorized: token_expired` | El token venció | Emitir uno nuevo (3.3) |
| `401` con un token que antes funcionaba | Token revocado o persona deshabilitada | Revisar `GET /nexo/admin/audit?outcome=denied` (motivo `token_revoked` o `principal_disabled`); emitir token nuevo o habilitar |
| `403 ... read-only (scope read)` | Token de solo lectura | Emitir un token `write` si la persona debe escribir |
| `403 ... (network)` | Acción de superadmin fuera de `NEXO_SUPERADMIN_ALLOWED_CIDRS` | Conectarse desde la red de administración o la VPN |
| `403 self_promotion` o `self_grant_forbidden` | Intento de darse un rol o permiso a sí mismo | Pedirlo a otro admin o al responsable |
| `403 ... space.members.manage is required` | Un admin sin rol intenta agregar miembros a un espacio | Lo hace el responsable del espacio (4.3) |
| `404` al abrir un espacio | El espacio es restringido y la persona no tiene rol, o no existe | Pedir al responsable que la agregue |
| `409 last_owner` | Se intentó quitar al último responsable | Nombrar primero a otro responsable |
| `409 last_superadmin` | Se intentó degradar o deshabilitar al último superadmin | Promover antes a otro superadmin |
| `409 scope_has_owner` en `:recover` | El espacio tiene responsable habilitado | Pedirle a esa persona que asigne roles |
| `409 principal_disabled` al emitir token | Persona deshabilitada | Habilitarla primero |
| Documento "Rechazado" con `secret_detected` | El documento tiene una credencial | Quitarla y volver a subir |
| Documento "Rechazado" por categoría | Falta la categoría o no es del espacio | Indicarla en la subida o en el front matter; o agregarla al espacio (4.2) |
| La poda no retiró nada (`prune_cap`) | Se superó el tope de seguridad | Revisar con `--dry-run` y confirmar con `--confirm-prune` (5.7) |
| `413` al subir | Archivo o subida demasiado grande | Dividir la subida o pedir que se ajusten los límites |
| `409 version_conflict` al aprobar | El documento cambió después de la propuesta | Revisar y aprobar con `"force":true` si corresponde |
| `429 rate_limited` | Demasiadas peticiones por minuto | Esperar lo que indica `Retry-After`; si es habitual, pedir ajustar `NEXO_RATE_LIMITS` |
| `429 auth_locked` | Muchos tokens inválidos distintos desde la misma IP | Esperar 15 minutos y revisar qué PC usa tokens inválidos (auditoría `sync.auth`, `unknown_token`, con IP y cliente) |
| Los enlaces de las citas salen con `http://` | Detrás de Caddy, el servidor no tiene a Caddy en `NEXO_TRUSTED_PROXIES` (y tampoco `NEXO_KB_PUBLIC_URL`) | Pedir a quien opera el servidor que configure `NEXO_TRUSTED_PROXIES` con la IP de Caddy (los enlaces toman el esquema y el host de `X-Forwarded-Proto` y `X-Forwarded-Host`) o `NEXO_KB_PUBLIC_URL=https://<dominio>`, que manda sobre lo anterior |
| Una persona no sincroniza un proyecto | No tiene permiso de proyecto, o el proyecto no está en la lista del servidor | Dar el permiso (8) y revisar `ENGRAM_CLOUD_ALLOWED_PROJECTS` |
| La alerta `shared_token_used` aparece | Alguien usa el token compartido del servidor | Identificar la PC por IP y cliente y entregarle un token personal |

## Anexo A. Referencia rápida de rutas

| Ruta | Capacidad |
|---|---|
| `GET /nexo/authz/v1/me` | cualquiera con token asignado |
| `GET /nexo/authz/v1/platform/members` | `platform.directory.read` |
| `PUT` y `DELETE /nexo/authz/v1/platform/members/<usuario>` | `platform.admins.manage` |
| `GET /nexo/authz/v1/tokens[?principal=]` | `self.read` (propios) o `platform.tokens.manage` |
| `POST /nexo/authz/v1/tokens` | `platform.tokens.manage` |
| `GET /nexo/authz/v1/service-accounts`, `GET /nexo/authz/v1/service-accounts/<cuenta>` | `platform.directory.read` |
| `POST /nexo/authz/v1/service-accounts`, `PATCH .../<cuenta>`, `POST .../<cuenta>:disable` y `:enable` | `platform.users.manage` |
| `POST /nexo/authz/v1/service-accounts/<cuenta>/tokens`, `.../tokens/<id>:rotate` y `:revoke` | `platform.tokens.manage` (sin caducidad: además `platform.admins.manage`) |
| `GET`, `POST /admin/users`; `POST /admin/users/<id>/enable` y `/disable` | `platform.users.manage` (lectura: también auditor) |
| `GET`, `POST /admin/users/<id>/tokens`; `POST /admin/tokens/<id>/revoke` | `platform.tokens.manage` |
| `GET`, `POST /admin/users/<id>/grants`; `POST /admin/users/<id>/grants/<proyecto>/revoke` | `platform.users.manage` |
| `GET`, `POST /nexo/kb/v1/spaces` | listar: cualquiera; crear: `platform.scope.create` y `platform.scope.assign_owner` |
| `GET`, `PATCH /nexo/kb/v1/spaces/<espacio>` | `space.read`; `space.configure` |
| `POST /nexo/kb/v1/spaces/<espacio>:archive` y `:restore` | `platform.scope.archive` |
| `GET /nexo/authz/v1/scopes/space/<espacio>/members` | `space.read` o `platform.directory.read` |
| `PUT` y `DELETE /nexo/authz/v1/scopes/space/<espacio>/members/<usuario>` | `space.members.manage` |
| `POST /nexo/authz/v1/scopes/space/<espacio>:recover` | `platform.scope.recover` |
| `POST /nexo/kb/v1/spaces/<espacio>/uploads` | `space.publish` |
| `POST /nexo/kb/v1/spaces/<espacio>/documents/<doc_key>:retire` y `:supersede` | `space.publish` |
| `GET /nexo/kb/v1/proposals`, `POST /nexo/kb/v1/proposals/<id>:approve` y `:discard` | propias, o `space.review` |
| `GET /nexo/kb/v1/changes?since=` | `space.read` |
| `GET /nexo/kb/v1/spaces/<espacio>/events` y `/publish-runs` | `space.audit.read` o `platform.audit.read` |
| `GET /nexo/admin/audit`, `/activity`, `/presence`, `/history/<sync_id>` | `platform.audit.read` |
| `GET /nexo/authz/v1/events`, `GET /nexo/admin/authz-events` | `platform.audit.read` |
| `GET /nexo/admin/audit/verify`, `GET /nexo/admin/alerts`, `GET /nexo/admin/access-review` | `platform.audit.read` |
| `POST /nexo/admin/alerts/<id>:ack`, `POST /nexo/admin/access-review:complete` | `platform.audit.read` y token `write` |
| `GET /nexo/kb/v1/admin/index`, `POST /nexo/kb/v1/admin/index:reindex` | `platform.settings` |

## Anexo B. Puntos por verificar

| Tema | Motivo |
|---|---|
| Cuentas de servicio y versión del fork | Las cuentas de servicio (3.8) llegan en la versión siguiente a `3.4.0-nexo.1`. Con un servidor anterior, use un usuario dedicado (por ejemplo `ci-lineamientos`) con rol publicador y token con caducidad. Los ejemplos de CI solo necesitan `engram nexo kb upload`, que ya existe en `3.4.0-nexo.1` |
| `ENGRAM_CLOUD_ALLOWED_PROJECTS` y personas con token asignado | La autorización por proyecto de una persona consulta solo sus permisos; no se confirmó si la lista del servidor también la limita |
| Avisos de conocimiento para responsables | La ruta de alertas exige `platform.audit.read`; no hay una vista de avisos para el responsable del espacio |
