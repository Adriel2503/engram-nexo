# Manual de administración de Nexo IA

| | |
|---|---|
| Versión de Nexo | `3.4.0-nexo.2` (base Engram v3.4.0) |
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
| Panel web | Usuarios y tokens, cuentas de servicio, permisos de proyecto, crear espacios, miembros de cada espacio, subir y retirar documentos, buscar, revisar propuestas | `https://<dominio>/dashboard/` (entrar con su token). Conocimiento en `https://<dominio>/dashboard/kb`; cuentas de servicio en `https://<dominio>/dashboard/admin/service-accounts` |
| API con `curl` | Todo lo anterior y además: roles de plataforma, miembros de espacios, propuestas, avisos, auditoría y revisión de accesos | `https://<dominio>/...` con `Authorization: Bearer <token>` |
| CLI `engram nexo` | Subir carpetas o la wiki, ver cambios, reindexar, verificar la auditoría, cuentas de servicio (`engram nexo sa`) | En su PC conectada (`engram nexo connect`) o dentro del contenedor del servidor |

Los miembros de un espacio se gestionan en el panel (sección 4.3) o con la API `/nexo/authz/v1`. Los
roles de plataforma no tienen pantalla: se cambian con la API (sección 2.4). Las propuestas se revisan
en el panel (sección 6.1), con las herramientas `nexo_*` o con la API.

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
- **Mantenga dos superadmins.** Con uno solo, si pierde su token o su cuenta nadie puede gestionar admins
  ni la configuración. El servidor deja la alerta alta `single_superadmin` (y la marca `single_superadmin`
  en la revisión de accesos) mientras quede uno solo. Se crea el segundo en Administración > Usuarios con el
  rol Superadmin (o por API); ver `servidor-produccion.md`, 7.3.
- Nadie se asigna a sí mismo un rol mayor al que tiene (`403 self_promotion`).

### 2.1.1 Un solo rol de plataforma: el de Nexo

Cada persona tiene **un solo rol de plataforma**, el de Nexo (miembro, auditor, admin o superadmin).
Es el único que se elige, se muestra y se audita. Engram guarda además una columna `role`
(`admin` o `member`) que usan sus pantallas y consultas, pero **se deriva** del rol de Nexo y nadie la
edita aparte: cualquier cambio de rol (el formulario del panel, la API `/admin/users`, el comando de
arranque o la API de roles) pasa por Nexo, que actualiza las dos cosas en la misma transacción y deja el
evento de auditoría.

| Rol de plataforma (Nexo) | Lo que Engram ve (columna `role`) | Quién lo asigna |
|---|---|---|
| Miembro | `member` | Cualquier admin, al crear la persona |
| Auditor | `member` | Solo un superadmin |
| Administrador (`admin`) | `admin` | Solo un superadmin |
| Superadmin | `admin` | Solo un superadmin; el primero lo crea `engram cloud bootstrap admin` |

- **Desde el panel:** Administración > Usuarios. Al crear, se elige uno de los cuatro roles (un admin solo
  puede elegir Miembro: los otros salen desactivados y el servidor los rechaza con 403). En el detalle de
  una persona, el superadmin cambia su rol con confirmación; la lista y el detalle muestran el rol de Nexo
  en español y la cabecera lo muestra junto a tu nombre.
- **Reglas que siguen valiendo:** nadie se asigna un rol mayor al que tiene (403); el último superadmin
  habilitado no se degrada ni se deshabilita (409); una cuenta de servicio solo puede ser miembro.
- **Token heredado `ENGRAM_CLOUD_ADMIN`:** no tiene rol de Nexo. Como hasta ahora, puede ver las
  pantallas de administración del panel, pero no crea personas, ni emite tokens, ni cambia roles.
- **Si la columna de Engram y el rol de Nexo difieren** (alguien desactivó los triggers de la base o se
  restauró una copia vieja): la columna no concede nada, la alerta `platform_role_drift` avisa, la
  revisión de accesos marca `role_drift` y `engram nexo authz reconcile-roles` lo corrige (ver 2.4).

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

Desde el panel (superadmin): **Administración > Usuarios**, abrir a la persona, elegir el rol en
"Cambiar a", **Cambiar rol** y **Confirmar cambio**. Se registra igual que por la API.

La columna de rol de Engram se actualiza sola (superadmin y admin = `admin`; auditor y miembro =
`member`). Para comprobar que ambas coinciden, o corregirlas si no (en seco por defecto):

```sh
# Desde el contenedor o el servidor, con ENGRAM_DATABASE_URL definida
engram nexo authz reconcile-roles            # lista las diferencias; sale con 1 si hay alguna
engram nexo authz reconcile-roles --apply    # recalcula la columna desde el rol de Nexo (nunca al revés)
```

Cada corrección deja un evento `engram_role.synced`. El comando es idempotente.

## 3. Personas y tokens

La única identidad en Nexo es el **token asignado** por un administrador. Lo que declara la PC
(usuario de Windows, nombre del equipo) se guarda solo como referencia.

### 3.1 Crear una persona

En el panel: **Administración > Usuarios** (`/dashboard/admin/users`), formulario "Crear
usuario": usuario, correo (opcional), nombre para mostrar (opcional) y rol de plataforma (Miembro, Auditor,
Administrador o Superadmin; solo un superadmin puede elegir uno distinto de Miembro). Por API:

```sh
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"username":"<usuario>","email":"<correo>","display_name":"<Nombre Apellido>","role":"member"}' \
  "$NEXO_URL/admin/users"
```

La respuesta trae `principal_id` (el id numérico) y `platform_role` (el rol de Nexo). `role` acepta
`member` o `miembro`, `auditor`, `admin` y `superadmin`; crear con cualquiera que no sea miembro solo lo
puede hacer un superadmin (un admin recibe 403 y no se crea nada). Un valor distinto responde 400. La
columna `role` de la respuesta es la que ve Engram y se deriva del rol de Nexo (2.1.1). Para cambiar el rol
después, use el detalle de la persona en el panel o la API de roles (2.4).

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
se reasigna un equipo, revoque su token y emita uno nuevo con el mismo nombre. El servidor rechaza
(`400`) un nombre de más de 256 caracteres, con caracteres de control o que contenga una
credencial (token, código de activación, cabecera `Bearer`); vale para todas las rutas de creación,
también la autoemisión y las cuentas de servicio.

### 3.3 Emitir una credencial para una persona

| Opción | Valor | Por defecto |
|---|---|---|
| Vida del token (`expires_in_days`) | De 1 al máximo del servidor (`NEXO_TOKEN_MAX_DAYS`, 365 por defecto) | 90 días desde el canje |
| Alcance (`scope`) | `write` (lee y escribe) o `read` (solo consulta) | `write` |
| Plazo del código (`NEXO_ACTIVATION_TTL`) | Duración de Go entre `1h` y `168h`. Un valor inválido **impide arrancar el servidor**: el error nombra la variable y no muestra el valor | 24 horas |

Para **otra persona**, el administrador recibe un **código de activación**, nunca su token.
En el detalle de usuario del panel, complete nombre, días y alcance y pulse **Crear código de
activación**. El código se muestra una sola vez, con su fecha de vencimiento e instrucciones.
Entréguelo a esa persona por un canal seguro; no lo guarde en tickets, chats ni repositorios.

Las dos API de emisión conservan URL, cuerpo y estado `201`, pero cambian la respuesta para
personas ajenas. Es una incompatibilidad deliberada: clientes que esperaban `raw_token` deben
comprobar `credential_type: "activation_code"` y entregar `activation_code`. La respuesta incluye
`activation` (id, persona, nombre, alcance, vida del token, emisor y fechas); **no incluye
`raw_token` ni crea un token hasta el canje**.

```sh
# Nexo: usuario o id
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"principal":"<usuario>","name":"laptop-<usuario>","expires_in_days":90,"scope":"write"}' \
  "$NEXO_URL/nexo/authz/v1/tokens"
# Administración de Engram: id numérico
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"name":"laptop-<usuario>","expires_in_days":90,"scope":"read"}' \
  "$NEXO_URL/admin/users/<id>/tokens"
```

**Canje por la persona:** sin sesión previa, abra `/dashboard/activate` (**Activar mi cuenta**,
también enlazada desde el login). Recibe su token una vez y lo guarda en su gestor de contraseñas.
Para conectar la PC directamente, use:

```sh
engram nexo connect --server https://<dominio> --activate-stdin
```

En una terminal el código se pide sin mostrarlo; también puede pasarlo por una tubería. Existe
`--activate <codigo>`, pero deja la credencial en el historial y en los argumentos del proceso.
La CLI canjea, valida y guarda el token en `cloud.json`, como el token pegado; nunca imprime ni
guarda el código. La API pública de canje es `POST /nexo/authz/v1/activate`, cuerpo JSON
`{"code":"<codigo>"}`, respuesta `201` con `raw_token` y `token`. No envíe el código en la URL.

Un código solo sirve una vez y está ligado a la persona, nombre, alcance y vida solicitados.
Usado, vencido, revocado o de una persona deshabilitada responde `400` con
`activation_used|activation_expired|activation_revoked|activation_disabled`; desconocido,
`activation_invalid`. No nace ningún token en esos casos. Dos canjes simultáneos se serializan:
solo uno puede obtenerlo. Canje y emisión del token se confirman en una transacción.

Antes de consumir el código, el canje **revalida la política vigente**: el alcance y los días
pedidos al emitir deben seguir permitidos hoy (por ejemplo, si `NEXO_TOKEN_MAX_DAYS` bajó entre
la emisión y el canje). Si ya no lo son, responde `409 activation_policy_changed`, deja el evento
`nexo.activation.failed` y **no consume el código**: el administrador lo revoca y emite uno nuevo.
Si la petición llega autenticada como la persona que emitió el código (su token o su sesión del
panel), responde `403 activation_self_redeem`, audita el intento con su identidad y tampoco lo
consume. Ninguno de los dos casos suma al bloqueo por intentos fallidos.

El canje comparte el grupo de límite `login` (20 por minuto por IP por defecto) y el bloqueo de
`NEXO_AUTH_LOCKOUT`. En activación **cada fallo suma**, incluso con el mismo código; una IP
bloqueada recibe `429 auth_locked` con `Retry-After`. Los límites y bloqueos son por réplica,
como los controles existentes; detrás de un balanceador se necesita además un límite compartido.

**Pendientes y revocación:** en el detalle de usuario se muestran solo los metadatos de los
códigos vigentes pendientes y el botón **Revocar código**. Por API:

```sh
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/activations?principal=<usuario>"
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" \
  -d '{"principal":"<usuario>"}' "$NEXO_URL/nexo/authz/v1/activations/<id>/revoke"
```

Requieren `platform.tokens.manage`; para gestionar códigos de otro auditor, admin o superadmin
también `platform.admins.manage`. Una persona deshabilitada no recibe códigos nuevos.
Rehabilitarla permite canjear códigos pendientes aún vigentes: revoque también sus códigos si
la baja debe ser definitiva.

**Excepciones:** un administrador que emite para sí mismo conserva `raw_token`; las cuentas de
servicio conservan la entrega directa de una sola vez al responsable o administrador, porque un
bot no canjea códigos. Solo estas cuentas y solo un superadmin pueden pedir `expires_in_days: 0`.
El bootstrap del primer administrador, `recover-token` y `emergency-token` entregan directamente
el token a quien opera el servidor para sí mismo.

#### Problema conocido (riesgo aceptado): el administrador que emite el código lo conoce

**Qué pasa.** El administrador ve el código de activación al emitirlo y, mientras esté vigente,
podría canjearlo él mismo antes que la persona (o darlo a un tercero) y obtener un token a nombre
de ella. El cambio evita entregarle el token definitivo en la emisión, pero no que conozca el
secreto de entrega. Nexo no verifica por un segundo canal la identidad de quien canjea.

**Qué piden los estándares.** OWASP Forgot Password Cheat Sheet y ASVS 4.0.3 V2.5 (V6 en ASVS 5.0):
el secreto de recuperación o activación se entrega a la persona por un canal que ella controla.
NIST SP 800-63B: el secreto de activación se envía a la dirección registrada de la persona y quien
lo emite no lo conoce. ISO/IEC 27002:2022 5.17: entrega segura de la autenticación temporal y
confirmación de recepción.

**Qué se cumple y qué no.** Sí: código aleatorio de 256 bits, un solo uso, vencimiento, solo se
guarda su hash, intentos limitados y auditoría completa (IP y agente). No: que quien emite no
conozca el secreto.

**Mitigaciones actuales** (reducen el riesgo, no lo eliminan):

- Si la petición de canje llega autenticada como quien emitió el código (mismo token o misma
  sesión del panel), se rechaza (`403 activation_self_redeem`) y se audita. No detecta a un
  emisor que canje sin autenticarse o desde otra máquina.
- Alerta alta `activation_same_ip`: el código se canjeó desde la misma IP que lo emitió en menos
  de 1 minuto. Es solo un indicio (una oficina detrás de una misma IP, un proxy o una persona que
  comparte el equipo también la disparan); no es una prueba ni se debe usar como tal.
- Vencimiento de 24 horas por defecto (`NEXO_ACTIVATION_TTL`), código revocable y auditoría del
  canje con IP y agente (`nexo.activation.redeemed`), que permite comprobar a posteriori quién y
  desde dónde lo canjeó.

**Solución requerida antes de producción.** SSO corporativo (OIDC con el proveedor de identidad de
la empresa) para el alta y el acceso de las personas o, si no hay SSO, que el servidor envíe el
código al correo corporativo registrado de la persona sin que el administrador lo vea. Está en el
backlog como [SG-17](../mejoras-y-referencias.md) (prioridad A, requisito antes de producción);
comparte proveedor con SG-02.

### 3.4 Ver los tokens

```sh
curl -fsS -H "$H" "$NEXO_URL/nexo/authz/v1/tokens?principal=<usuario>"   # sin ?principal: los suyos
curl -fsS -H "$H" "$NEXO_URL/admin/users/<id>/tokens"
```

Cada token muestra caducidad, alcance y último uso. `expires_at: null` significa sin caducidad:
son tokens emitidos antes de esta versión y deben rotarse.

### 3.5 Revocar un token

En el panel: detalle de la persona, botón "Revocar" del token. Por API:

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
2. Emita un código nuevo con el mismo nombre de máquina para la persona (3.3).
3. La persona lo canjea e instala (`engram nexo connect --server https://<dominio> --activate-stdin`).
4. Revoque el token anterior cuando el nuevo aparezca con `last_used_at`.

**Tokens de superadmin, admin y auditor.** Son los que no pueden perderse: si vencen todos los de los
superadmins, nadie puede emitir tokens por la API. Para ellos el servidor deja la alerta
`privileged_token_expiring` a los 30 días (advertencia) y a los 7 días (alta), una por token y por aviso. El
procedimiento sin corte (emitir el nuevo, probarlo y revocar el viejo con el nuevo) está en
`servidor-produccion.md`, 7.4. Un superadmin puede emitirse su propio token con el vigente; un admin o un
auditor necesita que un superadmin se lo emita. **Los dos superadmins no renuevan el mismo día.** Si ningún
superadmin conserva un token vigente, quien opera el servidor usa el acceso de emergencia (7.5 del mismo
manual).

### 3.7 Deshabilitar y habilitar personas

No se borran personas: se deshabilitan. Todos sus tokens dejan de funcionar en la siguiente
petición y su historial se conserva.

```sh
curl -fsS -X POST -H "$H" "$NEXO_URL/admin/users/<id>/disable"
curl -fsS -X POST -H "$H" "$NEXO_URL/admin/users/<id>/enable"
```

En el panel: botones "Deshabilitar usuario" y "Habilitar usuario" del detalle. Deshabilitar a un admin, superadmin
o auditor solo lo puede hacer un superadmin; el último superadmin no se deshabilita (`409`). Si la
persona era el único responsable de un espacio, ese espacio queda sin responsable: un superadmin lo
recupera (4.4).

### 3.8 Cuentas de servicio (CI y bots)

Una **cuenta de servicio** es una identidad para un pipeline o un bot: publica en los espacios con su
propio token, sin usar el de una persona. La auditoría muestra la cuenta (por ejemplo `ci-wiki`)
como autora de cada publicación.

> **Apagadas por defecto en la beta (`NEXO_SERVICE_ACCOUNTS_ENABLED=false`).** No hay automatizaciones
> previstas y, mientras el administrador pueda emitir el token de una cuenta con roles de contenido,
> existe el riesgo R2 (docs/nexo/mejoras-y-referencias.md, SG-17). Con la variable apagada (valor por
> defecto), crear una cuenta, habilitarla de nuevo, emitir su token o rotarlo responde `403` con el
> código `service_accounts_disabled` (API, panel y `engram nexo sa ...`), queda en la auditoría
> (`nexo.service_accounts.denied`) y no crea nada; los tokens de cuentas de servicio que ya existían
> **no autentican** y el servidor lo avisa al arrancar (cuántas cuentas habilitadas hay, sin nombres).
> Siguen permitidos consultar (listar y ver), revocar tokens y deshabilitar. Para usarlas, quien
> administra el servidor define `NEXO_SERVICE_ACCOUNTS_ENABLED=true` (también `1`) y reinicia; cualquier
> otro valor impide arrancar. Con `true` rige el comportamiento descrito abajo, con el riesgo R2: el
> administrador puede emitir tokens de cuentas con roles de contenido. La solución futura son
> credenciales de cliente OAuth o federación de identidad de carga (propuesta de autenticación, §4.3).

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
      NEXO_VERSION: 3.4.0-nexo.2          # versión del fork con engram nexo kb upload
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
    value: 3.4.0-nexo.2
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

#### Desde el panel

Abre el espacio en **Conocimiento** y entra a la sección **Miembros**
(`/dashboard/kb/spaces/<espacio>/members`). La lista agrupa a las personas y cuentas de servicio por
rol (responsables, publicadores, proponentes y lectores), con el tipo ("Persona" o "Cuenta de
servicio") y desde cuándo tienen el rol.

| Quién abre la página | Qué ve |
|---|---|
| Responsable del espacio | La lista con los controles: selector de rol y botón "Cambiar rol", botón "Quitar" y el formulario "Agregar un miembro" |
| Cualquier persona con acceso al espacio (lector, proponente, publicador) | Solo la lista, con la nota "Solo el responsable del espacio y los administradores cambian roles" |
| Admin o auditor sin rol en un espacio restringido | Solo la lista (permiso de directorio); sin controles |
| Persona sin acceso a un espacio restringido | Nada: la página responde 404, como el resto del espacio |

- **Agregar:** escriba el nombre de usuario (o el nombre corto de una cuenta de servicio), elija el rol
  y pulse "Agregar miembro". Si la persona ya tenía un rol, se cambia por el nuevo. El administrador
  y el auditor ven sugerencias de nombres; el responsable escribe el nombre.
- **Cambiar el rol:** elija el rol en la fila y pulse "Cambiar rol".
- **Quitar:** botón "Quitar" de la fila. Es reversible: se vuelve a agregar con el rol que corresponda.
- La página muestra los rechazos de las reglas con un texto que dice qué hacer: una cuenta de servicio
  no puede ser responsable, el último responsable no se puede quitar ni bajar, nadie se sube el rol a
  sí mismo y el usuario escrito no existe.
- Las sesiones abiertas con un token de solo lectura ven la lista pero no pueden cambiar nada.
- Cada cambio queda en los eventos de roles (`GET /nexo/admin/authz-events`), igual que por la API.

#### Con la API

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

**Verlo en el panel (superadmin).** La pestaña **Estado de la búsqueda** (`/dashboard/kb/search-status`)
muestra lo mismo sin usar la terminal, en solo lectura: el modo por defecto (texto o texto y significado),
el proveedor, el modelo y las dimensiones, si la clave está configurada (nunca la clave) y de qué variable
sale, si pgvector está disponible y su versión, las versiones del índice (activa, en construcción y
retiradas, con sus fechas), el avance (secciones con vector sobre el total, pendientes, cambios sin leer),
las secciones que no se pudieron vectorizar con sus últimos errores y el comando para reencolarlas, las
últimas evaluaciones (Hit@5, MRR, sin respuesta) y los pesos y umbrales en uso. Pide el mismo rol que la
API (`platform.settings`): un administrador sin el rol de superadmin ve un 403 que lo explica. La frase de
arriba resume el estado: "al día", "con pendientes", "construyéndose" o "solo texto" con el motivo.
El bloque "Reordenamiento (R3)" dice "No configurado" hasta que ese reordenamiento exista.

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
asistente, la API o, desde K6, el panel.

### 6.1 Revisar propuestas desde el panel

- **Dónde:** la pestaña **Propuestas** del menú (`/dashboard/kb/proposals`) lista las propuestas de
  todos sus espacios; la sección **Propuestas** de cada espacio
  (`/dashboard/kb/spaces/<espacio>/proposals`) lista las de ese espacio. Los filtros son Pendientes,
  Aprobadas, Descartadas y Retiradas. Cada fila dice el espacio, si cambia un documento (y desde qué
  versión) o es un documento nuevo, quién propuso y cuándo.
- **Quién la ve:** quien tiene rol publicador o responsable en el espacio (`space.review`) ve todas las
  propuestas del espacio; un proponente solo ve las suyas. Si usted revisa algún espacio, la pestaña
  **Propuestas** muestra un número con las propuestas abiertas que puede revisar.
- **Detalle:** muestra el motivo del autor, la vigencia propuesta y, lado a lado, la versión vigente del
  documento y el texto propuesto (Markdown seguro: nunca se interpreta HTML). Si la propuesta es de un
  documento nuevo, solo muestra el texto propuesto. Si el documento cambió después de la propuesta, un
  aviso lo dice.
- **Aprobar y publicar:** nota para el autor (opcional), fechas de vigencia (opcionales; vacías, vale la
  propuesta o la fecha de hoy) y, si la propuesta es de un documento nuevo, la clave del documento
  (opcional). Si el documento cambió, aparece la casilla "Aprobar aunque el documento cambió": márquela
  solo si el cambio sigue siendo válido. Es lo mismo que `:approve` con `force`.
- **Descartar:** el motivo es obligatorio y lo ve el autor. La propuesta se conserva.
- Una propuesta ya cerrada muestra quién la cerró, cuándo y la nota, sin botones.
- Solo decide quien tiene rol publicador o responsable en el espacio y una sesión con token de
  escritura. Las sesiones con token de solo lectura no ven propuestas ajenas ni pueden decidir.
- Las decisiones usan los mismos eventos de auditoría que la API (`proposal.approved`,
  `proposal.discarded` en `GET /nexo/kb/v1/spaces/<espacio>/events`).

### 6.2 Con la API

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
| Dar permiso | Panel: detalle de la persona, campo "Nombre del proyecto" y botón "Dar permiso". API: `POST /admin/users/<id>/grants` con `{"project":"<proyecto>"}` |
| Quitar permiso | Panel: botón "Revocar" del permiso. API: `POST /admin/users/<id>/grants/<proyecto>/revoke` |
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

### 9.1.1 Auditoría de metadatos y regla R-6

`platform.audit.read` permite conocer quién, acción, entidad, identificador, proyecto, fecha
y referencia del token. **No concede acceso al contenido ni al título de una memoria o un
documento**, tampoco a admins o superadmins. El historial decide `project.read` con el motor
de Nexo **para cada fila**, incluso cuando la misma clave tiene versiones en varios proyectos.
Un rol de proyecto heredado por el motor (permiso de Engram o rol implícito de un proyecto abierto)
también cuenta; un rol de plataforma por sí solo nunca cuenta.

`GET /nexo/admin/history/<clave>?include_payload=true` no falla por falta de lectura:
omite `payload` y `title` de esa fila y marca `payload_withheld: true`. Con lectura devuelve
el título y, si se pidió, el payload. Sin `include_payload`, no devuelve payload. El panel de
Historial siempre omite el payload y solo muestra títulos de proyectos autorizados.

Los detalles de eventos de seguridad y roles, las alertas y la evidencia de conocimiento aplican
la misma separación: `space.read` para contenido del espacio; los campos libres desconocidos se
retienen sin lectura. Las corridas de publicación ocultan rechazos y textos libres sin lectura.
Una evaluación de varios espacios oculta resultados, nombre del conjunto, configuración libre,
clases y notas si falta lectura en cualquiera. Estado de la búsqueda aplica la misma proyección
a evaluaciones y exige lectura del espacio de cada error libre del proveedor. La verificación de integridad y la revisión/exportación de accesos contienen
metadatos, sin memorias ni documentos.

Las exportaciones diarias y manuales por CLI pasan a `nexo-audit-export/v2`: contienen una
proyección de metadatos de la fila, con `payload_withheld`, y conservan posiciones, digest y
hashes originales de la cadena. La firma HMAC protege el archivo proyectado; `verify-export`
sigue leyendo archivos v1 y v2. Los archivos v1 ya emitidos no se modifican: proteja su acceso
y conservación, porque podrían contener títulos o textos. El webhook transmite regla, severidad,
capa e identificador del sujeto, sin el resumen libre ni los detalles.

**Qué ve quien audita sin leer el contenido (proyección por esquema).** Cada tipo de registro declara los
campos que salen: números, identificadores (ids, claves de documento, nombres de proyecto y de espacio),
fechas, enumeraciones y etiquetas administrativas (usuario, nombre de token o de cuenta de servicio, motivo de
una revocación, User-Agent y declaración del cliente). Todo lo demás se omite, **objetos desconocidos incluidos**
(por ejemplo `settings` de un espacio, nombres, descripciones, notas, comandos, `commit`, retriever y modo no
validados) y el registro lleva `payload_withheld: true`. Las credenciales (`egc_`, `nxa_`, `Bearer ...`,
`postgres://`) nunca salen, ni a quien lee el contenido: se escriben rechazadas (`secret_detected` o 400) y las
filas históricas se entregan con `[credencial oculta]`. Una columna nueva no sale a un auditor ni a una
exportación hasta que se declara en su esquema (`internal/nexo/security/nexo_projection*.go`). Riesgo abierto:
la clave de un documento (`doc_key`) es un identificador definido por quien publica y los eventos y alertas de
conocimiento la conservan; no la ponga en una clave si el nombre es sensible.

**Texto que viene de afuera (credenciales).** El User-Agent, la declaración del cliente
(`client_claim`), los nombres de token y de actor, las notas, el proyecto y las claves de documento
pueden traer, por error o a propósito, un token (`egc_...`), un código de activación (`nxa_...`),
una cabecera `Bearer`/`Authorization: ...` o una cadena de conexión. Nexo quita esos formatos
**antes de guardar** en `cloud_auth_audit_log`, `nexo_authz_events`, `nexo_kb_events`,
`nexo_activity_log`, `nexo_actor_presence`, `nexo_alerts`, `nexo_alert_acks` y
`nexo_access_reviews` (se conserva el resto del texto) y **otra vez al leer, renderizar o
exportar**, para las filas históricas: API de auditoría, actividad, presencia, historial, eventos,
alertas y revisión de accesos (JSON y CSV), panel, exportación diaria y por CLI (v2) y webhook.
Los nombres de token con credenciales o de más de 256 caracteres se rechazan en todas las rutas de
creación (`400`), incluidas la emisión y la rotación de cuentas de servicio. Una clave de documento
(`doc_key`), ruta, origen, nota de cambio, responsable o etiqueta con ese formato se rechaza al
publicar (`secret`) y el rechazo no repite el valor.

**Notas libres.** La nota con que se reconoce una alerta se entrega solo a quien lee el espacio o
proyecto de la alerta (como su resumen); sin lectura la respuesta lleva `note_withheld: true`. Las
notas de la revisión de accesos no tienen un ámbito contra el cual verificar quién puede leerlas:
`last_review`, `recent_reviews`, la respuesta de `access-review:complete`, el panel y las
exportaciones no las incluyen y marcan `note_withheld: true`. La nota se guarda (sin credenciales)
para quien tenga acceso a la base.

La activación registra `nexo.activation.issued|redeemed|failed|revoked`, con el identificador del
código, persona, IP y agente del canje. Nunca registra código ni token. La alerta alta
`activation_repeated` se crea en la misma transacción de un fallo cuando se reutiliza un código
usado, o se acumulan dos fallos de un código conocido; admite configuración mediante
`NEXO_ALERT_RULES`. Los códigos desconocidos dejan fallos y bloqueo por IP. La alerta alta
`activation_same_ip` (heurística, solo un indicio) se crea cuando un código se canjea desde la
misma IP que lo emitió en menos de un minuto; ver el problema conocido de 3.3. Los rechazos
`activation_self_redeem` (canje por el emisor) y `activation_policy_changed` (la política cambió
tras emitir) quedan en `nexo.activation.failed` con su motivo y no consumen el código.

### 9.2 Alertas

```sh
curl -fsS -H "$H" "$NEXO_URL/nexo/admin/alerts?unacked=true&severity=high"
curl -fsS -X POST -H "$H" -H "Content-Type: application/json" -d '{"note":"revisado con la persona"}' \
  "$NEXO_URL/nexo/admin/alerts/<id>:ack"
```

Reconocer una alerta exige un token `write` (también para el auditor) y solo se puede hacer una vez.

Alertas de continuidad del acceso de administración (plataforma):

| Regla | Cuándo | Severidad | Qué hacer |
|---|---|---|---|
| `single_superadmin` | Queda un solo superadmin habilitado (se repite cada semana) | alta | Crear el segundo superadmin (`servidor-produccion.md`, 7.3) |
| `privileged_token_expiring` | Un token activo de un superadmin, admin o auditor habilitado vence en 30 o en 7 días | advertencia a 30, alta a 7 | Renovarlo sin corte (7.4); no renovar los dos superadmins el mismo día |
| `emergency_access_used` | Se emitió un token de emergencia con `engram cloud bootstrap emergency-token` | alta | Revisar quién y por qué (auditoría `nexo.emergency_token`), revocar el token de emergencia y reconocer la alerta con una nota (7.5) |

### 9.3 Revisión de accesos

La revisión muestra, por persona: rol de plataforma (con fecha y quién lo asignó), roles en
proyectos y espacios, permisos de proyecto, tokens (caducidad, alcance, último uso), días sin
actividad y marcas `inactive`, `token_no_expiry`, `token_never_used`, `single_superadmin` y
`token_expiring`. El resumen incluye personas habilitadas, inactivas, superadmins habilitados
(`superadmins_enabled`), tokens activos, tokens sin caducidad, tokens nunca usados, tokens que vencen
en 30 días y, de ellos, los de superadmins, admins y auditores (`privileged_tokens_expiring`).

```sh
# Generar el reporte (umbral de inactividad: 90 días por defecto; de 1 a 3650)
curl -fsS -H "$H" "$NEXO_URL/nexo/admin/access-review?inactive_days=90" > revision-$(date +%F).json
# Para guardarlo como evidencia (CSV o JSON con huella SHA-256, ver 9.5): -OJ usa el nombre que propone el servidor
curl -fsS -OJ -H "$H" "$NEXO_URL/nexo/admin/access-review?inactive_days=90&format=csv"
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
| `role_drift` | La columna de rol de Engram no coincide con el rol de plataforma de Nexo (alguien evitó los triggers de la base o se restauró una copia vieja). La columna no concede nada: corrija con `engram nexo authz reconcile-roles --apply` (2.4). También deja la alerta `platform_role_drift` |
| `token_no_expiry` | Token anterior a esta versión: emita uno con caducidad y revoque el viejo |
| `token_never_used` | Token entregado que nunca se usó: confirme y revoque si sobra |
| `single_superadmin` | Es el único superadmin habilitado: cree el segundo (`servidor-produccion.md`, 7.3). También deja la alerta `single_superadmin` |
| `token_expiring` | Un superadmin, admin o auditor tiene un token que vence en menos de 30 días (en los tokens, "Token por vencer"): renuévelo sin corte (`servidor-produccion.md`, 7.4) |
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

### 9.5 Panel del auditor

Todo lo de 9.1 a 9.3 también se hace desde el panel, sin terminal. Entre con el token que obtuvo al canjear el código que le dio el
superadmin (el auditor puede entrar con un token `read` para mirar; reconocer alertas y registrar la
revisión piden un token `write`). El menú muestra la pestaña **Auditoría** a quien tiene
`platform.audit.read` (auditor, admin y superadmin), en todas las páginas del panel, y solo a esas
personas; cualquier otra persona no la ve y, si abre la ruta, recibe 403. El acceso denegado queda en
los eventos de roles (`authz.denied`).

La pestaña sale según el rol de Nexo, no según el rol de Engram: un administrador de Engram con
usuario administrado tiene el rol de plataforma `admin` y la ve; un administrador con el token
heredado `ENGRAM_CLOUD_ADMIN` no tiene roles en Nexo, no la ve y las rutas le dan 403. La pestaña es
solo un atajo: el servidor vuelve a comprobar el permiso en cada pantalla. Si se quita o se da el rol,
la pestaña puede tardar hasta 30 segundos en cambiar (el acceso a las pantallas cambia al instante).
Si el servidor no logra consultar los roles, la página carga igual y la pestaña no aparece.

| Pantalla | Ruta | Para qué sirve |
|---|---|---|
| Registro | `/dashboard/admin/audit` | Eventos de seguridad (logins, rechazos, tokens, permisos) o, con "Roles y permisos", los cambios de roles. Filtros: desde, hasta, actor, acción (exacta o con `*`), resultado y ámbito (proyecto; en roles, tipo y clave del ámbito). Páginas de 50 con "Ver más antiguos" |
| Actividad | `/dashboard/admin/audit/activity` | Quién se conectó (última conexión, IP y cliente) y qué sincronizó (operación, proyecto, elementos) |
| Historial | `/dashboard/admin/audit/history?key=` | Versiones de una memoria: quién la cambió y cuándo. Se llega desde "Ver historial" en el registro o escribiendo la clave. No muestra el contenido |
| Integridad | `/dashboard/admin/audit/integrity` | Botón "Verificar ahora": recorre la cadena de hashes y dice "La cadena está íntegra" o muestra la fila alterada y los huecos |
| Alertas | `/dashboard/admin/audit/alerts` | Pendientes y todas (incluida `service_account_stale`), con "Reconocer" y nota opcional |
| Revisión de accesos | `/dashboard/admin/audit/access-review` | Roles, tokens y marcas por persona, última revisión (fecha, autor y huella), "Descargar CSV" y "Descargar JSON" como evidencia y "Marcar revisión como hecha" |

Cada pantalla usa las mismas funciones que la API y deja las mismas acciones de auditoría
(`nexo.audit.read`, `nexo.alerts.read`, `nexo.alert.ack`, `nexo.access_review.complete`,
`nexo.access_review.export`, ...):
consultar el panel también queda registrado. El detalle de un evento muestra solo lo que la API ya
expone y omite cualquier valor con forma de token, hash o cabecera `Authorization`. La hora va en UTC.

#### Procedimiento de la revisión trimestral de accesos

1. Entre al panel con un token `write` y abra **Auditoría > Revisión de accesos**.
2. Compare el resumen con el de la revisión anterior (arriba aparece la fecha y el autor de la
   última). Ajuste "días sin uso" si el criterio del trimestre es otro y pulse "Recalcular".
3. Pulse "Solo con marcas" y recorra cada fila con la tabla de 9.3: `Inactiva`, `Sin uso`
   (cuenta de servicio), `Token sin caducidad` y `Token nunca usado`. Las acciones (deshabilitar,
   revocar, ajustar roles) se hacen en sus pantallas o con la API; esta pantalla no cambia accesos.
4. Revise "Rol de plataforma" y "Roles por proyecto y espacio": quien ya no corresponde, se ajusta.
5. Después de hacer los ajustes, abra de nuevo la pantalla (sin "Solo con marcas", para que el
   archivo incluya a todas las personas), pulse **Descargar CSV** (y **Descargar JSON** si el
   expediente lo pide) y **guarde el archivo** con el expediente de la revisión. El nombre del
   archivo lleva la huella SHA-256 (ver "Exportar la revisión y comprobar la huella").
6. Escriba en las notas qué encontró y qué hizo (por ejemplo "3 tokens rotados, 2 personas
   deshabilitadas, 1 cuenta de servicio sin uso confirmada con su responsable") y pulse **Marcar
   revisión como hecha**. Se guarda quién, cuándo, las notas y la huella SHA-256 del contenido que vio
   (personas, roles y tokens); la pantalla muestra "Revisión registrada" y la huella en "Última
   revisión registrada". Anótela en el expediente junto al archivo.
7. Cierre además las alertas pendientes de la pestaña Alertas y compruebe la pestaña Integridad.

#### Exportar la revisión y comprobar la huella

La exportación es evidencia para ISO/IEC 27001:2022 A.5.18 (derechos de acceso). Se baja desde los
botones de la pantalla o con `GET /nexo/admin/access-review?format=csv|json` (la respuesta sin
`format` no cambia). Es una lectura: la pueden hacer también las sesiones y los tokens de solo lectura;
quien no tiene `platform.audit.read` recibe 403. Opciones de la URL: `inactive_days` (1 a 3650, por
defecto 90) y `only=flags` (solo personas con marcas; el resumen sigue siendo el de todos). Los
archivos nunca llevan el valor de un token ni su hash: solo el id, el nombre, el alcance y las
fechas, como la pantalla.

Cada descarga queda en la auditoría de seguridad como `nexo.access_review.export`, con el formato,
el umbral, el filtro, el número de personas y la huella. Si no se puede registrar, no se entrega el
archivo.

| | CSV | JSON |
|---|---|---|
| Nombre | `nexo-revision-accesos-AAAAMMDDTHHMMSSZ-<huella>.csv` | `nexo-revision-accesos-AAAAMMDDTHHMMSSZ-<huella>.json` |
| Huella | SHA-256 de los bytes del archivo, en el nombre y en la cabecera `X-Nexo-Content-SHA256` | Campo `content_sha256` del documento (y en el nombre y la cabecera) |
| Qué cubre | El archivo completo, tal como se descargó | El documento sin el campo `content_sha256`, en forma canónica |

**CSV.** UTF-8 con BOM, comas, saltos de línea CRLF y una fila de títulos: Excel lo abre con doble
clic (si su Excel espera `;`, use Datos > Desde texto o CSV y elija la coma). Una sola hoja con una
fila por cada rol de plataforma (siempre una por persona, aunque sea `miembro`), por cada rol en un
proyecto o espacio, por cada permiso de proyecto de Engram y por cada token. La primera columna
(`tipo`) dice cuál: `rol`, `permiso_engram` o `token`; las demás repiten los datos de la persona para
poder filtrar y ordenar. Columnas: `tipo`, `persona_id`, `usuario`, `nombre`, `correo`, `clase`,
`habilitada`, `rol_engram`, `rol_plataforma`, `ultima_actividad`, `dias_sin_uso`, `marcas` (códigos
separados por espacio: `inactive`, `stale`, `disabled`, `role_drift`, `token_no_expiry`, `token_never_used`),
`ambito_tipo`, `ambito`, `rol`, `desde`, `otorgado_por` (filas `rol` y `permiso_engram`),
`token_id`, `token_nombre`, `token_alcance`, `token_creado`, `token_creado_por`, `token_vence`,
`token_sin_vencimiento`, `token_vencido`, `token_ultimo_uso` (filas `token`) y, al final, los datos de
la exportación: `generado_en` (UTC), `generado_por`, `umbral_inactividad_dias` y `solo_con_marcas`.
Las fechas van en UTC (`AAAA-MM-DDTHH:MM:SSZ`). Si el texto de una celda empieza con `=`, `+`, `-`,
`@`, tabulador o retorno, el archivo antepone una comilla simple para que la hoja no lo ejecute como
fórmula (inyección de fórmulas, OWASP).

**JSON.** Un documento con las claves ordenadas: `report` (`access_review`), `control`,
`format_version`, `generated_at` (UTC), `generated_by` (`principal_id` y `name`), `filters`
(`inactive_days`, `only_flagged`), `policy` (`disable_inactive_days`, `service_account_stale_days`),
`review_digest`, `summary`, `people` (cada persona con `scope_roles`, `engram_project_grants` y
`tokens`) y `content_sha256`. `review_digest` es la misma huella que "Marcar revisión como hecha"
guarda en `report_sha256`: si no cambió ningún acceso entre la descarga y el registro (incluido el
último uso de un token), las dos coinciden y enlazan el archivo con la revisión. Si difieren, el
archivo igual vale como evidencia del momento en que se generó; su huella queda además en la
auditoría (`nexo.access_review.export`).

**Cómo comprobar la huella.** Sirve para demostrar, meses después, que el archivo del expediente es el
que descargó el servidor y no se editó.

- Con el binario de Nexo (sirve para CSV y JSON, no usa la base y no hace falta la sesión del panel):
  `engram nexo audit verify-review nexo-revision-accesos-AAAAMMDDTHHMMSSZ-<huella>.csv` (o `.json`).
  Responde `"ok": true` si coincide, o `"ok": false` con la huella declarada y la calculada, y sale con
  código 1. En un CSV la huella declarada es la del nombre del archivo: si se renombró, comprúebelo a
  mano como se explica abajo.
- A mano, CSV: calcule el SHA-256 del archivo y compárelo con el que lleva el nombre.
  `sha256sum nexo-revision-accesos-*.csv` (Linux o macOS) o
  `Get-FileHash -Algorithm SHA256 .\nexo-revision-accesos-*.csv` (PowerShell). Si cambia una coma,
  la huella cambia.
- A mano, JSON: quite `content_sha256`, escriba el resto en forma canónica (claves ordenadas en todos los
  niveles, sin espacios ni salto de línea final, UTF-8 sin escapar `<`, `>` ni `&`) y calcule su
  SHA-256; debe ser igual a `content_sha256`. Con Node.js:

  ```sh
  node -e "const fs=require('fs'),c=require('crypto');const d=JSON.parse(fs.readFileSync(process.argv[1],'utf8'));const h=d.content_sha256;delete d.content_sha256;const s=o=>Array.isArray(o)?'['+o.map(s).join(',')+']':o&&typeof o==='object'?'{'+Object.keys(o).sort().map(k=>JSON.stringify(k)+':'+s(o[k])).join(',')+'}':JSON.stringify(o);console.log(c.createHash('sha256').update(s(d)).digest('hex')===h?'huella correcta':'EL ARCHIVO CAMBIO')" revision.json
  ```

  Con `jq`: `jq -S -c 'del(.content_sha256)' revision.json | tr -d '\n' | sha256sum`. Con Python:
  `json.dumps(d, sort_keys=True, separators=(',', ':'), ensure_ascii=False)` sobre el documento sin
  `content_sha256`, codificado en UTF-8. Los caracteres de control de los textos (saltos de línea, por
  ejemplo) se cambian por un espacio al exportar, para que todas las herramientas escriban lo mismo.

La huella demuestra que el archivo no cambió desde que el servidor lo entregó; no demuestra quién lo
guardó. Para eso sirve el registro de auditoría (`nexo.access_review.export` con la misma huella) y
guardar el archivo en un lugar de solo lectura o con control de cambios.

## 10. Tareas periódicas recomendadas

| Frecuencia | Tarea | Quién | Cómo |
|---|---|---|---|
| Diaria (o al recibir el webhook) | Revisar alertas no reconocidas y reconocerlas con una nota | Admin o auditor | 9.2 |
| Semanal | Revisar rechazos de autenticación y denegaciones repetidas (PCs con tokens viejos o mal pegados) | Admin | `GET /nexo/admin/audit?outcome=denied&from=...`, `GET /nexo/authz/v1/events?action=authz.denied` |
| Semanal | Revisar propuestas abiertas de cada espacio | Publicadores y responsables | 6 |
| Mensual | Renovar tokens que vencen en los próximos 30 días | Admin | 3.6 y `tokens_expiring_30d` |
| Al recibir el aviso `privileged_token_expiring` (30 días antes) | Renovar el token de cada superadmin, admin o auditor sin corte; los dos superadmins en días distintos | Superadmin | 3.6 y `servidor-produccion.md`, 7.4 |
| Mensual | Revisar avisos de vencimiento y revisión de documentos y actualizar o retirar | Responsables | 7, 5.9 |
| Mensual | Confirmar que la cadena de auditoría verifica y que la exportación firmada se copió fuera del host | Admin (con quien opera el servidor) | `GET /nexo/admin/audit/verify`; `servidor-produccion.md` §11.5 |
| Trimestral | Revisión de accesos registrada, con el CSV o JSON descargado y guardado en el expediente | Admin y auditor | 9.3 (API) o 9.5 (panel, con la huella) |
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
| Nadie puede emitir tokens: todos los tokens de los superadmins vencieron o se revocaron | No se renovó a tiempo | Quien opera el servidor ejecuta `engram cloud bootstrap emergency-token --user <superadmin>` dentro del servidor (`servidor-produccion.md`, 7.5) |
| Alerta `single_superadmin` | Queda un solo superadmin habilitado | Crear el segundo superadmin (`servidor-produccion.md`, 7.3) |
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
| `GET /dashboard/kb/spaces/<espacio>/members` (panel) | `space.read` o `platform.directory.read` |
| `POST /dashboard/kb/spaces/<espacio>/members` y `/members/remove` (panel, con CSRF) | `space.members.manage` |
| `GET /dashboard/kb/proposals` y `/dashboard/kb/spaces/<espacio>/proposals[/<id>]` (panel) | propias, o `space.review` |
| `POST /dashboard/kb/spaces/<espacio>/proposals/<id>/approve` y `/discard` (panel, con CSRF) | `space.review` |
| `GET /nexo/kb/v1/changes?since=` | `space.read` |
| `GET /nexo/kb/v1/spaces/<espacio>/events` y `/publish-runs` | `space.audit.read` o `platform.audit.read` |
| `GET /nexo/admin/audit`, `/activity`, `/presence`, `/history/<sync_id>` | `platform.audit.read` |
| `GET /nexo/authz/v1/events`, `GET /nexo/admin/authz-events` | `platform.audit.read` |
| `GET /nexo/admin/audit/verify`, `GET /nexo/admin/alerts`, `GET /nexo/admin/access-review` | `platform.audit.read` |
| `POST /nexo/admin/alerts/<id>:ack`, `POST /nexo/admin/access-review:complete` | `platform.audit.read` y token `write` |
| `GET /dashboard/admin/audit`, `/activity`, `/history`, `/integrity`, `/alerts`, `/access-review` (panel) | `platform.audit.read` |
| `POST /dashboard/admin/audit/alerts/<id>/ack`, `POST /dashboard/admin/audit/access-review/complete` (panel) | `platform.audit.read` y token `write` |
| `GET /nexo/kb/v1/admin/index`, `POST /nexo/kb/v1/admin/index:reindex` | `platform.settings` |
| `GET /nexo/admin/database` | `platform.settings`: uso del pool de conexiones, TLS hacia la base, versión de Postgres, `max_connections` y versión de pgvector (`servidor-produccion.md`, 4.1.1) |

## Anexo B. Puntos por verificar

| Tema | Motivo |
|---|---|
| Cuentas de servicio y versión del fork | Las cuentas de servicio (3.8) existen desde `3.4.0-nexo.2`. Con un servidor `3.4.0-nexo.1`, use un usuario dedicado (por ejemplo `ci-lineamientos`) con rol publicador y token con caducidad. Los ejemplos de CI solo necesitan `engram nexo kb upload`, que ya existe en `3.4.0-nexo.1` |
| `ENGRAM_CLOUD_ALLOWED_PROJECTS` y personas con token asignado | La autorización por proyecto de una persona consulta solo sus permisos; no se confirmó si la lista del servidor también la limita |
| Avisos de conocimiento para responsables | La ruta de alertas exige `platform.audit.read`; no hay una vista de avisos para el responsable del espacio |
