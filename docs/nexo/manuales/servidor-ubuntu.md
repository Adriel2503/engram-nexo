# Instalar Nexo IA en Ubuntu

Manual para una VM Ubuntu nueva, con PostgreSQL externo y arranque automático mediante systemd.
Los comandos se ejecutan en Bash con un usuario que tenga `sudo`.

**Recorrido:** preparar la VM → instalar Go y Nexo → conectar la base → iniciar → crear el administrador.

El comando del servidor es `engram cloud serve`. El panel y las API están incluidos en el ejecutable.
Los comandos de Nexo están contrastados con el código; falta validar el despliegue completo en la VM.

## 1. Qué necesitas antes de empezar

| Necesitas | Qué debes tener |
|---|---|
| VM Ubuntu | Acceso SSH, `sudo` y salida a Internet para descargar código y dependencias |
| Repositorio | Permiso de lectura en Azure DevOps |
| PostgreSQL | Base dedicada, host, puerto y las dos cuentas descritas en el paso 5 |
| Conexión a la base | Acceso desde la VM, TLS y certificado CA |
| Dirección del servicio | Un dominio, por ejemplo `https://nexo.ejemplo.com` |
| HTTPS | Proxy o balanceador configurado por infraestructura; puede ser Nginx, Caddy u otro |

```text
PCs y asistentes → HTTPS → Nexo en Ubuntu → PostgreSQL
```

Esta instalación no requiere Docker ni carga los datos de la demo.

## 2. Preparar Ubuntu y descargar el código

Instala las herramientas:

```sh
sudo apt-get update
sudo apt-get install -y git curl ca-certificates openssl nano build-essential
```

Descarga el repositorio como tu usuario normal:

```sh
mkdir -p "$HOME/src"
cd "$HOME/src"
git clone --branch main https://dev.azure.com/realplaza/AgentAI/_git/nexo-ai-playground engram-nexo
cd engram-nexo
```

`--branch main` selecciona esa rama al clonar. Si se solicita autenticación, usa el mecanismo
aprobado de Azure DevOps; no incluyas credenciales en la URL.

## 3. Instalar Go

La versión requerida está en `go.mod`; actualmente es `1.27.2`. Comprueba la arquitectura:

```sh
uname -m
```

Para Intel/AMD (`x86_64`), copia este bloque:

```sh
sudo test ! -e /usr/local/go &&
curl -fL https://go.dev/dl/go1.27.2.linux-amd64.tar.gz -o /tmp/nexo-go.tar.gz &&
sudo tar -C /usr/local -xzf /tmp/nexo-go.tar.gz
```

Para ARM (`aarch64`), cambia `linux-amd64` por `linux-arm64` en la URL.
Si ya existe `/usr/local/go`, el bloque se detiene sin modificarlo: comprueba su versión con
`/usr/local/go/bin/go version`. Si es la adecuada, continúa con el PATH:

```sh
printf '\nexport PATH=/usr/local/go/bin:$PATH\n' >> "$HOME/.profile"
export PATH="/usr/local/go/bin:$PATH"
go version
```

El cambio de `.profile` se hace una sola vez. Si cambia `go.mod`, ajusta la versión de descarga.
Referencia: [instalación oficial](https://go.dev/doc/install) y [descargas/SHA256](https://go.dev/dl/).

## 4. Compilar e instalar Nexo

Desde `~/src/engram-nexo`:

```sh
go mod download
mkdir -p out
go build -o out/engram ./cmd/engram
./out/engram version
```

Si falla la compilación, detente. Si termina correctamente, crea el usuario Linux y las carpetas:

```sh
sudo useradd --system --user-group --home-dir /var/lib/nexo --shell /usr/sbin/nologin nexo
sudo install -d -m 0755 /opt/nexo
sudo install -d -m 0750 -o root -g nexo /etc/nexo
sudo install -d -m 0750 -o root -g nexo /etc/nexo/certs
sudo install -d -m 0750 -o nexo -g nexo /var/lib/nexo
sudo install -m 0755 out/engram /opt/nexo/engram
```

| Ruta | Qué guarda |
|---|---|
| `/opt/nexo/engram` | Ejecutable |
| `/etc/nexo/nexo.env` | Conexiones y secretos privados |
| `/etc/nexo/certs/postgres-ca.crt` | CA para verificar PostgreSQL |
| `/var/lib/nexo` | Directorio de trabajo |
| `/etc/systemd/system/nexo.service` | Servicio de arranque automático |

## 5. Preparar PostgreSQL y sus cuentas

Pide al administrador de PostgreSQL una base dedicada, por ejemplo `nexo`, con estas dos cuentas:

| Cuenta | Para qué sirve | Quién la crea |
|---|---|---|
| `nexo_owner` | Dueña de la base y del esquema; aplica migraciones al iniciar | Administrador de PostgreSQL |
| `nexo_app` | Cuenta limitada que atiende las peticiones normales | Administrador de PostgreSQL |

Ninguna debe ser superusuario. `nexo_app` necesita `LOGIN`, `NOINHERIT`, conexión a la base y uso del
esquema; no debe ser dueña de objetos, crear objetos ni pertenecer a la cuenta dueña. Al iniciar,
Nexo concede los permisos operativos usando `nexo_owner`.

Solicita también el host, puerto, contraseñas y CA de PostgreSQL. La base debe permitir `unaccent`;
`pgvector` es opcional para búsqueda por significado. Los permisos exactos están en el
[manual de producción](servidor-produccion.md#2-requisitos).

Instala el certificado recibido, sustituyendo la ruta de origen:

```sh
sudo install -m 0640 -o root -g nexo /ruta/recibida/ca.crt /etc/nexo/certs/postgres-ca.crt
```

## 6. Configurar conexiones y credenciales

En esta instalación nueva, crea el archivo privado y ábrelo:

```sh
sudo install -m 0600 /dev/null /etc/nexo/nexo.env
sudo nano /etc/nexo/nexo.env
```

El primer comando vacía el archivo si ya existe: no lo repitas para actualizar.
Pega la plantilla y sustituye `<HOST_DB>`, las contraseñas, la red de administración y el dominio:

```ini
ENGRAM_CLOUD_HOST=127.0.0.1
ENGRAM_PORT=18080
ENGRAM_DATABASE_URL='postgres://nexo_app:<PASSWORD_APP>@<HOST_DB>:5432/nexo?sslmode=verify-full&sslrootcert=/etc/nexo/certs/postgres-ca.crt'
NEXO_MIGRATION_DATABASE_URL='postgres://nexo_owner:<PASSWORD_OWNER>@<HOST_DB>:5432/nexo?sslmode=verify-full&sslrootcert=/etc/nexo/certs/postgres-ca.crt'
NEXO_DB_REQUIRE_TLS=true
ENGRAM_CLOUD_ALLOWED_PROJECTS=mi-proyecto
NEXO_KB_PUBLIC_URL=https://nexo.ejemplo.com
NEXO_TRUSTED_PROXIES=127.0.0.1/32
NEXO_SUPERADMIN_ALLOWED_CIDRS=<RED_ADMIN_CIDR>
```

| Configuración | Significado |
|---|---|
| `ENGRAM_DATABASE_URL` | Conexión normal con `nexo_app` |
| `NEXO_MIGRATION_DATABASE_URL` | Conexión con `nexo_owner` para actualizar las tablas al iniciar |
| `verify-full` y `NEXO_DB_REQUIRE_TLS` | Conexiones cifradas con certificado PostgreSQL verificado |
| `ENGRAM_CLOUD_ALLOWED_PROJECTS` | Proyectos de memoria permitidos, separados por comas; no son espacios de conocimiento |
| `NEXO_KB_PUBLIC_URL` | URL pública de los enlaces a documentos |
| `NEXO_TRUSTED_PROXIES` | IP del proxy autorizado; el ejemplo supone un proxy en la misma VM |
| `NEXO_SUPERADMIN_ALLOWED_CIDRS` | IP o red de administración/VPN; una IP IPv4 única lleva `/32` |

Si una contraseña tiene caracteres especiales, pide la URL ya codificada. Escribe cada variable
en una sola línea. Los marcadores `<...>` deben reemplazarse antes de continuar.

Ahora genera los tres secretos internos en el archivo, sin mostrarlos. Ejecuta este bloque una sola vez:

```sh
sudo bash -c '
set -eu
for nombre in ENGRAM_CLOUD_TOKEN ENGRAM_JWT_SECRET ENGRAM_CLOUD_TOKEN_PEPPER; do
  if grep -q "^${nombre}=" /etc/nexo/nexo.env; then
    echo "Ya existen secretos; revisa el archivo antes de continuar." >&2
    exit 1
  fi
done
for nombre in ENGRAM_CLOUD_TOKEN ENGRAM_JWT_SECRET ENGRAM_CLOUD_TOKEN_PEPPER; do
  valor=$(openssl rand -hex 32)
  printf "%s=%s\n" "$nombre" "$valor" >> /etc/nexo/nexo.env
done
'
```

| Secreto | Qué hace |
|---|---|
| `ENGRAM_CLOUD_TOKEN` | Token compartido obligatorio; se mantiene privado y no se entrega a usuarios |
| `ENGRAM_JWT_SECRET` | Firma las sesiones del panel |
| `ENGRAM_CLOUD_TOKEN_PEPPER` | Protege los tokens personales; cambiarlo invalida los ya emitidos |

Conserva estos valores en el gestor de secretos del equipo. No los vuelvas a generar al actualizar.
El servidor recibe estas variables mediante systemd; no lee automáticamente un `.env` junto al binario.

## 7. Iniciar como servicio

Crea el archivo:

```sh
sudo nano /etc/systemd/system/nexo.service
```

Pega este contenido:

```ini
[Unit]
Description=Nexo IA
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=nexo
Group=nexo
WorkingDirectory=/var/lib/nexo
EnvironmentFile=/etc/nexo/nexo.env
ExecStart=/opt/nexo/engram cloud serve
Restart=on-failure
RestartSec=5
UMask=0077

[Install]
WantedBy=multi-user.target
```

Inicia y comprueba:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now nexo
sudo systemctl status nexo --no-pager
curl -fsS http://127.0.0.1:18080/nexo/health/ready
```

Debe responder `{"status":"ready"}`. El servidor crea o actualiza sus tablas al iniciar.
Si falla, consulta `sudo journalctl -u nexo -n 100 --no-pager`.

## 8. Publicar el acceso por HTTPS

Infraestructura puede usar **Nginx, Caddy, un balanceador u otro proxy HTTPS**. Debe configurar:

- El dominio y su certificado válido.
- El destino `http://127.0.0.1:18080`, si el proxy está en la misma VM.
- Las cabeceras `X-Forwarded-For`, `X-Forwarded-Proto` y `X-Forwarded-Host`, controladas por el proxy.
- `NEXO_TRUSTED_PROXIES` con la IP real de ese proxy y `NEXO_KB_PUBLIC_URL` con el dominio final.

La plantilla escucha solo en loopback: si el proxy está en otra máquina, ajusta la dirección
`ENGRAM_CLOUD_HOST` y permite el puerto interno únicamente desde el proxy.

Después comprueba, usando el dominio real:

```sh
curl -fsS https://nexo.ejemplo.com/nexo/health/ready
```

Como referencia de configuración, el repositorio incluye un
[ejemplo de Caddy](../../../deploy/nexo/Caddyfile.example). La elección e instalación del proxy
quedan a cargo de infraestructura.

## 9. Crear el primer usuario y entrar

**Al iniciar con una base nueva no hay personas ni contraseñas predeterminadas para entrar al panel.**
Estas son las cuentas que intervienen en este manual:

| Cuenta | Dónde existe | Cómo aparece |
|---|---|---|
| `nexo` | Ubuntu | La creas en el paso 4 para ejecutar el servicio |
| `nexo_owner` y `nexo_app` | PostgreSQL | Las prepara el administrador de la base en el paso 5 |
| `admin` | Panel Nexo | La creas con el comando siguiente; queda como superadmin |
| Resto de personas | Panel Nexo | Se crean después desde el panel, con sus roles y accesos |

Crea el primer superadmin desde la VM. El token inicial se guardará en un archivo privado:

```sh
sudo bash -c '
set -eu
set -a
. /etc/nexo/nexo.env
set +a
umask 077
if test -e /root/nexo-primer-acceso.txt; then
  echo "Ya existe el archivo de primer acceso; revísalo antes de continuar." >&2
  exit 1
fi
/opt/nexo/engram cloud bootstrap admin --username admin --issue-token pc-admin \
  > /root/nexo-primer-acceso.txt
'
```

`admin` es el nombre elegido en este ejemplo; puedes cambiarlo. `pc-admin` es el nombre del token.
El comando se usa para el primer administrador y se rechaza si ya existe un administrador activo.

Consulta el token de forma privada:

```sh
sudo nano /root/nexo-primer-acceso.txt
```

Entra en `https://nexo.ejemplo.com/dashboard/login` con ese token, desde la red de administración.
No hay una contraseña inicial: este acceso usa un token personal y tiene caducidad.
El token compartido de configuración no sirve como identidad de esta persona.

Desde el panel crea las demás personas, sus permisos, los espacios y los documentos.
No se cargan automáticamente los usuarios ni los documentos de la demo.
Más detalles en [administración](administracion.md).

## 10. Conectar una PC y operar el servicio

En la PC de cada persona, con el ejecutable del fork disponible:

```sh
engram nexo connect --server https://nexo.ejemplo.com --activate-stdin
engram setup codex
```

Introduce el código de activación cuando lo solicite el comando. Si recibiste un token personal,
usa `--token-stdin`. Reinicia el asistente después. Consulta la
[instalación para desarrolladores](instalacion-dev.md) para otros asistentes.

Comandos habituales en la VM:

| Acción | Comando |
|---|---|
| Ver el estado | `sudo systemctl status nexo --no-pager` |
| Ver registros | `sudo journalctl -u nexo -n 100 --no-pager` |
| Aplicar cambios de configuración | `sudo systemctl restart nexo` |
| Detener | `sudo systemctl stop nexo` |
| Volver a iniciar | `sudo systemctl start nexo` |

Para este despliegue se usa systemd. Las actualizaciones, respaldos y recuperación se describen en el
[manual de producción](servidor-produccion.md); conserva la base, los certificados y los secretos.
