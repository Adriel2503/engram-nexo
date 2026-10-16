# PostgreSQL en Ubuntu con TLS para Nexo

Guía para preparar una base nueva en la misma VM donde instalaste Nexo, con dos cuentas,
contraseñas y certificado verificado. No requiere Docker. Ejecuta los comandos en Bash,
en orden, con un usuario que tenga `sudo`. Si un comando falla, detente antes del siguiente.

Esta guía configura PostgreSQL; el servidor Nexo se inicia después con **`engram cloud serve`**.
En el [manual de Ubuntu](servidor-ubuntu.md#7-iniciar-como-servicio), systemd ejecuta ese mismo
comando mediante `ExecStart=/opt/nexo/engram cloud serve` y lo mantiene en segundo plano.

**Antes de empezar:** completa los pasos 1 a 4 del manual de Ubuntu. Deben existir el usuario
Linux `nexo` y la carpeta `/etc/nexo/certs`. No ejecutes la creación de cuentas sobre una base
existente: está pensada para una instalación nueva. Los comandos están revisados contra la
configuración de Nexo y la documentación oficial; falta validar este recorrido completo en la VM.

## 1. Instalar PostgreSQL 18 y pgvector

Esta guía selecciona **PostgreSQL 18** explícitamente. El paquete genérico `postgresql` puede
instalar otra versión según Ubuntu. Añade el repositorio oficial de PostgreSQL:

```sh
sudo apt update
sudo apt install postgresql-common ca-certificates openssl nano
sudo /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh
```

Confirma la incorporación del repositorio cuando el asistente lo solicite. Si tu versión de
Ubuntu no está admitida, detente y revisa las
[versiones compatibles del repositorio](https://www.postgresql.org/download/linux/ubuntu/).

Instala el servidor y la extensión para esa misma versión:

```sh
sudo apt update
sudo apt install postgresql-18 postgresql-client-18 postgresql-18-pgvector
sudo systemctl enable --now postgresql
pg_lsclusters
sudo -u postgres psql -p 5432 -c 'SHOW server_version;'
```

Comprueba que el servidor en el puerto `5432` sea **18.x** y que su instancia aparezca `online`.
Si ya había otra versión, instalar 18 puede crear una instancia en otro puerto: este procedimiento
no actualiza ni migra la anterior. Detente antes del paso 2 si la versión o el puerto no coinciden;
no borres la instancia existente. Los pasos siguientes asumen PostgreSQL 18 en `5432`.

`postgresql-18-pgvector` instala los archivos de pgvector en el servidor; en el paso 2 lo
habilitarás dentro de la base `nexo`. PostgreSQL queda como servicio del sistema y no depende
de mantener una terminal abierta.

## 2. Crear la base y sus dos cuentas

Estas cuentas son de PostgreSQL, distintas del usuario Linux `nexo`:

| Cuenta | Función |
|---|---|
| `nexo_owner` | Dueña de la base y del esquema; aplica migraciones al iniciar Nexo |
| `nexo_app` | Atiende las peticiones normales con permisos limitados |

Abre la consola de PostgreSQL:

```sh
sudo -u postgres psql
```

Ejecuta dentro de ella:

```sql
SET password_encryption = 'scram-sha-256';

CREATE ROLE nexo_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE nexo_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;

\password nexo_owner
\password nexo_app
```

Cada `\password` pide una contraseña y su confirmación sin mostrarlas. Usa dos diferentes y
guárdalas en un gestor de contraseñas. Necesitarás ambas al configurar Nexo. Las contraseñas
largas formadas por letras y números evitan tener que codificar caracteres especiales en la URL.

Continúa en la misma consola:

```sql
CREATE DATABASE nexo OWNER nexo_owner;
REVOKE ALL ON DATABASE nexo FROM PUBLIC;
GRANT CONNECT ON DATABASE nexo TO nexo_app;

\connect nexo

ALTER SCHEMA public OWNER TO nexo_owner;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO nexo_app;

CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE EXTENSION IF NOT EXISTS vector;

SELECT extname, extversion FROM pg_extension
WHERE extname IN ('unaccent', 'vector');

\quit
```

Nexo concede los permisos operativos sobre sus tablas al arrancar con la cuenta dueña.
No concedas superusuario ni membresía en `nexo_owner` a `nexo_app`.

La consulta debe mostrar ambas extensiones. Comprueba que `vector` tenga versión **0.8 o posterior**.
Si no aparece o la versión es menor, detente antes de configurar embeddings. La extensión se llama
`vector` en SQL aunque el proyecto y el paquete se llamen pgvector.

`unaccent` permite buscar texto sin distinguir acentos; pgvector almacena y consulta los vectores
de la búsqueda por significado. Instalarlo no genera embeddings por sí solo: también configurarás
un proveedor en el paso 7.

## 3. Crear los certificados del laboratorio

Las contraseñas identifican a cada cuenta. TLS cifra la conexión, y el certificado permite a
Nexo verificar la identidad de PostgreSQL. Aquí crearás una CA propia para practicar; en una
base administrada en la nube, utiliza la CA y las instrucciones del proveedor.

| Archivo | Uso |
|---|---|
| `ca.crt` | Certificado público de la CA; Nexo lo usa para verificar PostgreSQL |
| `ca.key` | Clave privada de la CA; permite firmar certificados y debe quedar protegida |
| `server.crt` | Certificado que presenta PostgreSQL |
| `server.key` | Clave privada que usa PostgreSQL; no se entrega a los clientes |

Prepara una carpeta privada. Haz este paso una sola vez: generar otra CA sobrescribiría los
archivos y cambiaría la identidad en la que confías.

```sh
mkdir -p "$HOME/nexo-pg-tls"
chmod 700 "$HOME/nexo-pg-tls"
cd "$HOME/nexo-pg-tls"
umask 077
```

Crea la CA. OpenSSL pedirá una contraseña para proteger `ca.key`; consérvala:

```sh
openssl req -x509 -newkey rsa:3072 -sha256 -days 3650 \
  -keyout ca.key -out ca.crt \
  -subj "/CN=Nexo laboratorio CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign"
```

Crea la clave y la solicitud de certificado del servidor:

```sh
openssl req -new -newkey rsa:3072 -nodes \
  -keyout server.key -out server.csr \
  -subj "/CN=localhost"
```

La clave del servidor no lleva contraseña para permitir el arranque automático; se protege
con permisos de archivo. Define las direcciones con las que te conectarás:

```sh
printf '%s\n' \
  'basicConstraints=critical,CA:FALSE' \
  'keyUsage=critical,digitalSignature,keyEncipherment' \
  'extendedKeyUsage=serverAuth' \
  'subjectAltName=DNS:localhost,IP:127.0.0.1' > server.ext
```

Firma el certificado. Se pedirá la contraseña de `ca.key`:

```sh
openssl x509 -req -in server.csr \
  -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 365 -sha256 -extfile server.ext

openssl verify -CAfile ca.crt server.crt
```

El último comando debe mostrar `server.crt: OK`. El certificado del servidor dura 365 días;
deberás renovarlo antes de que venza. Solo es válido para `localhost` y `127.0.0.1`.

## 4. Instalar los certificados

Desde la carpeta `~/nexo-pg-tls`:

```sh
sudo install -d -m 0750 -o postgres -g postgres /etc/postgresql/nexo-tls

sudo install -m 0600 -o postgres -g postgres \
  server.key /etc/postgresql/nexo-tls/server.key

sudo install -m 0644 -o postgres -g postgres \
  server.crt /etc/postgresql/nexo-tls/server.crt

sudo install -m 0640 -o root -g nexo \
  ca.crt /etc/nexo/certs/postgres-ca.crt
```

`install` copia los archivos y establece sus permisos; no mueve los originales.
Mantén `ca.key` protegida y fuera del repositorio. Nexo solo necesita `ca.crt`.

## 5. Activar y exigir TLS

Entra en PostgreSQL:

```sh
sudo -u postgres psql
```

Ejecuta:

```sql
ALTER SYSTEM SET ssl = 'on';
ALTER SYSTEM SET ssl_cert_file = '/etc/postgresql/nexo-tls/server.crt';
ALTER SYSTEM SET ssl_key_file = '/etc/postgresql/nexo-tls/server.key';
ALTER SYSTEM SET password_encryption = 'scram-sha-256';
ALTER SYSTEM SET listen_addresses = 'localhost';
\quit
```

PostgreSQL escuchará solo en la propia VM. Ahora abre su archivo de acceso, cuya ubicación
depende de la versión instalada:

```sh
nexo_hba=$(sudo -u postgres psql -Atc 'SHOW hba_file')
sudo nano "$nexo_hba"
```

Añade estas líneas **al principio**, antes de las reglas existentes:

```text
hostnossl nexo all                 127.0.0.1/32 reject
hostssl   nexo nexo_owner,nexo_app 127.0.0.1/32 scram-sha-256
hostnossl nexo all                 ::1/128      reject
hostssl   nexo nexo_owner,nexo_app ::1/128      scram-sha-256
```

La primera regla coincidente decide el acceso: se rechazan conexiones sin TLS a esa base por
loopback y se permite a las dos cuentas autenticarse con contraseña mediante TLS.
Conserva las reglas `local` existentes para administrar PostgreSQL con `sudo -u postgres psql`.

En nano, guarda con `Ctrl+O`, confirma con Enter y sal con `Ctrl+X`. Reinicia:

```sh
sudo systemctl restart postgresql
sudo -u postgres psql -c 'SHOW ssl;'
```

Debe mostrar `on`. Si PostgreSQL no arranca, consulta el servicio de la instancia con
`pg_lsclusters` y sus registros antes de continuar.

## 6. Comprobar la conexión verificada

Prueba con el usuario Linux que ejecutará Nexo:

```sh
sudo -u nexo psql \
  "host=127.0.0.1 port=5432 dbname=nexo user=nexo_app sslmode=verify-full sslrootcert=/etc/nexo/certs/postgres-ca.crt" \
  -W -c 'SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid();'
```

Introduce la contraseña de `nexo_app`. Debe devolver `ssl = t` y una versión de TLS.
Repite sustituyendo `user=nexo_app` por `user=nexo_owner` y usa su contraseña.
Si falla cualquiera de las dos conexiones, corrígela antes de iniciar Nexo.

## 7. Conectar Nexo y continuar con su instalación

Vuelve al [paso 6 del manual de Ubuntu](servidor-ubuntu.md#6-configurar-conexiones-y-credenciales).
En la plantilla de `/etc/nexo/nexo.env`, usa estas conexiones y conserva las demás variables:

```ini
ENGRAM_DATABASE_URL='postgres://nexo_app:<PASSWORD_APP>@127.0.0.1:5432/nexo?sslmode=verify-full&sslrootcert=/etc/nexo/certs/postgres-ca.crt'
NEXO_MIGRATION_DATABASE_URL='postgres://nexo_owner:<PASSWORD_OWNER>@127.0.0.1:5432/nexo?sslmode=verify-full&sslrootcert=/etc/nexo/certs/postgres-ca.crt'
NEXO_DB_REQUIRE_TLS=true
```

Sustituye los marcadores por las contraseñas reales. Si contienen caracteres reservados de URL,
codifícalos como porcentaje. No compartas este archivo ni lo guardes en Git.

Para usar la búsqueda por significado con Gemini, añade en ese mismo archivo:

```ini
GEMINI_API_KEY=<TU_CLAVE_PRIVADA>
NEXO_KB_EMBEDDER=gemini
NEXO_KB_EMBED_MODEL=gemini-embedding-001
NEXO_KB_EMBED_DIMS=768
```

Sustituye `<TU_CLAVE_PRIVADA>` por tu clave sin compartirla. Nexo necesita salida HTTPS hacia
la API de Gemini: envía las secciones y consultas al proveedor y consume su cuota. pgvector guarda
los vectores en tu PostgreSQL; Gemini los calcula. El reordenamiento es una opción independiente.
Las dimensiones, otros proveedores y las opciones de reordenamiento están en el
[manual de Ubuntu](servidor-ubuntu.md#61-elegir-búsqueda-y-reordenamiento).

Completa las credenciales y el paso 7 del manual para crear e iniciar `nexo.service`.
Systemd carga el archivo de entorno y ejecuta `/opt/nexo/engram cloud serve`; lanzar ese comando
manualmente no carga por sí solo `/etc/nexo/nexo.env`.

Si Nexo ya estaba iniciado, aplica los cambios con:

```sh
sudo systemctl restart nexo
```

Comprueba el avance del índice en **Estado de la búsqueda** del panel. Puede tardar en procesar
los documentos existentes; tener la extensión instalada no significa que el índice ya esté listo.

## Cuando la base esté en otro servidor

Esta práctica usa TCP, contraseñas y TLS verificado, pero no reproduce la red entre dos equipos.
Para una base externa:

1. Prepara las dos cuentas, la base y sus extensiones en el destino.
2. Migra los datos si quieres conservarlos; cambiar la URL no los copia.
3. Configura acceso de red desde la VM y usa el host y la CA que indique el administrador o proveedor.
4. Actualiza las dos conexiones de Nexo y el archivo CA, manteniendo `sslmode=verify-full`.
5. Reinicia con `sudo systemctl restart nexo` y comprueba sus registros.

El certificado de este laboratorio no sirve para otro nombre o IP. Si administras tú el nuevo
PostgreSQL, emite uno con ese nombre en `subjectAltName`. Con una base administrada, usa sus
certificados; no instales allí la CA de este laboratorio. El ejecutable de Nexo puede ser el mismo.

Referencias: [PostgreSQL en Ubuntu](https://documentation.ubuntu.com/server/how-to/databases/install-postgresql/index.html),
[repositorio oficial y PostgreSQL 18](https://www.postgresql.org/download/linux/ubuntu/),
[instalación de pgvector](https://github.com/pgvector/pgvector#apt),
[TLS del servidor](https://www.postgresql.org/docs/current/ssl-tcp.html),
[verificación de certificados](https://www.postgresql.org/docs/current/libpq-ssl.html) y
[reglas de acceso](https://www.postgresql.org/docs/current/auth-pg-hba-conf.html).
