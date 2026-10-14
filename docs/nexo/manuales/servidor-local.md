# Levantar un servidor Nexo en tu PC (desarrollo, demo o pruebas)

**Para quién:** quien necesita un servidor Nexo propio para desarrollar, hacer una demostración o
probar documentos antes de publicarlos en el servidor del equipo.
**Versión:** `3.4.0-nexo.2` (imagen `ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2`).
**Tiempo:** unos 20 minutos.

Este servidor escucha **solo en tu PC** (`127.0.0.1`), por HTTP y sin certificado. No lo uses para
datos reales ni lo expongas a la red. Para un servidor del equipo, con HTTPS, lee
[servidor-produccion.md](servidor-produccion.md).

## Contenido

1. [Qué vas a levantar](#1-qué-vas-a-levantar)
2. [Requisitos](#2-requisitos)
3. [Preparar la carpeta](#3-preparar-la-carpeta)
4. [Obtener la imagen del servidor](#4-obtener-la-imagen-del-servidor)
5. [El compose mínimo](#5-el-compose-mínimo)
6. [Generar los secretos (.env)](#6-generar-los-secretos-env)
7. [Arrancar y comprobar](#7-arrancar-y-comprobar)
8. [Crear el primer administrador](#8-crear-el-primer-administrador)
9. [Crear personas y tokens](#9-crear-personas-y-tokens)
10. [Entrar al panel](#10-entrar-al-panel)
11. [Crear un espacio y nombrar responsable](#11-crear-un-espacio-y-nombrar-responsable)
12. [Subir documentos](#12-subir-documentos)
13. [Conectar tu asistente al servidor local](#13-conectar-tu-asistente-al-servidor-local)
14. [Ver la base con DBeaver](#14-ver-la-base-con-dbeaver)
15. [Detener, borrar y empezar de cero](#15-detener-borrar-y-empezar-de-cero)
16. [Variables de entorno útiles para desarrollo](#16-variables-de-entorno-útiles-para-desarrollo)
17. [Problemas frecuentes](#17-problemas-frecuentes)

---

## 1. Qué vas a levantar

Dos contenedores con Docker Compose:

| Servicio | Imagen | Qué hace | Dónde escucha en tu PC |
|---|---|---|---|
| `postgres` | `pgvector/pgvector:pg18` | Base de datos `engram_cloud` con dos cuentas: `nexo_owner` (dueña, solo migra al arrancar) y `nexo_app` (la que usa el servidor) | `127.0.0.1:54330` (para DBeaver) |
| `cloud` | `ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2` | Servidor Nexo (`engram cloud serve`): API, panel web y espacios de conocimiento | `127.0.0.1:18095` |

Es el mismo esquema con el que se probó el modo local (06-pruebas/conocimiento del repositorio de
Nexo), sin el proxy HTTPS del despliegue de producción.

## 2. Requisitos

| Requisito | Detalle |
|---|---|
| Docker | Windows y macOS: Docker Desktop. Linux: Docker Engine con el complemento `docker compose`. Comprueba con `docker compose version` |
| Puertos libres | `18095` (servidor) y `54330` (Postgres) en `127.0.0.1`. Puedes cambiarlos en el compose |
| Acceso al repositorio privado `Adriel2503/engram-nexo` | Para descargar la imagen de GHCR o el código, y el script de cuentas de Postgres |
| Binario `engram` del fork en tu PC | Para crear documentos desde la terminal y conectar tu asistente. Instalación: [instalacion-dev.md](instalacion-dev.md) §5 |

**Windows: puertos reservados.** Windows (Hyper-V, WSL) reserva rangos de puertos que Docker no puede
publicar; en esta PC de referencia, por ejemplo, `55433-55632`. Antes de elegir un puerto, mira los
rangos reservados de tu equipo:

```powershell
netsh interface ipv4 show excludedportrange protocol=tcp
```

Si `18095` o `54330` caen dentro de un rango de la lista, usa otros en el compose.

Para ver si un puerto ya está en uso:

```powershell
Get-NetTCPConnection -LocalPort 18095,54330 -ErrorAction SilentlyContinue    # Windows: sin salida = libres
```

```bash
lsof -iTCP:18095 -iTCP:54330 -sTCP:LISTEN                                  # macOS y Linux: sin salida = libres
```

## 3. Preparar la carpeta

Crea una carpeta de trabajo, por ejemplo `nexo-local`, **fuera** de cualquier repositorio. Al
terminar tendrá esto:

```text
nexo-local/
  compose.yaml            el compose de la sección 5
  .env                    los secretos de la sección 6 (nunca se versiona)
  .gitignore              por si la carpeta termina dentro de un repositorio
  postgres-init/
    01-cuentas.sh         crea las cuentas nexo_owner y nexo_app
```

El script `01-cuentas.sh` está en el fork, en `deploy/nexo/postgres-init/`. Clona el repositorio una
vez (también lo necesitas si vas a construir la imagen) y copia la carpeta:

**Windows (PowerShell)**

```powershell
git clone https://github.com/Adriel2503/engram-nexo.git "$HOME\engram-nexo"
New-Item -ItemType Directory -Force "$HOME\nexo-local" | Out-Null
Copy-Item -Recurse "$HOME\engram-nexo\deploy\nexo\postgres-init" "$HOME\nexo-local\"
Set-Content "$HOME\nexo-local\.gitignore" ".env" -Encoding ascii
Set-Location "$HOME\nexo-local"
```

**macOS y Linux**

```bash
git clone https://github.com/Adriel2503/engram-nexo.git ~/engram-nexo
mkdir -p ~/nexo-local
cp -R ~/engram-nexo/deploy/nexo/postgres-init ~/nexo-local/
echo ".env" > ~/nexo-local/.gitignore
cd ~/nexo-local
```

El repositorio obliga finales de línea LF en los `.sh` (`.gitattributes`), así que el script sirve
también clonado en Windows. Si lo copias de otra forma, guárdalo con finales de línea LF.

## 4. Obtener la imagen del servidor

Elige una de las dos opciones.

### Opción A: imagen publicada en GHCR

La imagen está en `ghcr.io/adriel2503/nexo-engram-cloud` con las etiquetas `3.4.0-nexo.2` y `latest`,
para `linux/amd64` y `linux/arm64`. Como el repositorio es privado, Docker necesita iniciar sesión en
GHCR con un token de GitHub con permiso `read:packages`.

1. Crea un token clásico en GitHub: *Settings > Developer settings > Personal access tokens >
   Tokens (classic) > Generate new token*, con el permiso `read:packages` y una caducidad corta.
2. Inicia sesión en GHCR (pega el token cuando lo pida como contraseña; no queda en el historial):

   ```bash
   docker login ghcr.io -u <tu-usuario-de-github>
   ```

   Alternativa con GitHub CLI, sin crear el token a mano:

   ```bash
   gh auth refresh -h github.com -s read:packages
   gh auth token | docker login ghcr.io -u <tu-usuario-de-github> --password-stdin
   ```

   Por verificar: que el token de `gh` con `read:packages` sea aceptado por GHCR en todas las cuentas
   de la organización (con el token clásico siempre funciona).

3. Descarga la imagen:

   ```bash
   docker pull ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2
   ```

   Qué deberías ver: `Status: Downloaded newer image for ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2`.

### Opción B: construirla desde el código

Útil si estás cambiando el servidor. Desde la carpeta del fork:

```bash
cd ~/engram-nexo                    # Windows: Set-Location "$HOME\engram-nexo"
docker build -f deploy/nexo/Dockerfile --build-arg VERSION=3.4.0-nexo.2-dev -t nexo-engram-cloud:3.4.0-nexo.2-dev .
docker run --rm nexo-engram-cloud:3.4.0-nexo.2-dev version     # engram 3.4.0-nexo.2-dev
```

La imagen compila el binario dentro de Docker (no necesitas Go instalado). En una Mac con Apple
Silicon se construye para arm64; agrega `--platform linux/amd64` solo si necesitas la imagen amd64.

Para usarla en lugar de la publicada, define `CLOUD_IMAGE` en el `.env` (sección 6):

```text
CLOUD_IMAGE=nexo-engram-cloud:3.4.0-nexo.2-dev
```

## 5. El compose mínimo

Guarda esto como `compose.yaml` en `nexo-local`. Está basado en `deploy/nexo/compose.example.yaml`
(sin Caddy ni HTTPS) y en el compose de las pruebas de conocimiento.

```yaml
# Servidor Nexo local: solo en esta PC (127.0.0.1), HTTP, sin proxy.
name: nexo-local

services:
  postgres:
    image: pgvector/pgvector:pg18
    restart: unless-stopped
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?Falta POSTGRES_PASSWORD}
      POSTGRES_DB: postgres
      # Las usa postgres-init/01-cuentas.sh al crear el volumen.
      NEXO_OWNER_PASSWORD: ${NEXO_OWNER_PASSWORD:?Falta NEXO_OWNER_PASSWORD}
      NEXO_APP_PASSWORD: ${NEXO_APP_PASSWORD:?Falta NEXO_APP_PASSWORD}
      NEXO_DB_NAME: engram_cloud
    ports:
      - "127.0.0.1:54330:5432"      # solo esta PC, para DBeaver
    volumes:
      - pg18-data:/var/lib/postgresql   # Postgres 18: el volumen va aquí, no en .../data
      - ./postgres-init:/docker-entrypoint-initdb.d:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -h 127.0.0.1 -U postgres -d engram_cloud"]
      interval: 3s
      retries: 30

  cloud:
    image: ${CLOUD_IMAGE:-ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2}
    restart: unless-stopped
    depends_on:
      postgres: { condition: service_healthy }
    environment:
      ENGRAM_DATABASE_URL: postgres://nexo_app:${NEXO_APP_PASSWORD}@postgres:5432/engram_cloud?sslmode=disable
      NEXO_MIGRATION_DATABASE_URL: postgres://nexo_owner:${NEXO_OWNER_PASSWORD}@postgres:5432/engram_cloud?sslmode=disable
      ENGRAM_CLOUD_TOKEN: ${ENGRAM_CLOUD_TOKEN:?Falta ENGRAM_CLOUD_TOKEN}
      ENGRAM_JWT_SECRET: ${ENGRAM_JWT_SECRET:?Falta ENGRAM_JWT_SECRET}
      ENGRAM_CLOUD_TOKEN_PEPPER: ${ENGRAM_CLOUD_TOKEN_PEPPER:?Falta ENGRAM_CLOUD_TOKEN_PEPPER}
      ENGRAM_CLOUD_ALLOWED_PROJECTS: nexo-transversal
      ENGRAM_CLOUD_HOST: 0.0.0.0
      ENGRAM_PORT: "18080"
      ENGRAM_TIMEZONE: America/Lima
      NEXO_KB_PUBLIC_URL: http://127.0.0.1:18095
    ports:
      - "127.0.0.1:18095:18080"     # solo esta PC

volumes:
  pg18-data:
```

Sobre Postgres 18:

- La imagen `pgvector/pgvector:pg18` es el Postgres 18 oficial más la extensión pgvector (la que usa
  la búsqueda por significado de la sección 16). Sin proveedor de embeddings se comporta como un
  Postgres normal.
- En Postgres 18 el volumen se monta en `/var/lib/postgresql` (no en `/var/lib/postgresql/data`).
  Si lo montas en `.../data`, el contenedor se niega a iniciar con un mensaje que lo explica.
- Si ya tenías este servidor local con Postgres 16 (`pg-data`), no cambies solo la imagen: usa un
  volumen nuevo y migra los datos con `pg_dump` y `pg_restore`
  ([`servidor-produccion.md`](servidor-produccion.md), sección 9.5, que se probó de la 16 a la 18), o
  empieza de cero ([sección 15](#15-detener-borrar-y-empezar-de-cero)) si los datos son de prueba.

Qué hace cada parte:

| Parte | Para qué |
|---|---|
| `127.0.0.1:...` en `ports` | Publica los puertos solo en tu PC. Nadie de la red puede entrar |
| `./postgres-init` en `/docker-entrypoint-initdb.d` | Postgres ejecuta `01-cuentas.sh` **una sola vez**, al crear el volumen: crea `nexo_owner` (dueña de la base `engram_cloud`) y `nexo_app` (solo se conecta) |
| `ENGRAM_DATABASE_URL` | Cuenta de aplicación (`nexo_app`): todas las peticiones |
| `NEXO_MIGRATION_DATABASE_URL` | Cuenta dueña (`nexo_owner`): solo al arrancar, para migrar y dar permisos a `nexo_app`. Sin ella el servidor funciona con una sola cuenta y avisa "seguridad degradada" |
| `ENGRAM_CLOUD_TOKEN` | Token compartido de Engram. Es obligatorio para arrancar en modo autenticado, pero **no se entrega a nadie**: no sirve para los espacios de conocimiento (no tiene roles) |
| `ENGRAM_JWT_SECRET` | Firma de las sesiones del panel. Obligatorio, de 32 bytes o más, distinto del valor de desarrollo de Engram |
| `ENGRAM_CLOUD_TOKEN_PEPPER` | Secreto para guardar los tokens personales. De 32 bytes o más. Sin él no se pueden emitir tokens personales |
| `ENGRAM_CLOUD_ALLOWED_PROJECTS` | Lista de proyectos de memoria que acepta el servidor. Es obligatoria para arrancar; `nexo-transversal` es el valor de los despliegues de Nexo |
| `ENGRAM_CLOUD_HOST: 0.0.0.0` y `ENGRAM_PORT: "18080"` | El servidor escucha en el puerto 18080 dentro del contenedor (por defecto escucharía solo en `127.0.0.1` del contenedor y Docker no podría publicarlo) |
| `ENGRAM_TIMEZONE` | Zona horaria de las fechas del panel |
| `NEXO_KB_PUBLIC_URL` | URL de los enlaces permanentes de las citas. Debe coincidir con la que usas en el navegador (si cambias el puerto, cámbiala también) |

Para comprobar el archivo sin arrancar nada (después de crear el `.env`): `docker compose config --quiet`
(sin salida = correcto).

## 6. Generar los secretos (.env)

El `.env` va en la misma carpeta que `compose.yaml`. Contiene contraseñas y secretos: **nunca lo
subas a un repositorio** ni lo compartas. Los valores son hexadecimales para que sirvan dentro de una
URL de Postgres.

**Windows (PowerShell 5.1 o 7)**

```powershell
function New-Secreto([int]$Bytes = 32) {
  $b = New-Object byte[] $Bytes
  [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
  -join ($b | ForEach-Object { $_.ToString('x2') })
}
@(
  "POSTGRES_PASSWORD=$(New-Secreto 24)"
  "NEXO_OWNER_PASSWORD=$(New-Secreto 24)"
  "NEXO_APP_PASSWORD=$(New-Secreto 24)"
  "ENGRAM_CLOUD_TOKEN=$(New-Secreto 32)"
  "ENGRAM_JWT_SECRET=$(New-Secreto 32)"
  "ENGRAM_CLOUD_TOKEN_PEPPER=$(New-Secreto 32)"
) | Set-Content -Path .env -Encoding ascii
```

Se guarda en ASCII a propósito: `-Encoding utf8` en PowerShell 5.1 agrega una marca (BOM) al inicio y
Docker Compose no reconocería la primera variable.

**macOS y Linux (bash)**

```bash
{
  echo "POSTGRES_PASSWORD=$(openssl rand -hex 24)"
  echo "NEXO_OWNER_PASSWORD=$(openssl rand -hex 24)"
  echo "NEXO_APP_PASSWORD=$(openssl rand -hex 24)"
  echo "ENGRAM_CLOUD_TOKEN=$(openssl rand -hex 32)"
  echo "ENGRAM_JWT_SECRET=$(openssl rand -hex 32)"
  echo "ENGRAM_CLOUD_TOKEN_PEPPER=$(openssl rand -hex 32)"
} > .env
chmod 600 .env
```

Si usas la imagen construida (opción B), agrega al final la línea `CLOUD_IMAGE=...` de la sección 4.

Importante: las contraseñas de Postgres se aplican **solo al crear el volumen**. Si cambias el `.env`
después, tienes que empezar de cero ([sección 15](#15-detener-borrar-y-empezar-de-cero)).

## 7. Arrancar y comprobar

```bash
docker compose up -d
docker compose ps
```

Qué deberías ver: los dos servicios en estado `running` y `postgres` como `healthy`. La primera vez el
servidor tarda unos segundos más porque crea las tablas.

Comprueba que usa las dos cuentas de base de datos:

```powershell
docker compose logs cloud | Select-String "\[nexo\] seguridad"        # Windows
```

```bash
docker compose logs cloud | grep "\[nexo\] seguridad"                 # macOS y Linux
```

Debe aparecer una línea con `dos cuentas de base de datos (dueña ..., aplicación ...)`. También verás
avisos esperables en local, por ejemplo que la exportación de evidencia está desactivada o que
`NEXO_SUPERADMIN_ALLOWED_CIDRS` está vacía.

Comprueba que responde:

```bash
curl http://127.0.0.1:18095/health
```

Respuesta esperada:

```json
{"service":"engram-cloud","status":"ok"}
```

En Windows PowerShell 5.1, `curl` es otro comando: usa `curl.exe` o `Invoke-RestMethod http://127.0.0.1:18095/health`.

`/health` responde `ok` aunque la base no responda. Para comprobar también la base:
`curl http://127.0.0.1:18095/nexo/health/ready` debe responder `{"status":"ready"}` (con la base
caída, `503` y `{"status":"unavailable"}`).

## 8. Crear el primer administrador

El primer administrador se crea con un comando **dentro del contenedor** `cloud`. Así lo hacen las
pruebas de punta a punta.

```bash
docker compose exec -T cloud engram cloud bootstrap admin --username admin --email admin@ejemplo.local --issue-token pc-admin
```

Opciones de `engram cloud bootstrap admin`: `--username` (obligatoria), `--email`,
`--grant-project <proyecto>` (repetible) y `--issue-token [nombre]` (emite un token; sin nombre, se
llama `cli-bootstrap`).

Qué deberías ver (la primera línea empieza con una marca de verificación):

```text
Managed admin created: username=admin principal_id=<id>

Token issued — SHOWN ONCE, copy and store it now, it cannot be retrieved again:
<token>
```

- **Copia el token ahora**: no se puede volver a ver. Guárdalo en tu gestor de contraseñas. Si lo
  pierdes: `docker compose exec -T cloud engram cloud bootstrap recover-token` (solo funciona si ese
  administrador no tiene ningún token; con `--revoke-existing` reemplaza un token de arranque sin
  usar).
- Ese token vence a los 90 días.
- Este primer administrador queda como **superadmin** de Nexo (el rol único de plataforma; Engram lo ve
  como `admin`) en la misma transacción que lo crea, y el comando lo dice al terminar. Después, los roles
  se cambian desde Administración > Usuarios o con la API de roles. Si ya existe un administrador, el comando se niega: `error: a managed admin already exists`.

Para los comandos siguientes, deja el token en una variable de la terminal (no en un archivo):

```powershell
# Windows (PowerShell 7.1 o posterior)
$env:NEXO_ADMIN_TOKEN = Read-Host "Token del admin" -MaskInput
$h = @{ Authorization = "Bearer $env:NEXO_ADMIN_TOKEN" }
Invoke-RestMethod -Headers $h http://127.0.0.1:18095/nexo/authz/v1/me
```

```bash
# macOS y Linux
read -rs NEXO_ADMIN_TOKEN && export NEXO_ADMIN_TOKEN
curl -s -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" http://127.0.0.1:18095/nexo/authz/v1/me
```

La respuesta de `/nexo/authz/v1/me` muestra tu identidad (`label`, `principal_id`), los espacios que
ves y los datos del token (alcance, caducidad y último uso).

## 9. Crear personas y tokens

Cada persona necesita un **usuario** y un **token personal**. Puedes hacerlo desde el panel o desde
la API. El detalle de roles y permisos está en [administracion.md](administracion.md).

### Desde el panel

1. Entra al panel con el token del admin ([sección 10](#10-entrar-al-panel)).
2. Ve a *Administración > Usuarios* (`http://127.0.0.1:18095/dashboard/admin/users`).
3. En el formulario de creación: *Usuario*, *Correo* (opcional), *Nombre para mostrar* (opcional) y
   el rol *Miembro* o *Administrador*. Pulsa *Crear usuario*.
4. Abre el usuario en la lista. En *Tokens*: nombre del token (por ejemplo `pc-ana`), *Días de vida*
   (vacío = 90), alcance *Escritura* o *Solo lectura*. Pulsa *Crear token*.
5. Copia el token que aparece. Se muestra **una sola vez**.

### Desde la API

```powershell
# Windows (PowerShell), con $h de la sección 8
Invoke-RestMethod -Method Post -Headers $h -ContentType "application/json" http://127.0.0.1:18095/admin/users `
  -Body '{"username":"ana","email":"ana@ejemplo.local","display_name":"Ana","role":"member"}'

$r = Invoke-RestMethod -Method Post -Headers $h -ContentType "application/json" http://127.0.0.1:18095/nexo/authz/v1/tokens `
  -Body '{"principal":"ana","name":"pc-ana","expires_in_days":30,"scope":"write"}'
$r.raw_token | Set-Clipboard        # queda en el portapapeles, sin mostrarse
```

```bash
# macOS y Linux
curl -s -X POST -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" -H "Content-Type: application/json" \
  http://127.0.0.1:18095/admin/users \
  -d '{"username":"ana","email":"ana@ejemplo.local","display_name":"Ana","role":"member"}'

curl -s -X POST -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" -H "Content-Type: application/json" \
  http://127.0.0.1:18095/nexo/authz/v1/tokens \
  -d '{"principal":"ana","name":"pc-ana","expires_in_days":30,"scope":"write"}'
# La respuesta trae "raw_token": es el token de Ana. Se muestra una sola vez.
```

Detalles:

- `POST /admin/users` responde `201` con el usuario creado.
- `POST /nexo/authz/v1/tokens` acepta `principal` (usuario o id), `name`, `expires_in_days` y `scope`
  (`read` o `write`). También existe `POST /admin/users/{principal_id}/tokens` con
  `{"name":"...","expires_in_days":30,"scope":"write"}` (el `principal_id` sale de `GET /admin/users`).
- `expires_in_days`: por defecto 90; máximo `NEXO_TOKEN_MAX_DAYS` (365 por defecto). `0` (sin
  caducidad) solo para cuentas de servicio y solo lo puede emitir un superadmin.
- Un token `read` consulta pero no cambia nada (no sube ni propone).

## 10. Entrar al panel

1. Abre `http://127.0.0.1:18095/dashboard/login`.
2. Pega tu token en *Token de acceso* y pulsa *Iniciar sesión*. El panel usa los permisos de ese token.
3. Entra a la pestaña **Conocimiento** (`http://127.0.0.1:18095/dashboard/kb`).

Qué deberías ver en *Conocimiento*: un buscador, la sección "Cómo funciona" con 4 pasos, los espacios
que ves como tarjetas y, si eres administrador, el formulario *Crear espacio*.

## 11. Crear un espacio y nombrar responsable

Un espacio agrupa documentos de un tema (por ejemplo `arquitectura`). Su **responsable** sube y
publica documentos y revisa propuestas. El responsable debe existir antes como usuario
([sección 9](#9-crear-personas-y-tokens)).

### Desde el panel

En *Conocimiento > Crear espacio* (lo ve un administrador):

| Campo | Ejemplo | Nota |
|---|---|---|
| Identificador | `arquitectura` | Minúsculas, números y guiones; va en la URL. No se puede cambiar después |
| Nombre | `Arquitectura` | |
| Descripción | `Estándares de arquitectura de software` | |
| Responsable | `ana` | Usuario del panel |
| Visibilidad | *Abierto a todo el equipo* o *Restringido a sus miembros* | |

### Desde la API

```bash
curl -s -X POST -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" -H "Content-Type: application/json" \
  http://127.0.0.1:18095/nexo/kb/v1/spaces \
  -d '{"slug":"arquitectura","name":"Arquitectura","owner":"ana"}'
```

```powershell
Invoke-RestMethod -Method Post -Headers $h -ContentType "application/json" http://127.0.0.1:18095/nexo/kb/v1/spaces `
  -Body '{"slug":"arquitectura","name":"Arquitectura","owner":"ana"}'
```

Campos opcionales: `description`, `visibility` (`open` por defecto o `restricted`), `default_role` (rol
de quien entra a un espacio abierto; por defecto `lector`) y `categories`. Responde `201`.

Para dar un rol a alguien en un espacio (por ejemplo, para que Luis lea un espacio restringido), el
responsable o un administrador usa:

```bash
curl -s -X PUT -H "Authorization: Bearer <token del responsable o del admin>" -H "Content-Type: application/json" \
  http://127.0.0.1:18095/nexo/authz/v1/scopes/space/arquitectura/members/luis -d '{"role":"lector"}'
```

Roles de un espacio: `lector`, `proponente`, `publicador` y `responsable`. Para quitarlo, la misma
ruta con `DELETE`. Más detalle en [administracion.md](administracion.md).

## 12. Subir documentos

Formatos aceptados: `.md` (también `.markdown` y `.mdx`), `.txt` (y `.text`) y `.docx`, sueltos, en una
carpeta o en un zip (por ejemplo, una wiki de Azure DevOps exportada). Sube quien tenga permiso de
publicar en el espacio: su responsable o un publicador, con un token `write`.

Antes de publicar, prueba siempre sin publicar: el resultado dice qué se crearía, actualizaría,
retiraría o rechazaría (por ejemplo, documentos con secretos) sin cambiar nada.

### Desde el panel

1. Entra con el token del responsable y abre el espacio (*Conocimiento >* el espacio).
2. En *Subir documentos*, elige archivos, una carpeta o un zip. Opcionales: *Categoría*, *Colección*
   (nombre del conjunto, para retirar lo que ya no viene), *Tipo de fuente* (*Detectar*, *Archivos* o
   *Wiki de Azure DevOps*) y la casilla para retirar los documentos de la colección que ya no vienen.
3. Pulsa **Probar sin publicar** y revisa el resultado.
4. Si está bien, repite con **Publicar**.

### Desde la terminal (`engram nexo kb upload`)

El comando usa el servidor y el token configurados en tu PC. La forma más simple es conectar tu PC al
servidor local con el token del responsable ([sección 13](#13-conectar-tu-asistente-al-servidor-local)).
También puedes indicar el servidor con `--server http://127.0.0.1:18095` y el token con la variable
`ENGRAM_CLOUD_TOKEN` solo en esa terminal.

```bash
engram nexo kb upload --space arquitectura --dry-run ./docs-arquitectura
engram nexo kb upload --space arquitectura ./docs-arquitectura
```

Opciones de `upload`:

| Opción | Para qué |
|---|---|
| `--space S` | Espacio (obligatoria) |
| `--dry-run` | Muestra el resultado sin publicar |
| `--prune` | Retira los documentos de la misma colección que ya no vienen |
| `--confirm-prune` | Permite una poda mayor que el tope de seguridad (25 % de los documentos del origen) |
| `--source auto\|dir\|azure-devops-wiki` | Tipo de fuente (por defecto `auto`) |
| `--collection N` | Nombre de la colección; por defecto, el nombre de la carpeta o del zip |
| `--wiki-url URL` | URL de la wiki de Azure DevOps (para los enlaces a la fuente) |
| `--category C`, `--owner O` | Categoría y responsable por defecto de los documentos |
| `--note T` | Nota del cambio |
| `--server URL` | Servidor (si no, el configurado) |
| `--json` | Salida en JSON |

Al subir una carpeta se omiten los archivos y carpetas ocultos (como `.git` o `.attachments`),
`node_modules` y lo que no tenga una extensión aceptada.

Luego, comprueba desde la terminal:

```bash
engram nexo kb spaces
engram nexo kb search --space arquitectura "nomenclatura de tablas"
```

Cada resultado trae la cita (espacio, documento, sección, versión y vigencia) y un enlace al panel. Si
no hay nada que responda, dice: "No existe un estándar oficial publicado sobre este tema."

## 13. Conectar tu asistente al servidor local

Sigue [instalacion-dev.md](instalacion-dev.md) §7 y §8, con la URL local:

```powershell
Get-Clipboard | engram nexo connect --server http://127.0.0.1:18095 --token-stdin     # Windows
```

```bash
pbpaste | engram nexo connect --server http://127.0.0.1:18095 --token-stdin           # macOS
engram nexo connect --server http://127.0.0.1:18095 --token-stdin                     # Linux: pega y Enter
```

Luego `engram setup <asistente>` (o `engram nexo setup <asistente>` si ya estaba configurado) y
reinicia el asistente.

**Qué permite el código sin HTTPS** (leído de `cloudconfig.ValidateServerURL`,
`internal/nexo/kb/client` e `internal/cloud/remote`):

| Parte | ¿Funciona con `http://`? |
|---|---|
| `engram nexo connect` | Sí. Acepta `http` y `https`; rechaza otros esquemas y URL con `?` o `#` |
| Herramientas `nexo_*` del asistente, copia local y `engram nexo kb ...` | Sí. El cliente de conocimiento no exige HTTPS |
| Panel en el navegador | Sí |
| Sincronización de la **memoria de proyectos** de Engram (`engram sync --cloud` y la sincronización automática con `ENGRAM_CLOUD_AUTOSYNC=1`) | **No.** Con token, Engram exige HTTPS y responde `cloud: bearer token requires an HTTPS remote URL`. La sincronización automática está apagada salvo que definas `ENGRAM_CLOUD_AUTOSYNC=1`; si la tienes activa, solo verás ese error en el registro |

No hay ninguna opción del cliente para permitir HTTP en la sincronización de memoria. La variable
`ENGRAM_CLOUD_INSECURE_NO_AUTH=1` es del **servidor** (lo arranca sin autenticación), no se puede
usar junto con `ENGRAM_CLOUD_TOKEN` y tampoco sirve para Nexo: los espacios de conocimiento exigen un
token personal y responden 403 en ese modo. Para probar Nexo en local no hace falta.

**Volver al servidor del equipo.** `engram nexo connect` guarda una sola conexión. Para volver,
conecta de nuevo con la URL y tu token del servidor del equipo. Verás
`Aviso: la configuración cloud apuntaba a http://127.0.0.1:18095; ahora apunta a Nexo.`, y la copia
local se reconstruye porque cambió el servidor.

Si prefieres no tocar tu conexión real, usa otra carpeta de datos solo en una terminal para los
comandos `engram nexo` (el asistente sigue usando la conexión normal):

```powershell
$env:ENGRAM_DATA_DIR = "$HOME\nexo-local\engram-data"; $env:NEXO_HOME = "$HOME\nexo-local\nexo-home"
```

```bash
export ENGRAM_DATA_DIR=~/nexo-local/engram-data NEXO_HOME=~/nexo-local/nexo-home
```

## 14. Ver la base con DBeaver

Postgres está publicado solo en tu PC (`127.0.0.1:54330`). Crea una conexión PostgreSQL con:

| Campo | Valor |
|---|---|
| Host | `127.0.0.1` |
| Puerto | `54330` |
| Base de datos | `engram_cloud` |
| Usuario | `nexo_app` |
| Contraseña | el valor de `NEXO_APP_PASSWORD` de tu `.env` |
| SSL | desactivado |

Con `nexo_app` ves lo mismo que el servidor: puede leer y escribir las tablas operativas y solo leer o
insertar en las de evidencia (auditoría, eventos, versiones de documentos). No modifiques datos a mano:
lo que cambies no queda auditado y puede romper la verificación de la cadena de auditoría. Tablas
útiles para mirar: `nexo_kb_spaces`, `nexo_kb_documents`, `nexo_kb_versions`, `nexo_kb_sections`,
`nexo_kb_proposals`, `nexo_kb_events`, `nexo_kb_publish_runs`, `nexo_role_bindings` y
`cloud_auth_audit_log`.

Para ver la contraseña sin abrir el archivo en un editor:

```powershell
(Select-String '^NEXO_APP_PASSWORD=' .env).Line.Split('=',2)[1] | Set-Clipboard     # Windows
```

```bash
grep '^NEXO_APP_PASSWORD=' .env | cut -d= -f2-                                        # macOS y Linux
```

## 15. Detener, borrar y empezar de cero

Desde la carpeta `nexo-local`:

| Quieres | Comando | Qué pasa con los datos |
|---|---|---|
| Pausar | `docker compose stop` | Se conservan. `docker compose start` vuelve a levantar |
| Apagar y quitar los contenedores | `docker compose down` | Se conservan (quedan en el volumen `pg18-data`). `docker compose up -d` vuelve a levantar |
| Empezar de cero | `docker compose down -v` | **Se borra todo**: usuarios, tokens, espacios y documentos. No se puede deshacer |
| Ver el registro del servidor | `docker compose logs -f cloud` | |
| Actualizar la imagen | `docker compose pull` y `docker compose up -d` | Se conservan; el servidor migra solo al arrancar |

Después de empezar de cero:

1. Los tokens anteriores dejan de servir. En tu PC, ejecuta `engram nexo disconnect` (o vuelve a
   conectar con un token nuevo).
2. Repite desde la [sección 7](#7-arrancar-y-comprobar). Si cambiaste las contraseñas, también la
   [sección 6](#6-generar-los-secretos-env).

Si cambias `NEXO_OWNER_PASSWORD` o `NEXO_APP_PASSWORD` en el `.env` **sin** borrar el volumen, el
servidor no podrá conectarse. Para aplicar las contraseñas nuevas sin borrar los datos, recrea los
contenedores (toman los valores nuevos del `.env`) y ejecuta a mano el script de cuentas, que es
idempotente:

```bash
docker compose up -d
docker compose exec -T postgres sh /docker-entrypoint-initdb.d/01-cuentas.sh
docker compose restart cloud
```

Por verificar: este procedimiento no se probó; el encabezado de `01-cuentas.sh` documenta la variante
`docker compose exec -T -e NEXO_OWNER_PASSWORD -e NEXO_APP_PASSWORD postgres sh ...`, que toma las
contraseñas de la terminal y no del `.env`. Si falla, usa `docker compose down -v`.

## 16. Variables de entorno útiles para desarrollo

Se agregan en `environment:` del servicio `cloud` y se aplican con `docker compose up -d` (Docker
recrea el contenedor). Los valores por defecto están leídos del código.

### Conocimiento: búsqueda (R1)

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_KB_R1` | vacía: todas las mejoras de R1 activas | `off`, `all` o una lista separada por comas de `doc_context`, `split_tables`, `synonyms`, `threshold`, `diversity`, `quote`. Si cambias `doc_context`, reindexa con `engram nexo kb reindex` (superadmin) |
| `NEXO_KB_MIN_COVERAGE` | `0.3` | Umbral de cobertura para responder "no existe un estándar oficial". Más alto: dice "no hay" más seguido |
| `NEXO_KB_MAX_PER_DOC` | `2` | Máximo de secciones de un mismo documento por respuesta |
| `NEXO_KB_RANK_CANDIDATES` | `20000` | Tope de secciones candidatas por búsqueda antes de ordenar (mínimo 100) |
| `NEXO_KB_PUBLIC_URL` | vacía: el esquema y el host de la petición (detrás de un proxy de `NEXO_TRUSTED_PROXIES`, los que informa el proxy) | URL de los enlaces de las citas; si está, manda |
| `NEXO_KB_DISABLE_UNACCENT` | vacía | `1` al crear la configuración de búsqueda: no usar la extensión `unaccent` |

### Conocimiento: búsqueda por significado (R2, opcional)

Para probar la búsqueda híbrida, que combina palabras y significado y encuentra un estándar aunque
la pregunta use otras palabras:

1. Usa la imagen `pgvector/pgvector:pg18` del compose de la sección 5 (ya trae la extensión pgvector;
   `01-cuentas.sh` la crea al iniciar el volumen).
   - Si tu volumen es anterior y se creó sin la extensión, pide a la cuenta `postgres` que la cree:
     `docker compose exec postgres psql -U postgres -d engram_cloud -c "CREATE EXTENSION IF NOT EXISTS vector SCHEMA public"`.
2. Agrega al servicio `cloud` lo siguiente, con la clave en el `.env`, nunca en el compose:

   ```yaml
         NEXO_KB_EMBEDDER: ${NEXO_KB_EMBEDDER:-off}
         NEXO_KB_EMBED_API_KEY: ${NEXO_KB_EMBED_API_KEY:-}
   ```

   En el `.env`: `NEXO_KB_EMBEDDER=gemini` y `NEXO_KB_EMBED_API_KEY=<tu clave de Google AI Studio>`.
3. Ejecuta `docker compose up -d`.
4. Comprueba el arranque: `docker compose logs cloud | grep "\[nexo\] kb"` debe mostrar
   `pgvector ... disponible` e `indexador de vectores en marcha`. Unos segundos después de subir
   documentos, debe mostrar `índice vectorial v1 activo`.
5. Comprueba una búsqueda: `engram nexo kb search --json "<pregunta>"` debe devolver
   `"mode":"hybrid"`. Para comparar con la búsqueda solo por palabras, usa `--mode fts`.

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_KB_EMBEDDER` | `off` | `gemini`, `openai`, `azure-openai` u `openai-compatible`. Con `off` (o sin pgvector) todo funciona como antes, solo por texto |
| `NEXO_KB_EMBED_API_KEY` | — | Clave del proveedor. También sirven `GEMINI_API_KEY` y `OPENAI_API_KEY` |
| `NEXO_KB_EMBED_MODEL`, `NEXO_KB_EMBED_DIMS` | `gemini-embedding-001`, 768 | Modelo y dimensión. Cambiarlos crea un índice nuevo, construido en segundo plano |
| `NEXO_KB_EMBED_URL` | — | Para un modelo local con Ollama: `NEXO_KB_EMBEDDER=openai-compatible`, `NEXO_KB_EMBED_URL=http://host.docker.internal:11434/v1`, `NEXO_KB_EMBED_MODEL=bge-m3`, `NEXO_KB_EMBED_DIMS=1024` |
| `NEXO_KB_MIN_SIMILARITY`, `NEXO_KB_MIN_TEXT_SIMILARITY` | `0.70`, `0.65` | Umbral de "no existe un estándar oficial" en el modo híbrido (calibrado con Gemini a 768 dimensiones) |
| `NEXO_KB_RRF_VECTOR_WEIGHT`, `NEXO_KB_RRF_TEXT_WEIGHT` | `0.7`, `0.3` | Pesos de la fusión entre significado y palabras |
| `NEXO_KB_VECTOR_WORKER` | activo | `off` apaga el indexador en segundo plano |

Con un proveedor externo, el texto de los documentos y las preguntas sale de tu PC hacia el
proveedor. Para una demo con documentos internos, confirma que está permitido.

### Conocimiento: límites de subida y publicación

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_KB_MAX_DOC_BYTES` | `2097152` (2 MB) | Tamaño máximo de un documento |
| `NEXO_KB_MAX_BATCH` | `200` | Documentos por pedido de publicación |
| `NEXO_KB_MAX_REQUEST_BYTES` | `33554432` (32 MB) | Cuerpo máximo de una petición a `/nexo/kb/v1` (413 si se supera) |
| `NEXO_KB_UPLOAD_MAX_BYTES` | `268435456` (256 MB) | Cuerpo máximo de una subida |
| `NEXO_KB_UPLOAD_MAX_FILES` | `2000` | Archivos indexables por subida |
| `NEXO_KB_UPLOAD_MAX_FILE_BYTES` | `20971520` (20 MB) | Tamaño máximo por archivo |
| `NEXO_KB_UPLOAD_MAX_UNZIPPED` | `67108864` (64 MB) | Suma de lo descomprimido de un zip |
| `NEXO_KB_UPLOAD_MAX_ENTRIES` | `10000` | Entradas de un zip |
| `NEXO_KB_PRUNE_MAX_PERCENT` | `25` | La poda no retira más de ese porcentaje sin `--confirm-prune` |
| `NEXO_KB_PRUNE_MIN_DOCS` | `5` | Hasta esa cantidad de documentos no se aplica el porcentaje |

### Límites, bloqueo y tokens

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_RATE_LIMITS` | vacía: `sync` 600/600, `admin` 120/120, `login` 20/-, `nexo` 300/300, `other` 600/- (por IP / por token, por minuto) | JSON por grupo, por ejemplo `{"nexo":{"ip":3000,"token":3000}}` (`0` = sin límite), u `off` para desactivarlo. Útil en pruebas de carga |
| `NEXO_AUTH_LOCKOUT` | vacía: 20 tokens distintos inválidos desde una IP en 300 s bloquean 900 s | JSON `{"max_failures","user_max_failures","window_seconds","lockout_seconds"}` u `off`. En local todas las peticiones vienen de la misma IP: si pruebas tokens inválidos a propósito, puedes bloquearte |
| `NEXO_TOKEN_MAX_DAYS` | `365` | Caducidad máxima al emitir un token (por defecto se emiten con 90 días) |
| `NEXO_SUPERADMIN_ALLOWED_CIDRS` | vacía (sin restricción, con aviso) | Redes desde las que se administran admins y ajustes de plataforma |
| `NEXO_TRUSTED_PROXIES` | vacía | IP o CIDR de un proxy cuyas cabeceras `X-Forwarded-For`, `X-Forwarded-Proto` y `X-Forwarded-Host` se creen. En local, sin proxy, se deja vacía |

### Evidencia y registro

| Variable | Por defecto | Efecto |
|---|---|---|
| `NEXO_AUDIT_DEDUP_SECONDS` | `600` | Ventana del resumen de rechazos repetidos; `0` = una fila por rechazo |
| `NEXO_AUDIT_VERIFY_MINUTES` | `60` | Cada cuánto se verifica la cadena de auditoría; `0` = solo a pedido |
| `NEXO_ACTIVITY_RETENTION_DAYS` | `180` | Días que se conserva el registro de conexiones (`0` = no depurar; mínimo 30) |
| `NEXO_ALERT_WEBHOOK_URL` | vacía | Webhook de Slack o Teams para las alertas |
| `NEXO_AUDIT_EXPORT_DIR`, `NEXO_AUDIT_EXPORT_KEY` | vacías (exportación desactivada, con aviso) | Exportación diaria firmada. En local normalmente no hace falta |

Las variables del cliente (en tu PC, no en el contenedor), como `NEXO_KB_TIMEOUT_SECONDS` o
`NEXO_KB_SYNC_MINUTES`, están en [instalacion-dev.md](instalacion-dev.md) §17. El listado completo de
variables del fork está en `NEXO_CHANGES.md` §5.

## 17. Problemas frecuentes

| Síntoma | Causa probable | Solución |
|---|---|---|
| `required variable POSTGRES_PASSWORD is missing a value: Falta POSTGRES_PASSWORD` (u otra) | Falta el `.env`, está en otra carpeta o tiene una marca BOM | Genera el `.env` como en la [sección 6](#6-generar-los-secretos-env), en la carpeta de `compose.yaml` |
| `ports are not available` o `bind: An attempt was made to access a socket in a way forbidden` | Puerto ocupado o reservado por Windows | Revisa la [sección 2](#2-requisitos) y cambia el puerto de la izquierda en `ports` (y `NEXO_KB_PUBLIC_URL` si es el del servidor) |
| `docker pull` responde `denied` o `unauthorized` | No iniciaste sesión en GHCR o el token no tiene `read:packages` | [Sección 4](#4-obtener-la-imagen-del-servidor), opción A, o construye la imagen (opción B) |
| `cloud` se reinicia sin parar | Error de configuración al arrancar | `docker compose logs cloud`. Mensajes típicos: falta `ENGRAM_JWT_SECRET` o es el valor de desarrollo; `jwt secret must be at least 32 bytes`; `dedicated cloud token pepper must be at least 32 bytes`; `cloud project allowlist is required`; la cuenta de aplicación tiene permisos de más (el servidor no arranca por seguridad) |
| `password authentication failed for user "nexo_app"` | Cambiaste el `.env` después de crear el volumen | [Sección 15](#15-detener-borrar-y-empezar-de-cero) |
| El registro dice `seguridad degradada: una sola cuenta de base de datos` | Falta `NEXO_MIGRATION_DATABASE_URL` | Agrégala como en el compose de la [sección 5](#5-el-compose-mínimo) |
| `error: --issue-token requires ENGRAM_CLOUD_TOKEN_PEPPER to be configured` | Falta el pepper en el servicio `cloud` | Agrégalo al `.env` y ejecuta `docker compose up -d` |
| `error: a managed admin already exists` | Ya creaste el primer administrador | Usa su token. Si lo perdiste: `engram cloud bootstrap recover-token` ([sección 8](#8-crear-el-primer-administrador)) |
| `engram nexo connect` responde `el token no es de un usuario administrado` | Usaste `ENGRAM_CLOUD_TOKEN` (el token compartido) | Usa un token personal ([sección 9](#9-crear-personas-y-tokens)) |
| `engram nexo connect` responde `el servidor no es un servidor Nexo` | La URL apunta a otro servicio o a una imagen de Engram sin Nexo | Revisa el puerto y la imagen (`docker compose ps`) |
| Respuestas `429` con `rate_limited` o `auth_locked` | Límite de peticiones o bloqueo por tokens inválidos | Espera o ajusta `NEXO_RATE_LIMITS` / `NEXO_AUTH_LOCKOUT` ([sección 16](#16-variables-de-entorno-útiles-para-desarrollo)) |
| Los enlaces de las citas apuntan a otro host o puerto | `NEXO_KB_PUBLIC_URL` no coincide con la URL que usas | Corrígela y ejecuta `docker compose up -d` |
| La subida responde 403 | El token es `read` o la persona no es responsable ni publicadora del espacio | Usa un token `write` del responsable o asigna el rol ([sección 11](#11-crear-un-espacio-y-nombrar-responsable)) |
