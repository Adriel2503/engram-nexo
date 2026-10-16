# Cliente Nexo en Windows: compilar y conectar tu asistente

Para quien ya tiene Engram oficial o instala por primera vez. Ejecuta los pasos en **PowerShell**,
con tu usuario habitual. Copia los bloques en orden; si un comando falla, detente.

Instalarás nuestro fork desde `main` en tu PC. No necesitas Docker, PostgreSQL, una clave Gemini
ni ejecutar `engram cloud serve`: esos componentes están en el servidor.

**Necesitas:** acceso al repositorio de Azure, acceso de lectura al fork en GitHub para los plugins
de Codex y Claude Code, un asistente instalado y tu código de activación o token personal.
La URL del entorno de prueba es `https://nexo-34-51-82-225.sslip.io`.

## 1. Preparar Git y Go

Comprueba si ya los tienes:

```powershell
git --version
go version
```

Si falta alguno, instala solo lo que necesites:

```powershell
winget install --id Git.Git --exact
winget install --id GoLang.Go --exact
```

Cierra y abre PowerShell después de instalar. La versión mínima de Go está en `go.mod`;
actualmente es **1.27.2**. También puedes usar los instaladores oficiales de
[Go](https://go.dev/doc/install) y [Git para Windows](https://git-scm.com/install/windows).

## 2. Si ya tienes Engram oficial: cerrar y respaldar

Cierra Codex, Claude Code, OpenCode y cualquier proceso que use Engram antes de copiar sus datos.
No ejecutes dos versiones sobre la misma memoria durante el cambio.

```powershell
Get-Command engram -All -ErrorAction SilentlyContinue | Select-Object CommandType, Source
Get-Process engram -ErrorAction SilentlyContinue | Select-Object Id, Path
```

Si quedan procesos, cierra la aplicación que los mantiene abiertos antes de continuar.
Guarda una copia de la memoria y del ejecutable que Windows encuentra actualmente:

```powershell
$nexoBackup = Join-Path $HOME ('engram-respaldo-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
New-Item -ItemType Directory -Path $nexoBackup | Out-Null

$nexoDatos = Join-Path $HOME '.engram'
if (Test-Path -LiteralPath $nexoDatos) {
    Copy-Item -LiteralPath $nexoDatos -Destination (Join-Path $nexoBackup '.engram') -Recurse
}

$nexoAnterior = Get-Command engram -CommandType Application -ErrorAction SilentlyContinue |
    Select-Object -First 1
if ($nexoAnterior) {
    Copy-Item -LiteralPath $nexoAnterior.Source -Destination (Join-Path $nexoBackup 'engram-anterior.exe')
}
Write-Output "Respaldo: $nexoBackup"
```

No se borra ni reinicia la carpeta `.engram`; el fork utiliza la memoria existente.
Si personalizaste `ENGRAM_DATA_DIR`, respalda esa carpeta real en vez de la ruta predeterminada.
Conserva el respaldo privado: puede contener credenciales. Un shim de un gestor de paquetes no es
necesariamente el ejecutable original completo; conserva también su instalación hasta terminar.

## 3. Descargar main y compilar

Usaremos una carpeta nueva para no cambiar otros clones que tengas:

```powershell
New-Item -ItemType Directory -Force -Path "$HOME\src" | Out-Null
Set-Location "$HOME\src"
git clone --branch main https://dev.azure.com/realplaza/AgentAI/_git/nexo-ai-playground engram-nexo-cliente
if ($LASTEXITCODE -ne 0) { throw 'No se pudo clonar; revisa tu acceso a Azure.' }
Set-Location engram-nexo-cliente

go mod download
if ($LASTEXITCODE -ne 0) { throw 'No se pudieron descargar las dependencias.' }
New-Item -ItemType Directory -Force -Path out | Out-Null
go build -o out/engram.exe ./cmd/engram
if ($LASTEXITCODE -ne 0) { throw 'La compilación falló; no reemplaces tu instalación.' }
.\out\engram.exe version
```

`out/engram.exe` es el archivo compilado. Todavía no está instalado. Si la carpeta del clon ya
existe, no la borres ni fuerces el comando: usa otro nombre o actualiza tu clon limpio con
`git switch main` y `git pull --ff-only`, comprobando antes `git status`.

## 4. Instalar el ejecutable en una ubicación estable

Desde el clon, copia el archivo a `%USERPROFILE%\.nexo-bin\engram.exe`:

```powershell
$ErrorActionPreference = 'Stop'
$nexoBin = Join-Path $HOME '.nexo-bin'
$nexoExe = Join-Path $nexoBin 'engram.exe'
New-Item -ItemType Directory -Force -Path $nexoBin | Out-Null

if (Test-Path -LiteralPath $nexoExe) {
    Copy-Item -LiteralPath $nexoExe -Destination ($nexoExe + '.backup-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
}
Copy-Item -LiteralPath '.\out\engram.exe' -Destination $nexoExe -Force
& $nexoExe version
```

Es una **copia**, no un movimiento: el archivo compilado sigue en `out`. Si el destino ya existía,
se respalda y reemplaza. Si aparece «archivo en uso», termina de cerrar los asistentes y reintenta.
No borres `.engram` ni uses un comando de reset.

Añade la carpeta al PATH de usuario y úsala primero en esta terminal:

```powershell
$nexoPathUsuario = [Environment]::GetEnvironmentVariable('Path', 'User')
$nexoOtrasRutas = @($nexoPathUsuario -split ';' | Where-Object {
    $_ -and $_.TrimEnd('\') -ine $nexoBin.TrimEnd('\')
})
[Environment]::SetEnvironmentVariable('Path', (@($nexoBin) + $nexoOtrasRutas) -join ';', 'User')
$env:Path = $nexoBin + ';' + $env:Path

Get-Command engram | Select-Object -ExpandProperty Source
engram version
```

Debe resolver a tu `.nexo-bin\engram.exe`. El Engram oficial que esté en otra carpeta se conserva,
pero no debe ser el que se use. En una terminal nueva vuelve a comprobar `Get-Command engram`;
si otro gana por el PATH de sistema, usa la ruta explícita de esta guía y corrige la prioridad del
PATH o desinstala el paquete oficial con su gestor. No elimines carpetas compartidas de Go.

Una compilación desde código puede mostrar una versión con fecha y commit, **sin el sufijo
`-nexo`**. Eso no indica por sí solo que sea el Engram oficial; verifica la ruta y el repositorio.

## 5. Conectar la PC a Nexo

Usa la URL base, sin `/dashboard`. Define la ruta explícita para asegurar que usas el fork:

```powershell
$nexoExe = Join-Path $HOME '.nexo-bin\engram.exe'
```

**Si recibiste un código de activación:** copia solo el código y ejecuta:

```powershell
Get-Clipboard | & $nexoExe nexo connect --server https://nexo-34-51-82-225.sslip.io --activate-stdin
```

**Si ya canjeaste el código en el panel y tienes un token personal:** copia solo el token y ejecuta
esta alternativa, en lugar de la anterior:

```powershell
Get-Clipboard | & $nexoExe nexo connect --server https://nexo-34-51-82-225.sslip.io --token-stdin
```

`Get-Clipboard` lee lo copiado y `|` lo entrega a Engram sin escribirlo en el comando ni dejarlo
en el historial. No pegues códigos o tokens en chats. El código se canjea una sola vez; no vuelvas
a canjearlo si ya obtuviste tu token. La configuración se guarda: no repitas la conexión en cada sesión.

Comprueba:

```powershell
& $nexoExe nexo status
```

Debes ver el servidor y tu identidad. Conectar el cliente no concede roles: el administrador y el
responsable de cada espacio deben darte los accesos que correspondan. Si cambias de servidor,
revisa además si ya tenías configurada sincronización de memorias hacia otro destino antes de
seguir; el protocolo Nexo y la sincronización cloud son configuraciones relacionadas pero distintas.

## 6. Vincular el asistente

Conecta primero la PC (paso 5) para que también se instale el protocolo Nexo. Ejecuta únicamente
las opciones de los asistentes que realmente utilices. Cada CLI debe estar instalada en el PATH.

### Codex

```powershell
& $nexoExe setup codex
```

Configura el MCP y el plugin de nuestro fork. Necesita acceso al repositorio GitHub
`Adriel2503/engram-nexo`, aunque hayas compilado desde Azure.

### Claude Code

Necesita Git para Windows, `claude`, `jq` y `curl` disponibles. Si falta jq:

```powershell
winget install --id jqlang.jq --exact
```

Abre una terminal nueva si instalaste herramientas. Si ya tenías el MCP de Engram oficial, quita
solo ese registro antes de instalar el nuevo; no elimines otros MCP:

```powershell
claude mcp remove engram --scope user
```

Si informa que no existe, continúa. Después ejecuta:

```powershell
$nexoExe = Join-Path $HOME '.nexo-bin\engram.exe'
& $nexoExe setup claude-code
```

### OpenCode

```powershell
& $nexoExe setup opencode
```

Instala el plugin de OpenCode y el protocolo Nexo en la configuración de tu usuario.

En Codex y Claude Code, el instalador detecta un catálogo `engram` de otro origen e intenta
reemplazarlo por el del fork. Si avisa de un conflicto, sigue el mensaje y los pasos de
[migración desde el oficial](instalacion-dev.md#4-si-ya-tienes-el-engram-oficial); no des por
terminada la instalación mientras siga apuntando al catálogo oficial.

## 7. Reiniciar y probar

Cierra por completo y vuelve a abrir el asistente. En un proyecto de trabajo, pregúntale:

> Comprueba si Nexo funciona y dime qué espacios puedo consultar. Después busca FPGA usando nexo_search.

El asistente debe tener las herramientas `nexo_*` y responder con documentos a los que tu usuario
tenga acceso. Que no encuentre FPGA puede ser correcto si no tienes acceso al espacio de prueba.
La memoria local continúa usando tu carpeta de Engram; no necesitas volver a crear recuerdos.

Si el asistente tiene memoria pero no herramientas Nexo, verifica `engram nexo status`, repite
el `setup` con el ejecutable de `.nexo-bin` y reinícialo.
No se necesita instalar otra copia de MCP aparte ni un servidor local.

Los comandos se contrastaron con el instalador del repositorio y se revisó su sintaxis PowerShell.
La compilación e instalación del fork están verificadas en nuestra PC; falta probar este recorrido
completo en el equipo del colega. Referencia general: [instalación del cliente](instalacion-dev.md).
