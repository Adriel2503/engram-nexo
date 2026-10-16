# Publicar Nexo con Traefik y HTTPS en Ubuntu

Una misma URL sirve para el panel web y los clientes Engram. Traefik recibe las conexiones
HTTPS y las envía al servidor **`engram cloud serve`**. El certificado web de Let's Encrypt es
independiente del certificado con el que Nexo verifica PostgreSQL.

Esta guía tiene dos recorridos: reutilizar Dokploy, como hicimos en la VM de prueba, o instalar
Traefik directamente en otra VM sin Dokploy ni Docker. Usa solo el que corresponda.

## 1. Configuración realizada en la VM de prueba

| Componente | Configuración |
|---|---|
| Panel | `https://nexo-34-51-82-225.sslip.io/dashboard` |
| URL base para clientes | `https://nexo-34-51-82-225.sslip.io` |
| Servidor Nexo | Ejecutable `/opt/nexo/engram`, servicio systemd `nexo` |
| Dirección interna de Nexo | `172.17.0.1:18080` |
| Proxy existente | Contenedor `dokploy-traefik`, Traefik `v3.6.7`, puertos 80 y 443 |
| Ruta añadida | `/etc/dokploy/traefik/dynamic/nexo.yml` |
| Certificado HTTPS | Resolver existente `letsencrypt`, validación HTTP por el puerto 80 |
| Base de datos | PostgreSQL 18 nativo, puerto 5433; `unaccent` y pgvector 0.8.7 |

El puerto 5432 ya lo utiliza un PostgreSQL de Langfuse en Docker; por eso conservamos el 5433
para Nexo. No se modificaron esos otros servicios ni se reinició Traefik.

En `/etc/nexo/nexo.env` se utilizan estas variables, además de las conexiones y secretos privados:

```ini
ENGRAM_CLOUD_HOST=172.17.0.1
ENGRAM_PORT=18080
NEXO_KB_PUBLIC_URL=https://nexo-34-51-82-225.sslip.io
NEXO_TRUSTED_PROXIES=172.17.0.2/32
```

`172.17.0.1` es el puente Docker del host y `172.17.0.2` es la IP observada de Traefik en esa red.
Son valores de esta VM, no valores universales. Si recreas Traefik y cambia su IP, actualiza
`NEXO_TRUSTED_PROXIES` y reinicia Nexo. Confía solo en la dirección real del proxy.

El contenido instalado en `nexo.yml` es:

```yaml
http:
  routers:
    nexo-http:
      rule: "Host(`nexo-34-51-82-225.sslip.io`)"
      entryPoints:
        - web
      middlewares:
        - nexo-redirect-https
      service: nexo-server
    nexo-https:
      rule: "Host(`nexo-34-51-82-225.sslip.io`)"
      entryPoints:
        - websecure
      service: nexo-server
      tls:
        certResolver: letsencrypt
  middlewares:
    nexo-redirect-https:
      redirectScheme:
        scheme: https
        permanent: true
  services:
    nexo-server:
      loadBalancer:
        passHostHeader: true
        servers:
          - url: "http://172.17.0.1:18080"
```

Dokploy ya tenía configurado el proveedor de archivos con `watch: true` sobre esa carpeta.
Traefik cargó la ruta y obtuvo el certificado automáticamente. No montamos la configuración
privada de Nexo dentro del contenedor.

**Comprobado el 2026-10-15:** estado público `ready`, redirección HTTP a HTTPS con código 301,
`/dashboard` redirige al inicio de sesión con 303 y `/dashboard/login` responde 200.
El certificado presentado es de Let's Encrypt y fue verificado sin omitir la validación TLS.
Esto verifica publicación y acceso al login; no se inició sesión ni se probó un cliente con token.

## 2. Otra VM: requisitos previos

Para instalar Traefik manualmente, sin Dokploy ni Docker:

- Completa los pasos 1 a 7 del [manual de Ubuntu](servidor-ubuntu.md).
- Ten una IP pública estable y un dominio apuntando a ella. Para una prueba puedes usar
  `nexo-IP-CON-GUIONES.sslip.io`, sustituyendo la IP por la de esa VM.
- Permite TCP 80 y 443 en el firewall de la nube y en Ubuntu. El puerto 80 debe ser accesible
  desde Internet para la validación HTTP de Let's Encrypt.
- Comprueba que ningún otro proxy ocupe esos puertos:

```sh
sudo ss -ltnp | grep -E ':(80|443)\b'
```

Si ya existe un proxy, reutilízalo; no instales otro sobre los mismos puertos.
Si UFW está activo, permite `80/tcp` y `443/tcp` según la política de tu VM; conserva el acceso SSH.
No necesitas publicar el puerto interno 18080 ni PostgreSQL para dar acceso a los clientes.

Los ejemplos siguientes usan `nexo.ejemplo.com`: reemplázalo por tu dominio real en todos
los archivos. Este recorrido manual está contrastado con la documentación oficial, pero no
se ha ejecutado de punta a punta en una segunda VM.

## 3. Instalar el ejecutable de Traefik

Ejecuta como tu usuario normal. La versión de referencia es `3.7.15`, disponible al escribir esta
guía; puedes revisar las [versiones oficiales](https://github.com/traefik/traefik/releases).
No actualiza el Traefik existente de Dokploy.

```sh
sudo apt update
sudo apt install curl ca-certificates

nexo_traefik_version=3.7.15
case "$(uname -m)" in
  x86_64) nexo_traefik_arch=amd64 ;;
  aarch64) nexo_traefik_arch=arm64 ;;
  *) echo 'Arquitectura no cubierta por esta guía'; exit 1 ;;
esac

nexo_traefik_tmp=$(mktemp -d)
cd "$nexo_traefik_tmp"
nexo_traefik_archive="traefik_v${nexo_traefik_version}_linux_${nexo_traefik_arch}.tar.gz"
nexo_traefik_release="https://github.com/traefik/traefik/releases/download/v${nexo_traefik_version}"

curl -fLO "$nexo_traefik_release/$nexo_traefik_archive"
curl -fLO "$nexo_traefik_release/traefik_v${nexo_traefik_version}_checksums.txt"
sha256sum --check --ignore-missing "traefik_v${nexo_traefik_version}_checksums.txt" &&
tar -xzf "$nexo_traefik_archive" traefik &&
sudo install -m 0755 traefik /usr/local/bin/traefik

/usr/local/bin/traefik version
```

Si falla la descarga o la comprobación, detente. Crea el usuario y las carpetas una sola vez:

```sh
sudo useradd --system --user-group --home-dir /var/lib/traefik --shell /usr/sbin/nologin traefik
sudo install -d -m 0750 -o root -g traefik /etc/traefik
sudo install -d -m 0750 -o root -g traefik /etc/traefik/dynamic
sudo install -d -m 0700 -o traefik -g traefik /var/lib/traefik
```

## 4. Configurar Traefik y la ruta de Nexo

Abre la configuración principal:

```sh
sudo nano /etc/traefik/traefik.yml
```

Pega esto, sustituyendo el correo por uno tuyo para la cuenta de Let's Encrypt:

```yaml
entryPoints:
  web:
    address: ":80"
  websecure:
    address: ":443"
providers:
  file:
    directory: /etc/traefik/dynamic
    watch: true
certificatesResolvers:
  letsencrypt:
    acme:
      email: tu-correo@ejemplo.com
      storage: /var/lib/traefik/acme.json
      httpChallenge:
        entryPoint: web
log:
  level: INFO
```

Traefik guardará su cuenta y certificados en `acme.json` y gestionará la renovación. Conserva
ese archivo entre actualizaciones y protégelo: contiene claves privadas. No lo publiques en Git.
Esta configuración no habilita el dashboard administrativo de Traefik.

Ahora crea la ruta:

```sh
sudo nano /etc/traefik/dynamic/nexo.yml
```

Copia el YAML de la sección 1 de esta guía y haz dos sustituciones:

- Cambia las dos apariciones de `nexo-34-51-82-225.sslip.io` por tu dominio real.
- Cambia `http://172.17.0.1:18080` por **`http://127.0.0.1:18080`**.

En esta segunda VM Traefik y Nexo son procesos nativos de Ubuntu, por lo que sí comparten
loopback. No necesitas una red Docker ni sus direcciones 172.17.x.x.

## 5. Ajustar Nexo para el proxy nativo

Edita el archivo existente, sin borrar las conexiones ni los secretos:

```sh
sudo nano /etc/nexo/nexo.env
```

Deja una sola definición de cada variable, en líneas separadas:

```ini
ENGRAM_CLOUD_HOST=127.0.0.1
ENGRAM_PORT=18080
NEXO_KB_PUBLIC_URL=https://nexo.ejemplo.com
NEXO_TRUSTED_PROXIES=127.0.0.1/32
```

Reinicia Nexo y comprueba el destino interno:

```sh
sudo systemctl restart nexo
curl -fsS http://127.0.0.1:18080/nexo/health/ready
```

Debe devolver `{"status":"ready"}` antes de seguir.

## 6. Iniciar Traefik como servicio

```sh
sudo nano /etc/systemd/system/traefik.service
```

Contenido:

```ini
[Unit]
Description=Traefik HTTPS para Nexo
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=traefik
Group=traefik
ExecStart=/usr/local/bin/traefik --configFile=/etc/traefik/traefik.yml
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
UMask=0077

[Install]
WantedBy=multi-user.target
```

La capacidad indicada permite que el usuario `traefik` atienda los puertos 80 y 443 sin ejecutar
todo el servicio como root. Inicia y consulta:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now traefik
sudo systemctl status traefik --no-pager
sudo journalctl -u traefik -n 50 --no-pager
```

Los cambios de rutas en `dynamic/` se cargan automáticamente. Si cambias `traefik.yml`, reinicia
con `sudo systemctl restart traefik`.

## 7. Comprobar HTTPS y conectar clientes

Sustituye el dominio y ejecuta desde tu PC o desde la VM:

```sh
curl -fsS https://nexo.ejemplo.com/nexo/health/ready
curl -I http://nexo.ejemplo.com/dashboard
curl -I https://nexo.ejemplo.com/dashboard/login
```

Comprueba `ready`, redirección a HTTPS e inicio de sesión accesible. La emisión inicial del
certificado puede tardar; si no funciona, revisa DNS, acceso al puerto 80 y registros de Traefik.
No uses `curl -k` como solución: oculta errores de certificado.

| Uso | Dirección |
|---|---|
| Panel web | `https://nexo.ejemplo.com/dashboard` |
| URL base del cliente Engram | `https://nexo.ejemplo.com` |
| `NEXO_KB_PUBLIC_URL` del servidor | `https://nexo.ejemplo.com` |

Cada cliente necesita además su token. El proxy no crea usuarios ni concede permisos.
Si el dominio, la IP o el proxy cambian, revisa DNS, rutas y las variables de Nexo correspondientes.

## 8. Primer acceso al panel

Si el panel pide token y todavía no creaste un administrador, es normal: una base nueva no trae
usuarios ni tokens personales predeterminados. Ejecuta en la VM, una sola vez:

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

Consulta el resultado de forma privada:

```sh
sudo nano /root/nexo-primer-acceso.txt
```

Copia el token personal en el formulario del panel; no lo compartas ni lo publiques en Git.
No uses el secreto interno `ENGRAM_CLOUD_TOKEN` como token personal. Si ya existe un administrador
activo, bootstrap rechaza crear otro; usa el acceso existente y el procedimiento administrativo.
Más detalles en el [paso 9 de Ubuntu](servidor-ubuntu.md#9-crear-el-primer-usuario-y-entrar).

Referencias: [rutas por archivo](https://doc.traefik.io/traefik/reference/dynamic-configuration/file/),
[certificados ACME](https://doc.traefik.io/traefik/reference/install-configuration/tls/certificate-resolvers/acme/),
[dominios en Dokploy](https://docs.dokploy.com/docs/core/domains) y
[versiones de Traefik](https://github.com/traefik/traefik/releases).
