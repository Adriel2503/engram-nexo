# Empezar con Nexo: start, stack y tour

El binario del fork incluye un asistente y entornos separados. Demo y dev son solo para pruebas con datos sintéticos. Requieren Docker Engine y Docker Compose v2 para Postgres 18 con pgvector; el servidor es el mismo binario, como proceso desacoplado. Prod local usa una imagen construida desde el repositorio y Caddy con CA interna.

## Primera prueba

~~~sh
engram nexo start
engram nexo stack up --mode demo --yes
engram nexo tour --mode demo
~~~

start pregunta qué quieres hacer: probar demo, conectar un asistente o montar dev/prod. También pregunta siempre si activas búsqueda por significado y reordenador. Puedes elegir ejemplo, archivos propios, ambos o vacío. En prod se prepara solo el superadmin; publica contenido después mediante kb upload.

La semilla se entrega por separado. Mientras NewSeeder sea el sustituto, up avisa «semilla no disponible» y deja el panel y las personas preparados. No inventa documentos ni roles de espacios. Después de integrar seed.New(), stack datos carga el ejemplo por el mismo contrato.

## Modos y carpetas

| Modo | Servidor / Postgres | Personas | Arranque |
| --- | --- | --- | --- |
| demo | 18480 / 55480 | superadmin, admin, publicador, proponente, lector, auditor | binario y Postgres |
| dev | 18481 / 55481 | superadmin, publicador, lector | binario y Postgres con dueña y aplicación |
| prod | HTTPS 18443 / Postgres interno | superadmin | genera y valida; arranca con --up |

Todo vive en NEXO_STACK_HOME/<modo> o, sin esa variable, en ~/.nexo-stack/<modo>. --name añade el mismo sufijo a la carpeta, al proyecto compose y a los volúmenes. --dir permite elegir una carpeta vacía distinta; repítelo en los comandos siguientes. No reutilices una carpeta ajena. Los puertos ocupados o reservados se buscan en los siguientes nueve; status indica los elegidos.

Para aislar pruebas, define NEXO_STACK_HOME, ENGRAM_DATA_DIR y NEXO_HOME en una carpeta temporal. El asistente de conexión usa la carpeta de datos de Engram indicada por ENGRAM_DATA_DIR; la copia de conocimiento respeta NEXO_HOME. No cambies tus carpetas habituales para una demo.

~~~powershell
$env:NEXO_STACK_HOME = Join-Path $env:TEMP ([guid]::NewGuid().ToString())
$env:ENGRAM_DATA_DIR = Join-Path $env:NEXO_STACK_HOME 'engram'
$env:NEXO_HOME = Join-Path $env:NEXO_STACK_HOME 'nexo'
engram nexo stack up --mode demo --name prueba --port 18490 --pg-port 55490 --yes
~~~

Sin terminal debes indicar --mode. --plain y NO_COLOR producen texto plano. Demo/dev escuchan en 127.0.0.1; exponer otro bind exige --bind y --i-understand-this-is-a-demo. Postgres siempre se publica en loopback; prod no lo publica.

## Personas y accesos

El núcleo crea el superadmin con la guarda atómica del bootstrap de Engram y emisión auditada. Para las otras personas emite códigos y los canjea con client.Activate sin autenticar el canje como emisor. Cada token se guarda inmediatamente en accesos.md; no va al estado ni a la salida del terminal. El archivo y .env son privados, con 0600 y, en Windows, ACL mediante icacls.

~~~sh
engram nexo stack accesos --mode demo --abrir
engram nexo stack accesos --mode demo --copiar lector
engram nexo connect --server http://127.0.0.1:18480 --token-stdin
engram setup codex
~~~

También puedes usar claude-code u opencode. accesos.md incluye los comandos y la URL del panel. --show se rechaza: este encargo exige no imprimir tokens, incluso a pedido. Usa el archivo o el portapapeles nativo. El contenido del portapapeles es un secreto; no lo pegues en registros ni chats.

start también pide una URL y un código con entrada oculta para conectar con ConnectActivated. El token canjeado se guarda antes de /me; si falla una operación posterior, continúa mediante nexo connect --server <URL> --resume. Luego configura el asistente usando engram setup y pide reiniciarlo.

## Configurar la búsqueda

Por defecto, embeddings y reordenador están apagados. Las mejoras R1 están activas. En el asistente siempre se pregunta por ambos proveedores. Con --yes o sin terminal se usan las opciones dadas.

~~~sh
engram nexo stack up --mode demo --embedder gemini --embed-key-file <archivo-privado> --yes
engram nexo stack config --mode demo --embedder off --rerank off --r1 all --yes
~~~

La clave se lee de entrada oculta, --embed-key-file/--rerank-key-file, NEXO_KB_EMBED_API_KEY/NEXO_KB_RERANK_API_KEY o el alias del proveedor (GEMINI_API_KEY, OPENAI_API_KEY, AZURE_OPENAI_API_KEY, COHERE_API_KEY, JINA_API_KEY, VOYAGE_API_KEY). No existe una opción que acepte la clave como valor. Se hace una llamada mínima antes de guardar; si falla, se conserva la configuración anterior.

En el asistente, una prueba fallida informa si se rechazó la clave, no se encontró el modelo, se agotó la cuota o falló la red, sin mostrar la clave ni el mensaje libre del proveedor. Ofrece **Reintentar** para revisar proveedor, modelo y clave (entrada oculta; vacío conserva la anterior), o **Seguir sin búsqueda por significado**, que desactiva embeddings y reordenador y conserva la búsqueda por palabras. Cancelar conserva la configuración anterior. Cuando no se abre el asistente de búsqueda (por ejemplo, stack up/config con --yes), una prueba fallida devuelve el diagnóstico y no guarda la configuración.

Embeddings: off, gemini, openai, azure-openai u openai-compatible. Opciones: --embed-model, --embed-dims, --embed-url, --embed-deployment y --embed-auth. Gemini usa gemini-embedding-001 y 768 dimensiones; OpenAI usa los valores definidos por el servidor. Azure requiere URL y despliegue; compatible requiere modelo, URL y dimensión.

Reordenador: off, cohere, jina, voyage, gemini, openai, azure-openai, openai-compatible, tei o rerank-compatible. Opciones: --rerank-model, --rerank-url, --rerank-deployment y --rerank-auth. Puede reutilizar la clave de embeddings del mismo proveedor. --r1 admite all, off o una lista de doc_context,split_tables,synonyms,threshold,diversity,quote.

Los documentos publicados y las consultas reordenadas envían fragmentos al proveedor y pueden generar costos. Las pruebas de clave envían solo texto sintético. HTTP para proveedores se admite solo en loopback; los externos requieren HTTPS. Cambiar modelo/dimensión crea un índice nuevo. config reinicia demo/dev conservando sus datos; en prod guarda la configuración y se aplica con --up. Sin pgvector, el núcleo avisa, cambia a texto y reinicia lo necesario; status refleja el cambio.

## Contenido y recorrido

~~~sh
engram nexo stack datos --mode demo
engram nexo stack datos --mode demo --own-dir <carpeta> --per-subfolder
engram nexo stack datos --mode demo --quitar-ejemplo
engram nexo tour --mode demo
~~~

El contrato Seeder se ocupa de publicación, permisos, redacción e idempotencia. Los formatos soportados por Nexo incluyen Markdown (.md/.markdown/.mdx), texto (.txt/.text) y Word (.docx). --no-data evita cargar ejemplos al arrancar. La semilla no está disponible hasta unir su rama.

tour comienza con el paso 0 de asistentes y continúa con tres pasos humanos: guardar/buscar una memoria con el asistente, preguntar por logging y abrir una cita, y proponer/aprobar/revisar auditoría con distintos roles. Solo escribe tour.done al confirmar los tres pasos. El menú de siguiente paso informa si ya lo completaste; tour explícito siempre permite repetirlo. En dev/prod debes solicitar los roles que faltan. La memoria de Engram por HTTP no se sincroniza a cloud: esa sincronización exige HTTPS; la memoria local y el conocimiento funcionan en demo.

## Prod local

~~~sh
engram nexo stack up --mode prod --yes
engram nexo stack up --mode prod --up --yes
engram nexo stack up --mode prod --image <referencia> --yes
engram nexo stack up --mode prod --build-from <repositorio> --rebuild --yes
~~~

Sin --up se construye/reutiliza la imagen propia nexo-stack-<modo>-<nombre>-cloud:<versión> y se valida compose con --quiet, sin arrancar servicios. Desde el repositorio usa docker/cloud/Dockerfile; sin fuentes requiere --image o --build-from. --rebuild fuerza construcción. No admite --example ni contenido inicial propio: empieza con el superadmin. Las cuentas de servicio permanecen apagadas.

Caddy usa tls internal y solo publica 127.0.0.1:18443. El núcleo obtiene su CA para sus comprobaciones; no desactiva la verificación TLS. Para navegador y asistentes debes confiar explícitamente en esa CA. La red y los volúmenes tienen nombre propio; solo la IP del proxy es confiable. Si la subred calculada colisiona con una existente, usa otro --name. Esto ensaya el despliegue local; para operar un servidor del equipo sigue [servidor-produccion.md](servidor-produccion.md).

## Postgres existente: --database-url

Con --database-url <URL> (o NEXO_STACK_DATABASE_URL) en demo/dev no se utiliza Docker. Nexo migra una base dedicada ya existente; no crea ni borra una base externa. Para dos cuentas define también NEXO_STACK_MIGRATION_DATABASE_URL. En prod sigue haciendo falta Docker para servidor/Caddy y es obligatoria la cuenta dueña separada. Las URL de prod desde un contenedor deben ser alcanzables desde su red, no el localhost del host.

Entrega la URL con contraseña mediante el entorno, nunca la copies a salidas ni al historial. Se guarda solo en .env protegido. En una base externa reset retira el estado local pero conserva los datos; un entorno nuevo no puede bootstrapear otra vez una base con superadmin existente. Para reutilizarla conserva su carpeta de estado o administra expresamente la base dedicada por separado. Con TLS usa las reglas del manual de producción; monta certificados cuando corresponda.

## Detener, diagnosticar y borrar

~~~sh
engram nexo stack status --mode demo
engram nexo stack logs --mode demo
engram nexo stack down --mode demo
engram nexo stack up --mode demo --yes
engram nexo stack reset --mode demo --yes
~~~

down conserva datos y credenciales. reset pide confirmación; sin terminal exige --yes. Solo retira contenedores/red/volúmenes del proyecto propio y archivos conocidos. Nunca ejecuta prune. Los archivos ajenos en --dir se conservan. Las carpetas vacías pueden permanecer para una nueva corrida. logs vuelve a redactar la salida; el supervisor redacta antes de persistir servidor.log. status combina identidad del proceso, /health y /nexo/health/ready.

| Problema | Acción |
| --- | --- |
| Docker no responde o acceso denegado | Inicia el motor y revisa permisos; instala Compose v2 o usa una base externa |
| Puerto ocupado/reservado | Mira status para el puerto elegido; cambia --port/--pg-port en un entorno nuevo |
| Proceso muerto o servidor no saludable | Revisa logs; si sigue vivo, down antes de up |
| Archivo .operacion pendiente tras un corte | Confirma que no corre otra operación; retira solo ese archivo de tu entorno y repite |
| Bootstrap interrumpido antes de guardar el primer token | Usa el flujo recover-token existente dentro del servidor, guardando el acceso privado; no imprimas el token en una sesión registrada |
| Clave rechazada | Revisa proveedor, modelo, URL, cuota y permisos; la clave inválida no se guarda |
| Semilla no disponible | Integra la rama de seed y cambia NewSeeder a seed.New(); los comandos de datos usan ese contrato |
| No se puede copiar | Abre accesos.md; en Linux instala wl-copy o xclip |

Windows lanza el supervisor sin ventana y desacoplado de la terminal. Guarda identidad de PID, ejecutable e instante de creación para supervisor e hijo. El cierre usa esos PIDs; un PID reutilizado o un ejecutable distinto de engram se rechaza. El cierre real de la terminal y Docker requieren una prueba manual en una sesión con permisos sobre el motor.

## Asistentes en el recorrido

Después de up, el menú «Siguiente paso» ofrece recorrido guiado, solo conectar asistentes o terminar con los comandos para después. --yes y --no-tour omiten el menú. --no-agents omite la configuración de asistentes.

~~~sh
engram nexo stack agentes --mode demo
engram nexo stack agentes --mode dev --agents codex,pi --user publicador --yes
engram nexo tour --mode demo --no-agents
~~~

El paso 0 detecta los asistentes soportados por engram setup (claude, codex, opencode, pi y el resto del registro), marca los detectados y permite elegir varios. Ejecuta el setup existente para cada elegido, informa los archivos actualizados y guarda la conexión común a ese servidor con el token de publicador en demo/dev o superadmin en prod. --user elige otra persona. El token no se imprime. Si elegiste carpetas aisladas mediante ENGRAM_DATA_DIR/NEXO_HOME, los asistentes deben iniciarse con esas mismas variables.

El recorrido omite el paso 0 si ya se configuró alguno para ese servidor o si no detecta ninguno instalado. Sin asistentes, muestra engram save/search y nexo kb search; primero conecta la CLI con --token-stdin. El paso 3 ofrece tour --action proponer, tour --action aprobar --proposal-id <ID> y tour --action auditoria, usando las mismas APIs y los tokens de proponente/publicador/auditor. El contenido propuesto es sintético y requiere backend y sus roles; la auditoría por CLI muestra solo el número de eventos. Para deshacer la conexión usa engram nexo disconnect --keep-copy; para retirar la integración quita solo la entrada MCP engram, sus bloques de protocolo o el plugin del asistente indicado.

## Prueba de humo con Docker

Es optativa, con etiqueta stackdocker. El coordinador debe ejecutarla en una sesión con acceso al motor, desde el repositorio, usando Go 1.27.2. No forma parte de la batería normal.

~~~sh
go test -tags stackdocker -count=1 -timeout 30m -run TestStackDockerSmoke ./internal/nexo/stack/
~~~

STACK_SMOKE_PORT (por defecto 18480) y STACK_SMOKE_DBPORT (por defecto 55480) fijan los puertos; deben estar libres. La prueba rechaza recursos previos del proyecto nexo-stack-demo-smoke para no tocar una corrida ajena. Aísla NEXO_STACK_HOME, ENGRAM_DATA_DIR y NEXO_HOME en una carpeta temporal, construye un binario engram y usa el Seeder de sustituto. Comprueba panel/preparación, seis personas y todos los tokens, status/logs/repetición sin reinicio, permisos de accesos, ausencia de secretos en salida/log, persistencia después de down/up y eliminación con reset. Un defer limpia exclusivamente su proyecto y verifica contenedores/volúmenes/red/PIDs. Ante fallos muestra las últimas 80 líneas ya redactadas.

~~~powershell
$env:GOTOOLCHAIN = 'go1.27.2'
$env:STACK_SMOKE_PORT = '18480'
$env:STACK_SMOKE_DBPORT = '55480'
go test -tags stackdocker -count=1 -timeout 30m -run TestStackDockerSmoke ./internal/nexo/stack/
~~~

Compilar/verificar sin ejecutarla: go vet -tags stackdocker ./internal/nexo/stack/. Sin la etiqueta, TestStackDockerSmoke no está compilada ni se ejecuta.
