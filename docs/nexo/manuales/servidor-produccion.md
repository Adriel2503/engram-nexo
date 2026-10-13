# Manual de despliegue en producción del servidor Nexo IA

| | |
|---|---|
| Versión de Nexo | `3.4.0-nexo.2` (etiqueta `v3.4.0-nexo.2`, base Engram v3.4.0) |
| Imagen | `ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2` (también `latest`), amd64 y arm64 |
| Para quién | La persona que instala y opera el servidor de Nexo IA en Real Plaza |
| Fecha | 2026-10-13 |

Este manual explica cómo poner en producción el servidor de Nexo IA (`engram cloud serve` del fork
`engram-nexo`), cómo respaldarlo, actualizarlo y vigilarlo. Reutiliza y amplía la guía técnica
[`docs/nexo/seguridad/guia-despliegue.md`](../seguridad/guia-despliegue.md): si algo de este manual
no está claro, esa guía tiene el detalle de cada control de seguridad.

Documentos relacionados:

| Documento | Para qué |
|---|---|
| [`administracion.md`](administracion.md) | Trabajo diario del administrador y de los responsables de espacios |
| `instalacion-dev.md` (en esta carpeta) | Conectar la PC de un desarrollador |
| `servidor-local.md` (en esta carpeta) | Levantar un servidor de prueba en una sola máquina |
| [`NEXO_CHANGES.md`](../../../NEXO_CHANGES.md) | Qué cambia el fork respecto de Engram, migraciones y publicación de versiones |

En los ejemplos, `<dominio>` es el nombre DNS del servidor (por ejemplo `nexo.empresa.interno`),
`<token>` es un token de acceso y `<contraseña>` es un secreto. Nunca escriba valores reales en
archivos versionados, en tickets ni en chats.

---

## 1. Arquitectura

```text
  PCs del equipo (Engram + Nexo)        Navegador (panel)
             |                                  |
             +---------------- HTTPS 443 -------+
                               |
                   +-----------v------------+
                   |  proxy: Caddy 2.8      |   único puerto publicado: 443
                   |  TLS, X-Forwarded-For  |   IP fija 192.168.240.10
                   +-----------+------------+
                               | HTTP interno :18080
                   +-----------v------------+
                   |  cloud: engram cloud   |   imagen nexo-engram-cloud
                   |  serve (usuario 10001) |   IP fija 192.168.240.5
                   |  volumen /export       |   exportación firmada de auditoría
                   +-----+-------------+----+
     ENGRAM_DATABASE_URL |             | NEXO_MIGRATION_DATABASE_URL
     (nexo_app: opera)   |             | (nexo_owner: solo al iniciar, migra)
                   +-----v-------------v----+
                   |  Postgres 18 (pgvector)|   sin puertos publicados
                   |  base engram_cloud     |   red interna del compose
                   +------------------------+
```

| Componente | Qué hace |
|---|---|
| Caddy | Termina HTTPS y reenvía al servidor. Agrega `X-Forwarded-For`; el servidor solo lo cree si la conexión viene de `NEXO_TRUSTED_PROXIES` |
| Servidor `cloud` | Sincronización de memorias (`/sync/*`), panel (`/dashboard`), administración (`/admin/*`, `/nexo/admin/*`, `/nexo/authz/v1`) y conocimiento (`/nexo/kb/v1`) |
| Postgres | Una sola base `engram_cloud` con dos cuentas: `nexo_owner` (dueña del esquema, solo migra) y `nexo_app` (operación diaria, sin poder alterar la evidencia) |
| `postgres` (superusuario) | Solo para administrar la base. El servidor nunca lo usa |

## 2. Requisitos

| Requisito | Detalle |
|---|---|
| Servidor | Linux con Docker Engine y Docker Compose v2. Plataforma amd64 o arm64 |
| Postgres | 18, imagen oficial `pgvector/pgvector:pg18` (la del compose de ejemplo; hasta `3.4.0-nexo.2` el ejemplo usaba 16, la migración está en 9.5) o un servicio administrado compatible, 16 o posterior (sección 8) |
| Extensión `unaccent` | Opcional. Si existe o se puede crear, la búsqueda ignora tildes en la base; si no, las quita la aplicación y la migración sigue con un aviso. En Postgres 13 o posterior es una extensión "trusted": basta con que `nexo_owner` sea dueña de la base. Para no usarla: `NEXO_KB_DISABLE_UNACCENT=1` antes del primer arranque |
| Extensión `pgvector` | Opcional. La usa la búsqueda por significado (R2, sección 4.6.1); sin ella, el servidor busca solo por texto. La imagen `pgvector/pgvector:pg18` ya la trae. Si elige un Postgres administrado, conviene que la ofrezca (0.8 o posterior) |
| Dominio | Un nombre DNS interno que resuelva a la IP del servidor |
| Certificado | Certificado del dominio emitido por la CA corporativa (recomendado) o la CA interna de Caddy (`tls internal`, solo si todas las PCs confían en esa CA). El cliente Engram exige HTTPS cuando usa token |
| Red | Puerto 443 abierto desde la red de las PCs. Salida HTTPS hacia el webhook de Slack o Teams si se usan alertas |
| Acceso a la imagen | El repositorio es privado: `docker login ghcr.io` con un token de GitHub con permiso `read:packages` |
| Almacenamiento de respaldos | Un destino fuera del host para los respaldos de Postgres y para la carpeta de exportación de auditoría |

## 3. Obtener la imagen

```sh
# El token de GitHub (permiso read:packages) se lee de un archivo que no se versiona.
docker login ghcr.io -u <usuario-github> --password-stdin < ~/ghcr-token.txt
docker pull ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2
docker run --rm ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2 version
# Debe mostrar: engram 3.4.0-nexo.2
```

Use siempre la etiqueta exacta (`3.4.0-nexo.2`), no `latest`: así sabe qué versión corre y puede
volver atrás (sección 10).

## 4. Variables de entorno

Los valores por defecto de esta sección salen del código de la versión `3.4.0-nexo.2`. "Obligatoria"
significa que sin ella el servidor no arranca o queda inseguro para producción. La columna
"Compose" indica el valor que trae `deploy/nexo/compose.example.yaml`.

### 4.1 Base de datos

| Variable | Obligatoria | Por defecto (código) | Compose | Qué hace |
|---|---|---|---|---|
| `ENGRAM_DATABASE_URL` | Sí | `postgres://engram:engram_dev@localhost:5433/engram_cloud?sslmode=disable` (solo desarrollo) | `postgres://nexo_app:${NEXO_APP_PASSWORD}@postgres:5432/engram_cloud?sslmode=disable` | Cuenta de aplicación (`nexo_app`). Todas las peticiones. No migra |
| `NEXO_MIGRATION_DATABASE_URL` | Sí en producción | vacía: una sola cuenta y aviso `seguridad degradada` | `postgres://nexo_owner:${NEXO_OWNER_PASSWORD}@postgres:5432/engram_cloud?sslmode=disable` | Cuenta dueña (`nexo_owner`). Solo al iniciar: migraciones, privilegios y funciones de retención. Si la cuenta de aplicación es superusuario, dueña de objetos o puede crear objetos, el servidor no arranca |
| `NEXO_DB_APP_ROLE` | No | el usuario de `ENGRAM_DATABASE_URL` | no se usa | Rol que recibe los privilegios cuando no coincide con el usuario de la URL (por ejemplo, detrás de un pooler) |
| `NEXO_KB_DISABLE_UNACCENT` | No | vacía | no se usa | `1` o `true`: no usar la extensión `unaccent`. Solo tiene efecto en la primera migración del conocimiento (luego el modo queda fijo) |

Variables del contenedor `postgres` (solo las usa `postgres-init/01-cuentas.sh` la primera vez que
se crea el volumen): `POSTGRES_PASSWORD`, `NEXO_OWNER_PASSWORD`, `NEXO_APP_PASSWORD` (obligatorias),
`NEXO_DB_NAME` (por defecto `engram_cloud`), `NEXO_OWNER_ROLE` (`nexo_owner`), `NEXO_APP_ROLE`
(`nexo_app`).

### 4.2 Secretos y autenticación

| Variable | Obligatoria | Por defecto | Qué hace |
|---|---|---|---|
| `ENGRAM_CLOUD_TOKEN` | Sí | vacía: el servidor no arranca (`cloud auth token is required`) | Token compartido de Engram. Ponga un valor aleatorio largo y **no lo entregue a nadie**: lo que llegue con él queda como `no-identificado` y no tiene roles en Nexo |
| `ENGRAM_JWT_SECRET` | Sí | vacía: no arranca con autenticación. Rechaza el valor de desarrollo | Firma las sesiones del panel. Mínimo 32 caracteres |
| `ENGRAM_CLOUD_TOKEN_PEPPER` | Sí en la práctica | vacía: no se pueden emitir ni validar tokens asignados | Secreto con el que se guardan los tokens de las personas. Mínimo 32 bytes y distinto de `ENGRAM_JWT_SECRET`. Si cambia, todos los tokens emitidos dejan de funcionar |
| `ENGRAM_CLOUD_ADMIN` | No | vacía | Token de administrador heredado de Engram. Déjela vacía: en Nexo ese administrador no tiene roles (403 en `/nexo/*`) |
| `ENGRAM_CLOUD_INSECURE_NO_AUTH` | No | vacía | Nunca en producción. Desactiva la autenticación (no se puede usar junto con `ENGRAM_CLOUD_TOKEN`) |

### 4.3 Red, proyectos y proxies de confianza

| Variable | Obligatoria | Por defecto | Compose | Qué hace |
|---|---|---|---|---|
| `ENGRAM_CLOUD_HOST` | Sí en contenedor | `127.0.0.1` | `0.0.0.0` | Interfaz donde escucha el servidor |
| `ENGRAM_PORT` | No | `8080` | `18080` | Puerto interno. Si lo cambia, cambie también `reverse_proxy cloud:18080` en el Caddyfile |
| `ENGRAM_CLOUD_ALLOWED_PROJECTS` | Sí | vacía: el servidor no arranca (`cloud project allowlist is required`) | `nexo-transversal` | Proyectos de memoria que acepta el servidor, separados por comas |
| `ENGRAM_CLOUD_MAX_PUSH_BYTES` | No | `8388608` (8 MiB) | no se usa | Tamaño máximo de una subida de sincronización |
| `NEXO_TRUSTED_PROXIES` | Sí detrás de Caddy | vacía: se registra la IP de la conexión y se ignora `X-Forwarded-For` | `192.168.240.10/32` (Caddy) | IP o CIDR del proxy, separados por comas o espacios. Sin ella todas las peticiones parecen venir del proxy y comparten límites y bloqueos. Con ella, los enlaces de las citas toman el esquema y el host públicos de `X-Forwarded-Proto` y `X-Forwarded-Host` (solo desde esos proxies) |
| `NEXO_SUPERADMIN_ALLOWED_CIDRS` | Recomendada | vacía: sin restricción y aviso al iniciar | no se usa | Redes (CIDR o IP, separadas por comas) desde las que un superadmin puede administrar admins y la configuración de la plataforma. Fuera de ellas: 403 auditado (`reason=network`). Si tiene valor pero ninguna red válida, se niega todo |
| `NEXO_KB_PUBLIC_URL` | Recomendada | vacía: esquema y host de la petición; detrás de un proxy de `NEXO_TRUSTED_PROXIES`, los que informa el proxy | no se usa | URL pública (por ejemplo `https://<dominio>`) para los enlaces de las citas; si está, manda. Detrás de Caddy, sin esta variable ni `NEXO_TRUSTED_PROXIES`, los enlaces salen con `http://` porque el servidor no ve el TLS |

### 4.4 Límites y bloqueos

| Variable | Por defecto | Qué hace |
|---|---|---|
| `NEXO_RATE_LIMITS` | vacía: `sync` 600/600, `admin` 120/120, `login` 20/-, `nexo` 300/300, `other` 600/- (por IP / por token, por minuto) | JSON con los grupos a cambiar, por ejemplo `{"sync":{"ip":1200}}`. `0` = sin límite en esa dimensión. `off` desactiva el límite. JSON inválido: valores por defecto con aviso |
| `NEXO_AUTH_LOCKOUT` | vacía: 20 tokens inválidos distintos desde una IP en 300 s bloquean la IP 900 s; sin bloqueo por persona | JSON `{"max_failures":20,"user_max_failures":0,"window_seconds":300,"lockout_seconds":900}` u `off`. No se recomienda activar `user_max_failures` (un token viejo podría dejar a la persona sin acceso) |

Grupos de rutas: `sync` = `/sync/*`; `admin` = `/admin/*`, `/nexo/admin/*`, `/nexo/authz/*`;
`login` = `POST /dashboard/login` y `/dashboard/bootstrap`; `nexo` = resto de `/nexo/*`; `other` =
panel, archivos estáticos y `/health`. Los límites son por réplica. Con muchas PCs detrás de un mismo
NAT, suba `sync.ip` (600 por minuto alcanza para unas 100 PCs con sincronización automática).

### 4.5 Tokens

| Variable | Por defecto | Qué hace |
|---|---|---|
| `NEXO_TOKEN_MAX_DAYS` | `365` (válido de 1 a 36500) | Caducidad máxima al emitir un token. Los tokens se emiten por defecto con 90 días (o con el máximo, si es menor) |

### 4.6 Conocimiento (`NEXO_KB_*`, servidor)

| Variable | Por defecto | Qué hace |
|---|---|---|
| `NEXO_KB_MAX_DOC_BYTES` | `2097152` (2 MB) | Tamaño máximo de un documento convertido |
| `NEXO_KB_MAX_BATCH` | `200` | Documentos por pedido de publicación |
| `NEXO_KB_MAX_REQUEST_BYTES` | `33554432` (32 MB) | Cuerpo máximo de una petición JSON a `/nexo/kb/v1` (413 si se supera) |
| `NEXO_KB_UPLOAD_MAX_BYTES` | `268435456` (256 MB) | Cuerpo máximo de una subida de archivos |
| `NEXO_KB_UPLOAD_MAX_FILES` | `2000` | Archivos por subida |
| `NEXO_KB_UPLOAD_MAX_FILE_BYTES` | `20971520` (20 MB) | Tamaño máximo por archivo |
| `NEXO_KB_UPLOAD_MAX_UNZIPPED` | `67108864` (64 MB) | Suma de lo descomprimido de un zip |
| `NEXO_KB_UPLOAD_MAX_ENTRIES` | `10000` | Entradas de un zip |
| `NEXO_KB_PRUNE_MAX_PERCENT` | `25` | Porcentaje máximo de documentos que una poda retira sin confirmación |
| `NEXO_KB_PRUNE_MIN_DOCS` | `5` | Hasta esta cantidad de documentos retirados no se aplica el porcentaje |
| `NEXO_KB_RANK_CANDIDATES` | `20000` (mínimo 100) | Secciones candidatas por búsqueda |
| `NEXO_KB_R1` | vacía: todas las mejoras de búsqueda | `off`, `all` o lista (`doc_context,split_tables,synonyms,threshold,diversity,quote`). Cambiar `doc_context` exige reindexar (sección 10.3) |
| `NEXO_KB_MIN_COVERAGE` | `0.3` | Umbral para responder "no existe un estándar oficial" |
| `NEXO_KB_MAX_PER_DOC` | `2` | Secciones de un mismo documento por respuesta |
| `NEXO_KB_PUBLIC_URL` | vacía | Ver 4.3 |
| `NEXO_KB_DISABLE_UNACCENT` | vacía | Ver 4.1 |

### 4.6.1 Búsqueda por significado (R2, opcional)

La búsqueda híbrida combina palabras y significado. Con ella el asistente encuentra un estándar
aunque la pregunta use otras palabras: en la wiki de arquitectura, las paráfrasis pasaron de 0 % a
57 % y el acierto total de 76 % a 93 % (`docs/nexo/conocimiento/design.md` §20).

Es **opcional**. Sin proveedor configurado, o sin la extensión pgvector, el servidor funciona igual
que antes, solo con búsqueda por texto, y sin errores.

**Requisitos.**

| Qué | Detalle |
|---|---|
| Extensión `vector` (pgvector) | Versión 0.8 o superior, que permite el recorrido filtrado (`iterative_scan`). Con el compose de ejemplo, use la imagen `pgvector/pgvector:pg18`: `postgres-init/01-cuentas.sh` crea la extensión con el superusuario. En un Postgres administrado, un DBA ejecuta `CREATE EXTENSION IF NOT EXISTS vector;` en la base `engram_cloud` antes del primer arranque. La cuenta dueña no puede crearla, porque pgvector no es una extensión "trusted". Para ver la versión disponible: `SELECT * FROM pg_available_extension_versions WHERE name = 'vector';` |
| Proveedor de embeddings | Gemini, OpenAI, Azure OpenAI o un servidor compatible con OpenAI (Ollama, vLLM, TEI) |
| Política de datos | Cada sección publicada (con su título y su ruta) y cada pregunta se envían al proveedor. Hace falta la aprobación de Real Plaza antes de activarlo en producción |
| Salida a internet | Desde el contenedor `cloud` hacia la API del proveedor (Gemini: `generativelanguage.googleapis.com`; OpenAI: `api.openai.com`; Azure: `<recurso>.openai.azure.com`) |

**Variables.**

| Variable | Por defecto | Qué hace |
|---|---|---|
| `NEXO_KB_EMBEDDER` | `off` | `gemini`, `openai`, `azure-openai`, `openai-compatible` u `off` |
| `NEXO_KB_EMBED_API_KEY` o `NEXO_KB_EMBED_API_KEY_FILE` | — | Clave del proveedor, o un archivo con ella (secreto de Docker). También se aceptan `GEMINI_API_KEY`, `OPENAI_API_KEY` y `AZURE_OPENAI_API_KEY`. Nunca se imprime ni se guarda en la base |
| `NEXO_KB_EMBED_MODEL` | `gemini-embedding-001` (gemini) o `text-embedding-3-small` (openai y azure) | Modelo. En Azure, es el modelo que hay detrás del despliegue |
| `NEXO_KB_EMBED_DIMS` | 768 (gemini) o la nativa del modelo | Dimensión de los vectores. Cambiarla, o cambiar el proveedor o el modelo, crea un índice nuevo (ver abajo) |
| `NEXO_KB_EMBED_URL` | URL pública del proveedor | Obligatoria en `azure-openai` (`https://<recurso>.openai.azure.com`) y en `openai-compatible` |
| `NEXO_KB_EMBED_DEPLOYMENT`, `NEXO_KB_EMBED_API_VERSION` | —, `2024-10-21` | Solo en Azure: nombre del despliegue y versión de la API |
| `NEXO_KB_EMBED_AUTH` | `api-key` (azure) o `bearer` | `api-key`, `bearer` (por ejemplo, un token de Entra ID) o `none` |
| `NEXO_KB_EMBED_RPM`, `NEXO_KB_EMBED_TPM` | sin límite | Límites propios por minuto (llamadas y tokens estimados). Sin ellos, un 429 del proveedor se reintenta con la espera que indica |
| `NEXO_KB_MIN_SIMILARITY`, `NEXO_KB_MIN_TEXT_SIMILARITY` | `0.70`, `0.65` | Umbral de "no existe un estándar oficial" en el modo híbrido. Están calibrados para Gemini a 768 dimensiones: con otro modelo, recalíbrelos con la evaluación |
| `NEXO_KB_RRF_VECTOR_WEIGHT`, `NEXO_KB_RRF_TEXT_WEIGHT`, `NEXO_KB_RRF_K` | `0.7`, `0.3`, `60` | Pesos de la fusión |
| `NEXO_KB_VECTOR_WORKER` | activo | `off` apaga el indexador en esa réplica. Con varias réplicas pueden quedar todos activos: la cola reparte el trabajo |

La lista completa de variables está en `NEXO_CHANGES.md` §5.

**Modelos admitidos** (la configuración se valida al arrancar; un error deja el servidor solo con
texto y lo explica en el log).

| Proveedor | Modelo | Dimensiones |
|---|---|---|
| `gemini` | `gemini-embedding-001` | 128 a 3072. Por defecto 768; recomendadas 768, 1536 y 3072 |
| `gemini` | `text-embedding-004` | Hasta 768 |
| `openai`, `azure-openai` | `text-embedding-3-small` | Hasta 1536 (por defecto 1536) |
| `openai`, `azure-openai` | `text-embedding-3-large` | Hasta 3072 (por defecto 3072) |
| `openai`, `azure-openai` | `text-embedding-ada-002` | Solo 1536 |
| cualquiera | otro modelo (por ejemplo `bge-m3` en Ollama) | Hay que declarar `NEXO_KB_EMBED_DIMS`; se comprueba en cada respuesta |

Hasta 2000 dimensiones se indexa con el tipo `vector`; de 2001 a 4000, con `halfvec`. Más de 4000
no se admite.

**Ejemplo con Gemini** (en el `.env`, nunca en `compose.yaml`):

```
NEXO_KB_EMBEDDER=gemini
NEXO_KB_EMBED_API_KEY=<clave de Google AI Studio>
```

**Ejemplo con Azure OpenAI:**

```
NEXO_KB_EMBEDDER=azure-openai
NEXO_KB_EMBED_URL=https://<recurso>.openai.azure.com
NEXO_KB_EMBED_DEPLOYMENT=<despliegue>
NEXO_KB_EMBED_MODEL=text-embedding-3-small
NEXO_KB_EMBED_DIMS=1536
NEXO_KB_EMBED_API_KEY=<clave>
```

**Qué pasa al arrancar.**

1. El log dice `pgvector X disponible`.
2. Luego dice `índice vectorial vN en construcción` e `indexador de vectores en marcha`.
3. El indexador vectoriza todo en segundo plano. La wiki de arquitectura (unas 1 900 secciones)
   tarda menos de un minuto con Gemini.
4. Mientras tanto la búsqueda sigue por texto. Cuando termina, el log dice `índice vectorial vN
   activo` y la búsqueda pasa a ser híbrida.
5. Las publicaciones nuevas se vectorizan en segundos. Hasta entonces se encuentran por texto, y la
   respuesta lo avisa (`index_lag`).
6. Para ver todo esto sin leer el log, el superadmin abre en el panel **Estado de la búsqueda**
   (`/dashboard/kb/search-status`): modo por defecto, proveedor y modelo, si la clave está configurada
   (nunca se muestra), pgvector, versiones del índice, avance, secciones que fallaron y últimas
   evaluaciones. Manual de administración, sección 5.12.

**Cambiar de proveedor, modelo o dimensión.** Cambie las variables y reinicie. Se construye un
índice nuevo en segundo plano mientras la búsqueda sigue con el anterior (o solo por texto, si
cambió el proveedor). El nuevo se activa al terminar. Nunca se mezclan vectores de modelos
distintos. El estado y la reconstrucción se ven en el manual de administración (sección 5.12).

**Si el proveedor falla** (clave inválida, cuota agotada o caída), la búsqueda responde solo por
texto con `notice: "Respuesta solo por texto: ..."`, sin error. El indexador espera y reintenta
solo.

### 4.6.2 Reordenamiento de los mejores resultados (R3, opcional)

Pone primero el resultado que mejor responde la pregunta. Un modelo externo puntúa los mejores
resultados de la búsqueda y la respuesta se ordena con ese puntaje. Es **opcional e independiente**
de la búsqueda por significado: funciona con o sin proveedor de embeddings.

Por defecto está **apagado** y, aun con proveedor, solo reordena cuando la consulta lo pide
(`rerank=on`) o cuando el servidor lo activa por defecto. Sin proveedor configurado, nunca
reordena. Reordenar solo cambia el orden de lo que la búsqueda ya encontró, con permisos y umbral
ya aplicados: no agrega resultados ni cambia la respuesta "no existe un estándar oficial". Si el
proveedor falla, tarda más que el presupuesto o responde mal, la respuesta conserva el orden normal
de la búsqueda, con `rerank.status` y un `notice`, y sin error.

**Requisitos.**

| Qué | Detalle |
|---|---|
| Proveedor | Cohere Rerank (también un despliegue de Cohere en Azure AI), Jina, Voyage, un servidor TEI propio (por ejemplo `bge-reranker-v2-m3`), un servicio con la forma de Cohere en una URL propia, o un LLM: Gemini, OpenAI, Azure OpenAI o un servidor compatible con la API de chat de OpenAI |
| Política de datos | **Cada consulta reordenada envía al proveedor la pregunta y hasta `NEXO_KB_RERANK_TOP_N` pasajes** (título, ruta y texto de la sección, con un recorte de 1 500 caracteres con un LLM y de 12 000 con Cohere). Son más datos y más veces que con los embeddings. Solo salen secciones que quien busca ya puede ver. Hace falta la aprobación de Real Plaza |
| Salida a internet | Desde el contenedor `cloud` hacia la API del proveedor (Gemini: `generativelanguage.googleapis.com`; Cohere: `api.cohere.com`) |
| Costo | Con un LLM, unos 2 400 tokens de entrada y 100 a 200 de salida por consulta reordenada (TopN 20, medido en la wiki). Una consulta sin respuesta o con menos de 2 resultados no cuesta nada |
| Latencia | Se suma a cada consulta reordenada. Medido con Gemini: `gemini-3.1-flash-lite`, 1,2 s de mediana y 1,9 s en el percentil 95; `gemini-3.8-flash`, 2,9 s y 4,9 s. El presupuesto (`NEXO_KB_RERANK_BUDGET_SECONDS`, 6 s) acota el peor caso |

**Variables** (la lista completa está en `NEXO_CHANGES.md` §5 y el diseño en `design.md` §21).

| Variable | Por defecto | Qué hace |
|---|---|---|
| `NEXO_KB_RERANKER` | `off` | `cohere`, `jina`, `voyage`, `tei`, `rerank-compatible`, `gemini`, `openai`, `azure-openai`, `openai-compatible` u `off` (también `llm` con `NEXO_KB_RERANK_LLM`) |
| `NEXO_KB_RERANK_API_KEY` o `NEXO_KB_RERANK_API_KEY_FILE` | — | Clave del proveedor, o un archivo con ella. También se aceptan `COHERE_API_KEY`, `JINA_API_KEY`, `VOYAGE_API_KEY`, `GEMINI_API_KEY`, `OPENAI_API_KEY` y `AZURE_OPENAI_API_KEY`. Nunca se imprime ni se guarda en la base |
| `NEXO_KB_RERANK_MODEL` | `rerank-v3.5` (cohere), `jina-reranker-v2-base-multilingual`, `rerank-2.5` (voyage), `gemini-3.1-flash-lite` (gemini) | Modelo. Con `openai` y `openai-compatible` es obligatorio; en Azure OpenAI, el despliegue |
| `NEXO_KB_RERANK_URL` | URL pública del proveedor | Obligatoria en `tei`, `rerank-compatible`, `azure-openai` y `openai-compatible`. Para Cohere en Azure AI, la URL del despliegue (si termina en `/rerank` se usa tal cual) |
| `NEXO_KB_RERANK_AUTH` | `bearer` | `api-key` (por ejemplo, Cohere en Azure AI o Azure OpenAI), `bearer` o `none` |
| `NEXO_KB_RERANK_TOP_N` | `20` | Cuántos resultados se reordenan (2 a 50). Casi nunca hay más de 20 que pasen el umbral |
| `NEXO_KB_RERANK_BLEND` | `0.7` | Peso del reordenador frente al orden de la búsqueda (1 = solo el reordenador) |
| `NEXO_KB_RERANK_DEFAULT` | `off` | `on`: reordenar siempre que la consulta no diga otra cosa. Recomendado solo después de la medición del manual de administración (sección 5.13) |
| `NEXO_KB_RERANK_MIN_SCORE` | `0` (apagado) | Descarta pasajes con puntaje menor. Depende del modelo: no lo active sin calibrarlo |
| `NEXO_KB_RERANK_BUDGET_SECONDS` | `6` | Tiempo total que una consulta espera al reordenador |
| `NEXO_KB_RERANK_RPM`, `NEXO_KB_RERANK_CONCURRENCY` | sin límite, 4 con LLM | Límite por minuto y llamadas en paralelo. Sin límite propio, un 429 se reintenta con la espera que indica el proveedor |

**Ejemplo con Gemini** (en el `.env`, nunca en `compose.yaml`; puede ser la misma clave de los
embeddings):

```
NEXO_KB_RERANKER=gemini
NEXO_KB_RERANK_API_KEY=<clave de Google AI Studio>
```

**Ejemplo con Cohere en Azure AI:**

```
NEXO_KB_RERANKER=cohere
NEXO_KB_RERANK_URL=https://<despliegue>.<region>.models.ai.azure.com/v2/rerank
NEXO_KB_RERANK_AUTH=api-key
NEXO_KB_RERANK_API_KEY=<clave del despliegue>
```

**Probar la clave y la ruta antes de activarlo** (desde una copia del código, nunca imprime la
clave):

```
NEXO_KB_RERANK_LIVE=1 NEXO_KB_RERANKER=cohere NEXO_KB_RERANK_API_KEY=<clave> \
  go test -run TestRerankerLive -v ./internal/nexo/kb/rerank/
```

**Qué pasa al arrancar.** No hace falta reindexar nada: no hay migraciones ni índice propio. Si la
configuración es inválida, el log lo dice (`configuración del reordenamiento inválida`) y el
servidor sigue sin reordenar.

**Si el proveedor falla** (clave inválida, modelo retirado, cuota agotada o caída), la consulta
responde con el orden normal, `rerank.status = "failed"` y `notice: "Sin reordenamiento: ..."`,
sin error. Ver el log del servidor (`reordenamiento fallido`), que no incluye la clave. Un modelo
retirado se ve como un 404 del proveedor: cambie `NEXO_KB_RERANK_MODEL`.

### 4.7 Evidencia, retención y alertas

| Variable | Por defecto | Qué hace |
|---|---|---|
| `NEXO_ACTIVITY_RETENTION_DAYS` | `180` | Días que se conserva el detalle de conexiones (`nexo_activity_log`). `0` = no depurar; menos de 30 se eleva a 30 |
| `NEXO_AUDIT_DEDUP_SECONDS` | `600` | Ventana en la que los rechazos repetidos (mismo token, IP y motivo) se resumen en una fila. `0` = una fila por rechazo |
| `NEXO_AUDIT_VERIFY_MINUTES` | `60` | Cada cuántos minutos el servidor verifica la cadena de hashes de la auditoría. `0` = solo a pedido |
| `NEXO_AUDIT_EXPORT_DIR` | vacía: exportación desactivada (aviso al iniciar) | Carpeta de la exportación diaria firmada (`<dir>/<tabla>/<AAAA-MM-DD>.jsonl` y `.jsonl.sig`). En el compose: `/export` |
| `NEXO_AUDIT_EXPORT_KEY` | vacía: exportación desactivada | Clave de las firmas, 32 caracteres o más |
| `NEXO_DISABLE_INACTIVE_DAYS` | vacía: desactivado | Días sin uso tras los cuales el servidor deshabilita a una **persona** (mínimo 7; nunca el último superadmin). Las cuentas de servicio nunca se deshabilitan por inactividad |
| `NEXO_SA_STALE_DAYS` | `180` | Días sin uso tras los cuales una cuenta de servicio habilitada deja la alerta `service_account_stale` (con su nombre y su responsable, una vez por período sin uso) y la marca "sin uso"; no la deshabilita. `0` = sin aviso |
| `NEXO_RETENTION_AUDIT_DAYS` | vacía: no se depura | Retención de `cloud_auth_audit_log`. Mínimo 365 |
| `NEXO_RETENTION_AUTHZ_EVENTS_DAYS` | vacía: no se depura | Retención de `nexo_authz_events`. Mínimo 365 |
| `NEXO_RETENTION_KB_EVENTS_DAYS` | vacía: no se depura | Retención de `nexo_kb_events`. Mínimo 365. Las versiones de documentos no se depuran nunca |
| `NEXO_RETENTION_ALERTS_DAYS` | vacía: no se depura | Retención de `nexo_alerts` y sus reconocimientos. Mínimo 365 |
| `NEXO_ALERT_RULES` | vacía: reglas por defecto (tabla en 11.3) | JSON por regla con `enabled`, `threshold`, `window_seconds`, `cooldown_seconds`, `severity` (`info`, `warning`, `high`, `critical`), u `off` |

### 4.8 Webhook de alertas

| Variable | Por defecto | Qué hace |
|---|---|---|
| `NEXO_ALERT_WEBHOOK_URL` | vacía: las alertas quedan solo en la base y en la API | URL del webhook entrante de Slack o de Teams. Se envía `{"text","summary","nexo_alert":{...}}` sin detalles ni secretos, con 3 intentos (0, 2 y 10 s). La URL lleva una clave: trátela como secreto (el log solo muestra el host). `http://` se acepta con aviso |

## 5. Generar los secretos

Genere cada secreto por separado, guárdelo en el gestor de secretos de la empresa y escríbalo solo
en el archivo `.env` del servidor (que no se versiona).

| Secreto | Comando sugerido | Requisito del código |
|---|---|---|
| `POSTGRES_PASSWORD`, `NEXO_OWNER_PASSWORD`, `NEXO_APP_PASSWORD` | `openssl rand -hex 24` | Deben ser seguros para URL (por eso hexadecimal) |
| `ENGRAM_CLOUD_TOKEN` | `openssl rand -hex 32` | Valor largo que no se entrega a nadie |
| `ENGRAM_JWT_SECRET` | `openssl rand -hex 32` | 32 caracteres o más, distinto del valor de desarrollo |
| `ENGRAM_CLOUD_TOKEN_PEPPER` | `openssl rand -hex 32` | 32 bytes o más, distinto de `ENGRAM_JWT_SECRET` |
| `NEXO_AUDIT_EXPORT_KEY` | `openssl rand -hex 32` | 32 caracteres o más. Guárdela fuera de la base y fuera del alcance de quien administra Postgres |

```sh
# Ejemplo: generar los valores en un archivo temporal protegido y copiarlos al gestor de secretos.
umask 077
for v in POSTGRES_PASSWORD NEXO_OWNER_PASSWORD NEXO_APP_PASSWORD; do echo "$v=$(openssl rand -hex 24)"; done > /tmp/nexo-secretos
for v in ENGRAM_CLOUD_TOKEN ENGRAM_JWT_SECRET ENGRAM_CLOUD_TOKEN_PEPPER NEXO_AUDIT_EXPORT_KEY; do echo "$v=$(openssl rand -hex 32)"; done >> /tmp/nexo-secretos
```

Borre `/tmp/nexo-secretos` cuando los valores estén en el gestor de secretos y en el `.env`.

## 6. Despliegue con Docker Compose y Caddy

Los archivos de ejemplo están en `deploy/nexo/` del repositorio. El compose usa rutas relativas
(`./postgres-init`, `./Caddyfile.example`, `./audit-export`): trabaje dentro de esa carpeta o copie
la carpeta completa al servidor.

### 6.1 Preparar la carpeta

```sh
mkdir -p /opt/nexo && cd /opt/nexo
# Copie aquí el contenido de deploy/nexo/ del fork (compose.example.yaml, Caddyfile.example, postgres-init/).
cp compose.example.yaml compose.yaml
mkdir -p audit-export && sudo chown 10001:10001 audit-export   # usuario del contenedor cloud
chmod 600 .env 2>/dev/null || true
```

### 6.2 Crear el archivo `.env`

Junto a `compose.yaml`, con permisos `600` y fuera del control de versiones:

```sh
POSTGRES_PASSWORD=<contraseña>
NEXO_OWNER_PASSWORD=<contraseña>
NEXO_APP_PASSWORD=<contraseña>
ENGRAM_CLOUD_TOKEN=<secreto>
ENGRAM_JWT_SECRET=<secreto>
ENGRAM_CLOUD_TOKEN_PEPPER=<secreto>
NEXO_AUDIT_EXPORT_KEY=<secreto>
NEXO_ALERT_WEBHOOK_URL=<url-del-webhook>          # opcional
NEXO_DOMAIN=<dominio>
CLOUD_IMAGE=ghcr.io/adriel2503/nexo-engram-cloud:3.4.0-nexo.2
ENGRAM_CLOUD_ALLOWED_PROJECTS=nexo-transversal     # proyectos de memoria, separados por comas
```

`CLOUD_IMAGE` es importante: sin ella el compose busca la imagen local `nexo-engram-cloud:3.4.0-nexo.2`.

### 6.3 Ajustar `compose.yaml` para producción

El compose de ejemplo ya trae dos cuentas, Caddy, `NEXO_TRUSTED_PROXIES` y la exportación firmada.
Agregue en el servicio `cloud`, bajo `environment`, lo que recomienda este manual:

```yaml
      NEXO_SUPERADMIN_ALLOWED_CIDRS: "<red-de-administración>/24"   # red de administración o VPN
      NEXO_KB_PUBLIC_URL: "https://${NEXO_DOMAIN}"
      # NEXO_DISABLE_INACTIVE_DAYS: "90"        # opcional, decisión de la empresa (solo personas)
      # NEXO_SA_STALE_DAYS: "180"               # aviso de cuentas de servicio sin uso (0 = sin aviso)
      # NEXO_RETENTION_AUDIT_DAYS: "365"        # solo con una política de retención aprobada
```

Revise también:

- La subred `192.168.240.0/24` del compose: cámbiela si choca con otra red del servidor (y ajuste las
  IP fijas de `cloud` y `proxy` y `NEXO_TRUSTED_PROXIES`).
- Que el servicio `postgres` no tenga `ports:` (no debe publicarse).

### 6.4 Certificado en Caddy

El `Caddyfile.example` usa `tls internal` (CA propia de Caddy). En producción use el certificado
corporativo:

```text
{$NEXO_DOMAIN} {
	tls /certs/servidor.crt /certs/servidor.key
	reverse_proxy cloud:18080
}
```

Guárdelo, por ejemplo, como `Caddyfile` y monte el certificado en el servicio `proxy`:

```yaml
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - ./certs:/certs:ro
      - caddy-data:/data
```

Por verificar: la emisión automática de certificados públicos de Caddy (ACME) no se probó con este
compose, que solo publica el puerto 443; para un dominio interno use el certificado corporativo.

### 6.5 Levantar

```sh
cd /opt/nexo
docker compose -f compose.yaml pull
docker compose -f compose.yaml up -d
docker compose -f compose.yaml ps
docker compose -f compose.yaml logs cloud | grep "\[nexo\]"
```

Al iniciar, `01-cuentas.sh` corre una sola vez (al crear el volumen `pg18-data`): crea `nexo_owner`
(dueña de la base y del esquema `public`) y `nexo_app` (solo `CONNECT` y `USAGE`). Si cambia las
contraseñas del `.env` después, no se aplican solas (sección 12.3).

### 6.6 Qué debe decir el log

| Línea esperada | Significa |
|---|---|
| `[nexo] seguridad: dos cuentas de base de datos (dueña ..., aplicación ...)` | Dos cuentas activas. Si ve `seguridad degradada: una sola cuenta`, falta `NEXO_MIGRATION_DATABASE_URL` |
| `[nexo] seguridad: limite de peticiones: sync=600/600 admin=120/120 ...` | Límites efectivos |
| `[nexo] seguridad: bloqueo por fallos de autenticacion: ip 20 tokens distintos en 5m0s bloquean 15m0s; sin bloqueo por persona ...` | Bloqueo efectivo |
| `[nexo] seguridad: tokens nuevos vencen a los 90 dias (maximo 365, NEXO_TOKEN_MAX_DAYS)` | Caducidad de tokens |
| `[nexo] seguridad: exportacion diaria firmada en /export (clave ...)` | Exportación activa. Si dice que está desactivada, revise `NEXO_AUDIT_EXPORT_DIR` y `NEXO_AUDIT_EXPORT_KEY` |
| `[nexo] seguridad: webhook de alertas: <host>` | Webhook configurado |
| `[nexo] seguridad: verificacion de la cadena de hashes cada 1h0m0s` | Verificación periódica activa |
| Aviso de `NEXO_SUPERADMIN_ALLOWED_CIDRS` vacío | Falta restringir la red de administración |
| `[nexo] kb: extensión unaccent no disponible ...` | La búsqueda funciona; las tildes se quitan en la aplicación |

## 7. Primer arranque y superadmin

### 7.1 Crear el primer administrador

Con el servidor arriba, cree el primer administrador desde el contenedor (usa las mismas variables
del servicio):

```sh
docker compose -f compose.yaml exec cloud \
  engram cloud bootstrap admin --username <usuario> --email <correo> --issue-token laptop-<usuario>
```

- El comando se niega si ya existe un administrador habilitado.
- `--issue-token <nombre>` emite un token con 90 días de vigencia. Se muestra **una sola vez**:
  cópielo directo al gestor de secretos.
- Opcional: `--grant-project <proyecto>` (repetible) le da permiso de sincronizar ese proyecto.
- Ese primer administrador pasa a ser **superadmin** de Nexo automáticamente (es el administrador
  habilitado de menor id cuando todavía no hay superadmin).

Si el token se perdió antes de usarlo y ese administrador no tiene ningún token:

```sh
docker compose -f compose.yaml exec cloud engram cloud bootstrap recover-token --name laptop-<usuario>
# --revoke-existing reemplaza un único token de arranque que nunca se usó
```

### 7.2 Comprobar

```sh
export NEXO_URL="https://<dominio>"
read -rs NEXO_ADMIN_TOKEN    # pegue el token; no queda en el historial
curl -fsS "$NEXO_URL/health"
curl -fsS "$NEXO_URL/nexo/health/ready"
curl -fsS -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" "$NEXO_URL/nexo/authz/v1/me"
```

`/health` responde `{"service":"engram-cloud","status":"ok"}` y `/nexo/health/ready`,
`{"status":"ready"}` (sección 11.1). En `/me`, `platform.role` debe ser
`superadmin` y `token` debe mostrar `scope`, `expires_at` y `last_used_at`. Si usa `tls internal`,
agregue `--cacert <ca-de-caddy>.crt` a `curl`.

También puede entrar al panel en `https://<dominio>/dashboard/` con ese token.

### 7.3 Segundo superadmin (recomendado)

Debe existir siempre al menos un superadmin habilitado; se recomiendan dos. Desde una red incluida en
`NEXO_SUPERADMIN_ALLOWED_CIDRS`:

```sh
# 1. Crear la persona como administradora (solo un superadmin puede crear admins)
curl -fsS -X POST -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"username":"<usuario2>","email":"<correo2>","display_name":"<Nombre Apellido>","role":"admin"}' \
  "$NEXO_URL/admin/users"
# 2. Promoverla a superadmin
curl -fsS -X PUT -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"role":"superadmin"}' "$NEXO_URL/nexo/authz/v1/platform/members/<usuario2>"
# 3. Emitirle su token (90 días, escritura)
curl -fsS -X POST -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"principal":"<usuario2>","name":"laptop-<usuario2>","expires_in_days":90,"scope":"write"}' \
  "$NEXO_URL/nexo/authz/v1/tokens"
```

La respuesta del paso 3 trae `raw_token` una sola vez. Entréguelo por un canal seguro. El resto de la
administración diaria está en [`administracion.md`](administracion.md).

## 8. Postgres administrado en lugar del contenedor

Puede usar un Postgres 16 o posterior administrado (el compose de ejemplo usa 18; por ejemplo, el servicio de base de datos de la nube
corporativa). Qué cambia:

| Tema | Con el contenedor | Con Postgres administrado |
|---|---|---|
| Servicio `postgres` del compose | Sí | Se quita, junto con `depends_on` del servicio `cloud` y el volumen `pg18-data` |
| Crear cuentas y base | `01-cuentas.sh` automático | Lo ejecuta usted una vez con la cuenta administradora del servicio (abajo) |
| TLS hacia Postgres | Opcional (misma red interna, sin puertos) | Obligatorio: `sslmode=verify-full` con la CA del proveedor |
| Superusuario | `postgres`, solo para administrar | La cuenta administradora del proveedor (normalmente no es superusuario) |
| Respaldos | `pg_dump` (sección 9) | Respaldos del proveedor más `pg_dump` propio |

### 8.1 Crear las cuentas

El script es idempotente y se puede correr desde un contenedor temporal con `psql`:

```sh
cd /opt/nexo
export PGPASSWORD   # contraseña de la cuenta administradora del servicio (escríbala con read -rs)
docker run --rm \
  -e PGHOST=<host-postgres> -e PGPORT=5432 -e PGSSLMODE=verify-full -e PGSSLROOTCERT=/tls/ca.crt \
  -e PGPASSWORD -e POSTGRES_USER=<admin-del-servicio> \
  -e NEXO_OWNER_PASSWORD -e NEXO_APP_PASSWORD \
  -v "$PWD/postgres-init:/init:ro" -v "$PWD/pg-tls:/tls:ro" \
  postgres:18-alpine sh /init/01-cuentas.sh
```

Por verificar: este procedimiento no se probó contra un servicio administrado. En algunos
proveedores la cuenta administradora necesita ser miembro de `nexo_owner` para ejecutar
`CREATE DATABASE ... OWNER nexo_owner`, y la extensión `unaccent` debe estar habilitada en la
configuración del servicio. Si el script falla, cree las cuentas a mano con el SQL de
[`guia-despliegue.md` §3](../seguridad/guia-despliegue.md) y revise la documentación del proveedor.

### 8.2 URLs con TLS

En el servicio `cloud`, monte la CA del proveedor y use `verify-full` en las dos URL:

```yaml
    environment:
      ENGRAM_DATABASE_URL: postgres://nexo_app:${NEXO_APP_PASSWORD}@<host-postgres>:5432/engram_cloud?sslmode=verify-full&sslrootcert=/tls/ca.crt
      NEXO_MIGRATION_DATABASE_URL: postgres://nexo_owner:${NEXO_OWNER_PASSWORD}@<host-postgres>:5432/engram_cloud?sslmode=verify-full&sslrootcert=/tls/ca.crt
    volumes:
      - ./audit-export:/export
      - ./pg-tls:/tls:ro
```

El nombre de host de la URL debe coincidir con el certificado del servidor de base de datos. Para
comprobar el cifrado, con la cuenta `nexo_app`:
`SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid();` debe devolver `t`.

Si hay un pooler (por ejemplo PgBouncer) cuyo usuario de conexión no es `nexo_app`, defina
`NEXO_DB_APP_ROLE=nexo_app`. Por verificar: el servidor no se probó detrás de un pooler.

## 9. Respaldos y restauración

### 9.1 Qué respaldar

| Qué | Dónde | Por qué |
|---|---|---|
| Base `engram_cloud` | Postgres | Memorias, usuarios, tokens (solo sus hashes), roles, conocimiento y evidencia |
| Carpeta `audit-export/` | Volumen `/export` del servicio `cloud` | Copia firmada de la auditoría: la única defensa frente a quien administra la base |
| `.env` y secretos | Gestor de secretos | Sin el mismo `ENGRAM_CLOUD_TOKEN_PEPPER`, los tokens restaurados no funcionan; sin `NEXO_AUDIT_EXPORT_KEY`, no se pueden verificar las exportaciones |
| Caddyfile y certificados | `/opt/nexo` | Para reconstruir el proxy |

### 9.2 Respaldo diario de Postgres

Use la cuenta dueña (`nexo_owner`), que puede leer todos los objetos. El formato personalizado
(`-Fc`) permite restaurar de forma selectiva:

```sh
cd /opt/nexo
docker compose -f compose.yaml exec -T postgres \
  pg_dump -U nexo_owner -d engram_cloud -Fc > respaldo-$(date +%F).dump
# Cifrar y sacar del host (ejemplo con age):
age -r <clave-publica> -o respaldo-$(date +%F).dump.age respaldo-$(date +%F).dump && rm respaldo-$(date +%F).dump
```

Dentro del contenedor oficial, la conexión por socket local normalmente no pide contraseña. Si la
pide, agregue `-e PGPASSWORD` al `exec` con la contraseña de `nexo_owner` en el entorno.

### 9.3 Restauración completa (por ejemplo, después de perder el servidor)

1. Prepare `/opt/nexo` con el mismo `.env` (mismas contraseñas y secretos) y un volumen `pg18-data`
   vacío.
2. Levante solo la base: `docker compose -f compose.yaml up -d postgres`. El script crea las cuentas
   y la base vacía.
3. Restaure **antes** de arrancar el servidor (así los triggers de la evidencia se crean después de
   cargar los datos y no recalculan nada):

   ```sh
   age -d -i <clave-privada> respaldo-AAAA-MM-DD.dump.age > respaldo.dump
   docker compose -f compose.yaml exec -T postgres sh -c 'cat > /tmp/respaldo.dump' < respaldo.dump
   docker compose -f compose.yaml exec -T postgres sh -c '
     pg_restore -l /tmp/respaldo.dump | grep -v -E "EXTENSION( -)? vector " > /tmp/lista.txt &&
     pg_restore -U nexo_owner -d engram_cloud --exit-on-error -L /tmp/lista.txt /tmp/respaldo.dump;
     r=$?; rm -f /tmp/respaldo.dump /tmp/lista.txt; exit $r' &&
   echo "restauración correcta" && rm respaldo.dump
   ```

   El filtro `grep` quita del respaldo la extensión `vector`: la imagen `pgvector/pgvector` ya la
   creó `01-cuentas.sh` con el superusuario, y `nexo_owner` no puede cambiarle el comentario
   (`must be owner of extension vector`). Sin el filtro, `--exit-on-error` detiene la restauración.
   Con una base sin pgvector el filtro no quita nada y es inofensivo. `respaldo.dump` contiene todos los
   datos: bórrelo al terminar.

4. Levante el resto: `docker compose -f compose.yaml up -d`. Las migraciones son idempotentes y los
   privilegios se vuelven a aplicar.
5. Verifique la cadena de auditoría (11.4) y entre al panel.

Por verificar: la restauración completa no forma parte de las pruebas automatizadas. Ensáyela en un
entorno aparte antes de depender de ella y anote el tiempo que tarda.

### 9.4 Tablas de evidencia: no se modifican

Estas tablas son de solo inserción para la cuenta de aplicación y están protegidas por triggers. No
las edite ni las borre a mano, tampoco con la cuenta dueña: cualquier cambio rompe la cadena de
hashes (alerta `audit_chain_broken`) y deja de ser evidencia válida.

| Grupo | Tablas |
|---|---|
| Engram | `cloud_auth_audit_log`, `cloud_sync_audit_log`, `cloud_mutations`, `cloud_chunks` |
| Trazabilidad y roles | `nexo_activity_log`, `nexo_authz_events` |
| Seguridad (S4) | `nexo_alerts`, `nexo_alert_acks`, `nexo_audit_exports`, `nexo_access_reviews`, `nexo_audit_chain_checkpoints` y `nexo_audit_chain_anchors` (esta última la escribe solo la cuenta dueña) |
| Conocimiento | `nexo_kb_events`, `nexo_kb_versions`, `nexo_kb_publish_runs`, `nexo_kb_eval_runs` |

La única forma correcta de borrar evidencia antigua es la retención configurada (11.6), que usa
funciones seguras con mínimos y deja un ancla para que la verificación siga pasando.

### 9.5 Migrar de Postgres 16 a Postgres 18

Hasta `3.4.0-nexo.2` el compose de ejemplo usaba `pgvector/pgvector:pg16`. Desde el cambio a
Postgres 18 (`NEXO_CHANGES.md`, "Postgres 18") usa `pgvector/pgvector:pg18`. **Una instalación que ya
tiene datos no puede cambiar solo la etiqueta de la imagen**: el formato de los datos de Postgres cambia
entre versiones mayores y hay que migrarlos. El servidor Nexo no cambia (las migraciones son las
mismas); lo que cambia es la base.

**Qué cambia en la imagen de Postgres 18**

| Tema | Postgres 16 | Postgres 18 | Qué hacer |
|---|---|---|---|
| Carpeta de datos | `PGDATA=/var/lib/postgresql/data`; el volumen se montaba en esa carpeta | `PGDATA=/var/lib/postgresql/18/docker`; el volumen se monta en **`/var/lib/postgresql`** | El compose de ejemplo ya lo hace. Si monta el volumen en `/var/lib/postgresql/data`, o reutiliza el volumen de la 16 en la ruta nueva, el contenedor **se niega a iniciar** con el mensaje `in 18+, these Docker images are configured to store database data in a format which is compatible with "pg_ctlcluster"` (se comprobó: no arranca ni inicializa una base vacía en silencio) |
| Volumen | `pg-data` | `pg18-data` (nombre nuevo en el ejemplo) | El volumen de la 16 queda intacto y sirve para volver atrás |
| Sumas de verificación de páginas | Desactivadas por defecto | Activadas por defecto en las bases nuevas (`SHOW data_checksums` = `on`) | Con `pg_dump` y `pg_restore` no importa. Con `pg_upgrade` sí (abajo) |
| Contraseñas | `scram-sha-256` (valor por defecto desde la 14) | Igual; `md5` quedó en desuso y avisa | `01-cuentas.sh` guarda las contraseñas con el `password_encryption` por defecto, es decir `scram-sha-256`; no requiere cambios. Un cliente o un pooler antiguo que solo hable `md5` ya no es compatible |
| Esquema `public` | Solo lo usa su dueña desde la 15 | Igual | Sin cambios: `01-cuentas.sh` ya lo deja a nombre de `nexo_owner` y sin permisos para `PUBLIC` |
| Estadísticas del planificador | Se pierden al restaurar un respaldo | Se pierden al restaurar un respaldo de la 16 | Ejecute `vacuumdb --analyze-in-stages` después de restaurar (paso 6) |
| pgvector | 0.8.x | 0.8.x (la imagen `pg18` trae la misma serie) | Sin cambios; los índices HNSW se reconstruyen al restaurar |

**Procedimiento recomendado: `pg_dump` y `pg_restore`.** Requiere una ventana sin servicio (el
servidor `cloud` se detiene y el autosync de las PCs reintenta después). Con una base de cientos de
MB tarda minutos; el tiempo lo dominan la reconstrucción de los índices HNSW y de texto.

Se probó el 2026-10-13 de Postgres 16.15 a 18.6, con pgvector 0.8.7 en las dos, con un índice
vectorial activo de 768 dimensiones, usuarios, tokens, un espacio de conocimiento con documentos,
memorias sincronizadas y la cadena de auditoría.

1. **Detenga el servidor y haga el respaldo.** Anote los conteos de todas las tablas para comparar
   después (con el servidor detenido nadie escribe):

   ```sh
   cd /opt/nexo
   docker compose -f compose.yaml stop cloud
   docker compose -f compose.yaml exec -T postgres psql -U nexo_owner -d engram_cloud -At --no-psqlrc -F' ' > conteos-pg16.txt <<'SQL'
   SELECT format('SELECT %L, count(*) FROM public.%I', tablename, tablename) FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename \gexec
   SELECT 'ext:' || extname || ' ' || extversion FROM pg_extension ORDER BY extname;
   SELECT 'hnsw_indexes ' || count(*) FROM pg_indexes WHERE indexdef ILIKE '%hnsw%';
   SQL
   docker compose -f compose.yaml exec -T postgres pg_dump -U nexo_owner -d engram_cloud -Fc > migracion-pg16.dump
   ls -l migracion-pg16.dump        # no debe estar vacío
   ```

   Copie `migracion-pg16.dump` fuera del host y cífrelo como un respaldo diario (9.2): contiene todos
   los datos. No hace falta respaldar las cuentas con `pg_dumpall --globals-only`: `01-cuentas.sh`
   crea `nexo_owner` y `nexo_app` con las contraseñas del `.env`.

2. **Baje los contenedores sin borrar el volumen** (nunca `down -v`):

   ```sh
   docker compose -f compose.yaml down
   docker volume ls | grep pg-data      # nexo-engram_pg-data: el volumen de la 16, se conserva
   ```

3. **Actualice `compose.yaml`** con el de `deploy/nexo/compose.example.yaml` del fork, o cambie en el
   suyo tres cosas: la imagen del servicio `postgres` a `pgvector/pgvector:pg18`, el montaje a
   `- pg18-data:/var/lib/postgresql` y, al final del archivo, el volumen `pg18-data:` en lugar de
   `pg-data:`. No cambie el `.env`.

4. **Levante solo la base nueva.** El script crea las cuentas, la base vacía y la extensión `vector`:

   ```sh
   docker compose -f compose.yaml up -d postgres
   docker compose -f compose.yaml ps postgres        # esperar "healthy"
   docker compose -f compose.yaml exec -T postgres psql -U postgres -At -c "SHOW server_version" -c "SHOW data_checksums"
   ```

5. **Restaure** antes de arrancar el servidor, con el mismo procedimiento de 9.3 (paso 3, con el
   filtro de la extensión `vector`), usando `migracion-pg16.dump`. Debe terminar con salida 0 y sin
   errores.

6. **Calcule las estadísticas** del planificador (el respaldo no las trae):

   ```sh
   docker compose -f compose.yaml exec -T postgres vacuumdb -U postgres -d engram_cloud --analyze-in-stages
   ```

7. **Compare los conteos** antes de arrancar el servidor. No debe haber diferencias:

   ```sh
   docker compose -f compose.yaml exec -T postgres psql -U nexo_owner -d engram_cloud -At --no-psqlrc -F' ' > conteos-pg18.txt <<'SQL'
   SELECT format('SELECT %L, count(*) FROM public.%I', tablename, tablename) FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename \gexec
   SELECT 'ext:' || extname || ' ' || extversion FROM pg_extension ORDER BY extname;
   SELECT 'hnsw_indexes ' || count(*) FROM pg_indexes WHERE indexdef ILIKE '%hnsw%';
   SQL
   diff conteos-pg16.txt conteos-pg18.txt && echo "conteos idénticos"
   ```

8. **Arranque el servidor y verifique.** Las migraciones son idempotentes y los privilegios se
   vuelven a aplicar:

   ```sh
   docker compose -f compose.yaml up -d
   docker compose -f compose.yaml logs --since 5m cloud | grep "\[nexo\]"
   ```

   Compruebe: el log dice `dos cuentas de base de datos` y, si usa búsqueda por significado,
   `pgvector 0.8.x disponible`; `/health` y `/nexo/health/ready` responden; un token existente sigue
   valiendo (`GET /nexo/authz/v1/me`); una búsqueda devuelve resultados (con `mode=hybrid` si hay
   proveedor de embeddings); `engram nexo audit verify` termina con `"ok":true` (11.4); la PC de un
   usuario sincroniza (`engram sync --cloud`). Los vectores no se recalculan: el índice vectorial
   restaurado queda activo.

9. **Conserve el volumen de la 16 unos días** y bórrelo cuando todo esté confirmado:
   `docker volume rm nexo-engram_pg-data` (el prefijo es el `name:` del compose). Hasta entonces
   también sirve para volver atrás.

**Volver atrás.** `docker compose -f compose.yaml down`, restaure el `compose.yaml` anterior (imagen
`pg16`, volumen `pg-data`) y `up -d`. Se pierde lo escrito desde el paso 8.

**Alternativa: `pg_upgrade`** (no se probó con las imágenes del ejemplo; solo se justifica con bases
de decenas de GB, donde la restauración tarda demasiado). Tenga en cuenta:

- `pg_upgrade` necesita los binarios de la 16 **y** de la 18 en el mismo contenedor, y pgvector
  instalado para las dos versiones. Las imágenes `pgvector/pgvector:pg16` y `pg18` traen solo una:
  hay que construir una imagen propia con ambas.
- Las bases nuevas de la 18 se crean con sumas de verificación de páginas, y `pg_upgrade` se niega si
  la base vieja no las tiene. O se inicia la base nueva con `initdb --no-data-checksums`, o se
  activan en la vieja con `pg_checksums --enable` (con la base detenida) antes de migrar.
- La carpeta de datos de la 16 está en la raíz del volumen y la de la 18 en `18/docker`: hay que
  mover los datos viejos a una carpeta propia (por ejemplo `16/docker`) antes de usar `--link`, o
  copiar en lugar de enlazar.
- Después: `vacuumdb --all --analyze-in-stages` y las mismas comprobaciones de los pasos 7 y 8. Con
  `pg_upgrade` los índices no se reconstruyen: si el sistema base de la imagen (`glibc`) cambió,
  ejecute `REINDEX DATABASE engram_cloud;` con `nexo_owner`. Con las imágenes de este ejemplo (Debian
  12 en ambas) no hace falta. Con `pg_dump` y `pg_restore` todos los índices se reconstruyen.

**Postgres administrado.** Use la actualización de versión mayor que ofrece el proveedor, o cree una
instancia 18 y restaure con los mismos pasos 1, 5 a 8, apuntando `ENGRAM_DATABASE_URL` y
`NEXO_MIGRATION_DATABASE_URL` a la nueva. Verifique que la instancia ofrezca pgvector 0.8 o posterior.

## 10. Actualizar a una versión nueva de Nexo

### 10.1 Cómo se publican las versiones

Cada versión del fork lleva una etiqueta `v<versión de Engram>-nexo.<n>` en la rama `main` (por
ejemplo `v3.4.0-nexo.2`). Al subirla, GitHub construye la imagen
`ghcr.io/adriel2503/nexo-engram-cloud:<versión>` y los binarios del release. El detalle está en
`NEXO_CHANGES.md`, sección OP-01.

### 10.2 Pasos

1. Lea en `NEXO_CHANGES.md` qué cambia la versión nueva y si trae un "paso de actualización".
2. Haga un respaldo (9.2) y guárdelo fuera del host.
3. Cambie la etiqueta en `.env`:
   `CLOUD_IMAGE=ghcr.io/adriel2503/nexo-engram-cloud:<versión-nueva>`.
4. Descargue y reinicie solo el servidor:

   ```sh
   docker compose -f compose.yaml pull cloud
   docker compose -f compose.yaml up -d cloud
   docker compose -f compose.yaml logs --since 10m cloud | grep "\[nexo\]"
   ```

5. Compruebe `/health`, `/nexo/authz/v1/me`, el panel y la cadena de auditoría (11.4).

Las migraciones corren solas al iniciar, con la cuenta dueña. Son aditivas e idempotentes (solo
agregan columnas, tablas, índices, vistas, funciones y triggers; nada se renombra ni se borra) y
corren dentro de una transacción con `pg_advisory_xact_lock`: si arrancan dos réplicas a la vez, una
espera a la otra. Los privilegios se aplican después, con su propio advisory lock.

### 10.3 Reindexar el conocimiento cuando la versión lo pide

Algunas versiones (o un cambio de `NEXO_KB_R1` que toque `doc_context`) piden reindexar. Lo hace un
superadmin, desde una red permitida:

```sh
engram nexo kb reindex --status      # estado del índice
engram nexo kb reindex               # reindexado seguro
# o por API: POST /nexo/kb/v1/admin/index:reindex  (y GET /nexo/kb/v1/admin/index para el estado)
```

### 10.4 Volver atrás

1. Vuelva a poner en `CLOUD_IMAGE` la etiqueta anterior y ejecute `docker compose -f compose.yaml up -d cloud`.
2. Como las migraciones son aditivas, la versión anterior ignora las columnas y tablas nuevas.
3. Si la versión nueva ya escribió datos que la anterior no entiende, o algo falla, restaure el
   respaldo del paso 2 (9.3).

Por verificar: no hay una prueba automatizada de volver de una versión de Nexo a la anterior. Los
triggers creados por la versión nueva siguen activos en la base aunque vuelva a la imagen anterior.

## 11. Monitoreo

En los ejemplos de esta sección, `$NEXO_ADMIN_TOKEN` es un token de un auditor, admin o superadmin.
Las acciones (reconocer una alerta, registrar una revisión) exigen además un token `write`.

### 11.1 Salud

| Comprobación | Comando | Esperado |
|---|---|---|
| El proceso responde | `curl -fsS https://<dominio>/health` | `{"service":"engram-cloud","status":"ok"}` |
| El servidor está disponible (con la base) | `curl -fsS https://<dominio>/nexo/health/ready` | `200` `{"status":"ready"}`; con la base caída, `503` `{"status":"unavailable"}` |
| Contenedores | `docker compose -f compose.yaml ps` | `postgres` en `healthy`; `cloud` y `proxy` en `running` |
| La base y la autenticación responden | `curl -fsS -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" https://<dominio>/nexo/authz/v1/me` | `200` con su rol |
| Avisos del servidor | `docker compose -f compose.yaml logs --since 1h cloud \| grep "\[nexo\]"` | Sin errores de exportación, retención ni verificación |

`/health` (de Engram) solo indica que el proceso está vivo: no consulta la base y responde `ok`
aunque Postgres esté caído. Para el monitoreo externo, el balanceador o el `healthcheck` de Docker use
`GET /nexo/health/ready`: consulta la base (`SELECT 1`, 2 segundos como máximo) y responde `200`
`{"status":"ready"}` o `503` `{"status":"unavailable"}`. No pide token ni muestra datos de la base ni
del error. Cuenta en el límite del grupo `nexo` (300 por minuto por IP). Ejemplo de `healthcheck`
para el servicio `cloud` del compose (la imagen trae `wget` de busybox):

```yaml
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "-", "http://127.0.0.1:18080/nexo/health/ready"]
      interval: 30s
      timeout: 5s
      retries: 3
```

Combínelo con la consulta a `/nexo/authz/v1/me` (la autenticación) y con las alertas.

### 11.2 Alertas

```sh
curl -fsS -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" "$NEXO_URL/nexo/admin/alerts?unacked=true"
curl -fsS -X POST -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"note":"revisado: PC de <persona> con token viejo"}' "$NEXO_URL/nexo/admin/alerts/<id>:ack"
```

Filtros de `GET /nexo/admin/alerts`: `rule`, `layer` (`plataforma`, `proyectos`, `conocimiento`),
`severity`, `unacked=true`, `from`, `to`, `cursor`, `limit`. Reconocer una alerta ya reconocida
devuelve `409`. El servidor evalúa las reglas cada minuto en una sola réplica.

### 11.3 Reglas por defecto

| Regla | Dispara cuando | Umbral / ventana | Severidad |
|---|---|---|---|
| `authz_denied_burst` | Ráfaga de denegaciones de permisos | 5 en 1 min | high |
| `auth_failures_burst` | Ráfaga de fallos de autenticación | 10 en 5 min | high |
| `platform_role_changed` | Cambia un rol de plataforma | 1 | high |
| `shared_token_used` | Se usa el token compartido `ENGRAM_CLOUD_TOKEN` | 1 | warning |
| `expired_token_used` | Se usa un token vencido | 1 | info |
| `revoked_token_used` | Se usa un token revocado o de una persona deshabilitada | 1 | warning |
| `invalid_token_repeated` | Un token inválido se repite | 10 en 30 min | warning |
| `audit_chain_broken` | La verificación de la cadena falla | 1 | critical |
| `logging_stalled` | La auditoría deja de registrar (desactivada por defecto) | 30 min | high |
| `inactive_account_disabled` | El servidor deshabilitó a una persona por inactividad (`NEXO_DISABLE_INACTIVE_DAYS`) | 1 | warning |
| `service_account_stale` | Una cuenta de servicio habilitada lleva `NEXO_SA_STALE_DAYS` días sin uso (no se deshabilita); una vez por período sin uso, pasada diaria | 180 días | warning |
| `audit_export_failed` | Falla la exportación diaria | 1 | high |
| `kb_doc_expiring` | Un documento vigente vence pronto | 30 días (o `settings.expiry_warn_days` del espacio) | info |
| `kb_doc_review_due` | Un documento no se revisa en el plazo del espacio (`settings.review_every_days`) | según el espacio | info |

Ejemplo para cambiar una regla:
`NEXO_ALERT_RULES={"authz_denied_burst":{"threshold":10},"logging_stalled":{"enabled":true,"window_seconds":1800}}`.

### 11.4 Verificar la cadena de auditoría

Las tablas `cloud_auth_audit_log`, `nexo_authz_events` y `nexo_kb_events` están encadenadas con
hashes. El servidor las verifica cada `NEXO_AUDIT_VERIFY_MINUTES`. A pedido:

```sh
# Por API (auditor, admin o superadmin)
curl -fsS -H "Authorization: Bearer $NEXO_ADMIN_TOKEN" "$NEXO_URL/nexo/admin/audit/verify"
# Por CLI, dentro del contenedor (sale con 1 si la cadena falla)
docker compose -f compose.yaml exec cloud engram nexo audit verify
docker compose -f compose.yaml exec cloud engram nexo audit verify --table cloud_auth_audit_log
```

La respuesta indica `ok`, filas revisadas, la primera fila alterada (`first_broken`), huecos y filas
sin cadena. Una falla es un incidente de seguridad: no corrija la tabla; conserve el respaldo y las
exportaciones y avise al responsable de seguridad.

### 11.5 Exportación firmada

Con `NEXO_AUDIT_EXPORT_DIR` y `NEXO_AUDIT_EXPORT_KEY`, el servidor exporta una vez por día (día UTC)
las tablas `cloud_auth_audit_log`, `nexo_authz_events`, `nexo_kb_events`, `nexo_alerts`,
`nexo_alert_acks` y `nexo_access_reviews` a `<dir>/<tabla>/<AAAA-MM-DD>.jsonl` con su firma
`.jsonl.sig`. Si un día falla, se reintenta (hasta 31 días atrás) y se crea la alerta
`audit_export_failed`.

```sh
# Exportar a mano un rango (los días ya exportados se saltan)
docker compose -f compose.yaml exec cloud engram nexo audit export --since 2026-10-01 --until 2026-10-12
docker compose -f compose.yaml exec cloud engram nexo audit export --since today --include-today   # día en curso, parcial
# Verificar un archivo: firma, contenido, continuidad y, con --db, contra la base
docker compose -f compose.yaml exec cloud engram nexo audit verify-export /export/cloud_auth_audit_log/2026-10-12.jsonl --db
```

Copie la carpeta `audit-export/` fuera del host (almacenamiento con retención o WORM). Para verificar
una copia en otra máquina, use el binario `engram` del release con `NEXO_AUDIT_EXPORT_KEY` en el
entorno (y `ENGRAM_DATABASE_URL` si agrega `--db`). Cada firma lleva `key_id`: si rota la clave,
conserve la anterior para poder verificar los archivos viejos.

### 11.6 Retención

| Datos | Variable | Por defecto | Mínimo |
|---|---|---|---|
| Detalle de conexiones (`nexo_activity_log`) | `NEXO_ACTIVITY_RETENTION_DAYS` | 180 días | 30 |
| Auditoría de seguridad (`cloud_auth_audit_log`) | `NEXO_RETENTION_AUDIT_DAYS` | no se depura | 365 |
| Eventos de roles (`nexo_authz_events`) | `NEXO_RETENTION_AUTHZ_EVENTS_DAYS` | no se depura | 365 |
| Eventos de conocimiento (`nexo_kb_events`) | `NEXO_RETENTION_KB_EVENTS_DAYS` | no se depura | 365 |
| Alertas (`nexo_alerts`, `nexo_alert_acks`) | `NEXO_RETENTION_ALERTS_DAYS` | no se depura | 365 |

La depuración corre una vez por día con funciones `SECURITY DEFINER` de la cuenta dueña
(`nexo_purge_activity`, `nexo_purge_evidence`) y deja el evento `retention.purged`. Defina la política
(por ejemplo, 365 días en la base y el archivo exportado por el plazo que exija la empresa) antes de
activar `NEXO_RETENTION_*`. La presencia (última conexión por persona y token) y las versiones de
documentos no se depuran.

## 12. Endurecimiento

### 12.1 Red y TLS

- Publique solo el puerto 443 de Caddy. El servicio `cloud` y Postgres no deben tener `ports:`.
- Postgres en red privada. Si la base está en otro host o es administrada: `sslmode=verify-full`
  (sección 8 y [`guia-despliegue.md` §4](../seguridad/guia-despliegue.md)).
- Certificado corporativo en Caddy; no use `tls internal` si las PCs no confían en esa CA.
- `NEXO_TRUSTED_PROXIES` con la IP de Caddy y nada más.
- `NEXO_SUPERADMIN_ALLOWED_CIDRS` con la red de administración o la VPN.
- `NEXO_KB_PUBLIC_URL=https://<dominio>` para fijar la URL de los enlaces de las citas (con `NEXO_TRUSTED_PROXIES` bien puesto ya salen con HTTPS; la variable evita depender de las cabeceras del proxy).

### 12.2 Cuentas y tokens

- Dos cuentas de base de datos (log `dos cuentas de base de datos`). El superusuario `postgres` solo
  para administrar.
- `ENGRAM_CLOUD_TOKEN` aleatorio y sin entregar; `ENGRAM_CLOUD_ADMIN` vacía.
- Dos superadmins habilitados; nadie trabaja a diario con un token de superadmin.
- Tokens con caducidad (90 días por defecto) y alcance `read` cuando la persona solo consulta.
- Revise la alerta `shared_token_used`: nadie debería usar el token compartido.

### 12.3 Rotación de secretos

| Secreto | Cómo rotarlo | Impacto |
|---|---|---|
| Contraseña de `nexo_app` o `nexo_owner` | `ALTER ROLE nexo_app PASSWORD '<nueva>';` como `postgres` (o `docker compose exec -T -e NEXO_OWNER_PASSWORD -e NEXO_APP_PASSWORD postgres sh /docker-entrypoint-initdb.d/01-cuentas.sh` con los valores nuevos), actualizar `.env` y `docker compose up -d cloud` | Reinicio breve del servidor |
| `ENGRAM_JWT_SECRET` | Nuevo valor en `.env` y reiniciar `cloud` | Se cierran las sesiones abiertas del panel |
| `ENGRAM_CLOUD_TOKEN` | Nuevo valor en `.env` y reiniciar | Ninguno si nadie lo usa (debería ser así) |
| `ENGRAM_CLOUD_TOKEN_PEPPER` | Solo si se filtró. Nuevo valor y reiniciar | **Todos** los tokens dejan de funcionar: hay que emitir tokens nuevos a todas las personas (empezando por un superadmin con `engram cloud bootstrap recover-token`, si aplica) |
| `NEXO_AUDIT_EXPORT_KEY` | Nuevo valor y reiniciar | Las firmas nuevas llevan otro `key_id`; guarde la clave anterior para verificar archivos viejos |
| `NEXO_ALERT_WEBHOOK_URL` | Regenerar el webhook en Slack o Teams, nuevo valor y reiniciar | Ninguno |
| Tokens de personas | Ver [`administracion.md`](administracion.md), sección 3 | Solo la persona afectada |

Por verificar: recuperar el acceso después de rotar el pepper. `recover-token` solo funciona cuando
el único administrador habilitado no tiene ningún token; con varios administradores puede hacer falta
otro camino. Ensáyelo en un entorno de prueba antes de necesitarlo.

## 13. Lista de comprobación final

| # | Comprobación | Hecho |
|---|---|---|
| 1 | Imagen con etiqueta exacta `ghcr.io/adriel2503/nexo-engram-cloud:<versión>` en `CLOUD_IMAGE` | |
| 2 | `.env` con permisos `600`, fuera del control de versiones; secretos en el gestor de secretos | |
| 3 | Log: `dos cuentas de base de datos` (no `seguridad degradada`) | |
| 4 | Postgres sin puertos publicados; TLS `verify-full` si la base está en otro host | |
| 5 | Caddy con certificado corporativo; solo el puerto 443 publicado | |
| 6 | `NEXO_TRUSTED_PROXIES` con la IP de Caddy | |
| 7 | `NEXO_SUPERADMIN_ALLOWED_CIDRS` con la red de administración (sin aviso al iniciar) | |
| 8 | `NEXO_KB_PUBLIC_URL=https://<dominio>` | |
| 9 | `ENGRAM_CLOUD_ALLOWED_PROJECTS` con los proyectos de memoria acordados | |
| 10 | Exportación firmada activa (`NEXO_AUDIT_EXPORT_DIR` y `NEXO_AUDIT_EXPORT_KEY`); carpeta con dueño 10001 y copiada fuera del host | |
| 11 | Webhook de alertas configurado y probado (o revisión manual de alertas acordada) | |
| 12 | Dos superadmins habilitados, con tokens personales | |
| 13 | `GET /nexo/admin/audit/verify` con `ok: true` | |
| 14 | Respaldo de Postgres programado, cifrado y fuera del host; restauración ensayada | |
| 15 | Política de retención decidida antes de activar `NEXO_RETENTION_*` | |
| 16 | `ENGRAM_CLOUD_ADMIN` vacía y `ENGRAM_CLOUD_INSECURE_NO_AUTH` sin definir | |

## Anexo A. Puntos por verificar

| Tema | Motivo |
|---|---|
| ACME (certificado automático) en Caddy | El compose solo publica el 443 y no se probó la emisión automática |
| Crear cuentas en un Postgres administrado | El script se escribió para la imagen oficial; los proveedores restringen `CREATE DATABASE ... OWNER` y las extensiones |
| Servidor detrás de un pooler | `NEXO_DB_APP_ROLE` existe para ese caso, pero no hay prueba |
| Restauración completa desde `pg_dump` | No está en las pruebas automatizadas |
| Volver a una versión anterior de Nexo | No hay prueba automatizada; los triggers nuevos quedan activos |
| Recuperar acceso tras rotar el pepper con varios administradores | `recover-token` tiene condiciones estrictas |
