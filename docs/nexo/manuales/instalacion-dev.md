# Instalar Nexo IA en tu PC (desarrolladores)

**Para quién:** desarrolladores de Real Plaza en Windows, macOS o Linux.
**Versión:** `3.4.0-nexo.1` (fork `engram-nexo`, basado en Engram v3.4.0).
**Tiempo:** unos 15 minutos.

Si vas a levantar un servidor Nexo en tu propia PC para pruebas, lee [servidor-local.md](servidor-local.md).
Este manual es para conectar tu PC a un servidor que ya existe.

## Contenido

1. [Qué es Nexo IA](#1-qué-es-nexo-ia)
2. [Antes de empezar](#2-antes-de-empezar)
3. [Si tenías el kit anterior de Nexo](#3-si-tenías-el-kit-anterior-de-nexo)
4. [Si ya tienes el Engram oficial](#4-si-ya-tienes-el-engram-oficial)
5. [Instalar el binario](#5-instalar-el-binario)
6. [Obtener tu token](#6-obtener-tu-token)
7. [Conectar tu PC](#7-conectar-tu-pc)
8. [Configurar tu asistente](#8-configurar-tu-asistente)
9. [Reiniciar y comprobar](#9-reiniciar-y-comprobar)
10. [Uso diario](#10-uso-diario)
11. [Trabajar sin conexión](#11-trabajar-sin-conexión)
12. [Comandos de terminal](#12-comandos-de-terminal)
13. [Actualizar a una versión nueva](#13-actualizar-a-una-versión-nueva)
14. [Desconectar y desinstalar](#14-desconectar-y-desinstalar)
15. [Problemas frecuentes](#15-problemas-frecuentes)
16. [Dónde queda cada cosa](#16-dónde-queda-cada-cosa)
17. [Variables de entorno del cliente](#17-variables-de-entorno-del-cliente)

---

## 1. Qué es Nexo IA

Nexo IA le da a tu asistente de IA (Claude Code, Codex, Cursor y otros) acceso a los **estándares
oficiales del equipo**, con cita, enlace y vigencia, y una **memoria del proyecto** en tu PC.
Cuando no hay un estándar publicado, el asistente lo dice en lugar de inventarlo.
Si el servidor no responde, el asistente consulta una copia local de los estándares.

Todo funciona con un solo programa, `engram`, que es la versión de Engram adaptada para Nexo.

## 2. Antes de empezar

Necesitas lo siguiente. Revisa cada punto antes de seguir.

| Requisito | Para qué | Cómo comprobarlo |
|---|---|---|
| Acceso de lectura al repositorio privado `Adriel2503/engram-nexo` en GitHub | Descargar el binario y, en Claude Code y Codex, instalar el plugin | Abre `https://github.com/Adriel2503/engram-nexo` con tu sesión de GitHub. Si ves "404", pide acceso al administrador de Nexo |
| Git con tus credenciales de GitHub por HTTPS | Claude Code y Codex clonan el plugin desde el repositorio privado | `git ls-remote https://github.com/Adriel2503/engram-nexo.git` debe listar ramas sin pedir usuario ni dar error |
| GitHub CLI (`gh`), recomendado | Descargar el release desde la terminal | `gh auth status` debe decir que iniciaste sesión |
| Al menos un asistente soportado | Usar Nexo | La lista completa está en la [sección 8](#8-configurar-tu-asistente) |
| `jq` y `curl` en el PATH, **solo si usas Claude Code** | El plugin de Claude Code los exige en cualquier sistema | `jq --version` y `curl --version` |
| La URL del servidor Nexo y tu token | Conectar tu PC | Te los da el administrador de Nexo ([sección 6](#6-obtener-tu-token)) |

Instalar lo que falte:

**Windows (PowerShell)**

```powershell
winget install GitHub.cli
winget install jqlang.jq        # solo para Claude Code
gh auth login                   # elige GitHub.com, HTTPS y "Login with a web browser"
```

`curl` ya viene con Windows 10 y 11 (`curl.exe`). Si usas Git para Windows, `gh auth login` también
deja configuradas tus credenciales de git por HTTPS.

**macOS (Terminal)**

```bash
brew install gh jq
gh auth login
```

**Linux (Debian o Ubuntu)**

```bash
sudo apt install jq curl        # gh: ver https://github.com/cli/cli/blob/trunk/docs/install_linux.md
gh auth login
```

Cierra y vuelve a abrir la terminal después de instalar, para que tome el PATH nuevo.

Por verificar: que `gh auth login` deje configurado el ayudante de credenciales de git en macOS y
Linux. Si `git ls-remote` pide usuario y contraseña, ejecuta `gh auth setup-git`.

## 3. Si tenías el kit anterior de Nexo

El kit anterior (`nexo_ia/02-nexo-kit`, plugin `nexo@nexo-ia`, instalador `Install-Nexo.ps1`) quedó
como histórico. Si lo instalaste, quítalo **antes** de seguir, porque deja variables de entorno que
tienen prioridad sobre la conexión nueva.

**Windows (PowerShell 7)**

```powershell
# 1. Desinstalador del kit: quita el protocolo de cada asistente, las variables de usuario
#    ENGRAM_CLOUD_SERVER, ENGRAM_CLOUD_TOKEN, ENGRAM_CLOUD_AUTOSYNC y ENGRAM_CLOUD_INSECURE_NO_AUTH,
#    la tarea programada "Nexo-Sync" y la carpeta ~/.nexo (incluida la copia local vieja).
pwsh "$HOME\.nexo\Uninstall-Nexo.ps1" -Completo

# 2. Catálogo del plugin del kit en Claude Code (el desinstalador no lo quita)
claude plugin marketplace remove nexo-ia

# 3. Comprobar que no quedaron variables del kit (las cuatro deben salir vacías)
'ENGRAM_CLOUD_SERVER','ENGRAM_CLOUD_TOKEN','ENGRAM_CLOUD_AUTOSYNC','ENGRAM_CLOUD_INSECURE_NO_AUTH' |
  ForEach-Object { "$_ = " + [Environment]::GetEnvironmentVariable($_, 'User') }
```

Qué deberías ver: mensajes `OK eliminado ...` y `OK variables de usuario de Engram Cloud quitadas`.
Tu memoria de proyectos (`~/.engram`) no se borra.

Notas:

- El desinstalador también quita el registro MCP `engram` de cada asistente. No pasa nada:
  `engram setup` lo vuelve a crear en la [sección 8](#8-configurar-tu-asistente).
- En Cursor, borra a mano el texto del protocolo Nexo de *Settings > Rules > User Rules*.
- Si el kit dejó `engram.exe` en `%USERPROFILE%\.nexo-bin`, bórralo y quita esa carpeta del PATH
  (ver [sección 4](#4-si-ya-tienes-el-engram-oficial)).
- Cierra y vuelve a abrir la terminal para que desaparezcan las variables.

## 4. Si ya tienes el Engram oficial

El binario de Nexo reemplaza al Engram oficial: usa la misma carpeta de datos (`~/.engram`), así que
tu memoria de proyectos se conserva. Lo que hay que evitar es tener **dos** `engram` en el PATH.

1. Busca dónde está el que tienes:

   ```powershell
   where.exe engram          # Windows
   ```

   ```bash
   which -a engram           # macOS y Linux
   ```

2. Quítalo según cómo lo instalaste:

   | Instalado con | Cómo quitarlo |
   |---|---|
   | Homebrew | `brew uninstall engram` |
   | `go install` | Borra `engram` (o `engram.exe`) de `$(go env GOPATH)/bin` |
   | Zip o tar.gz del release oficial, o el kit anterior | Borra el archivo y quita su carpeta del PATH |

3. Si ya habías corrido `engram setup claude-code` con el Engram oficial, quita también su plugin y
   su registro MCP. Si no lo haces, Claude Code seguiría usando el plugin oficial (sin el protocolo
   Nexo) y el registro MCP apuntaría al binario viejo:

   ```bash
   claude plugin marketplace list            # si "engram" dice Source: GitHub (Gentleman-Programming/engram):
   claude plugin uninstall engram@engram --scope user
   claude plugin marketplace remove engram
   claude mcp remove engram --scope user
   ```

4. Lo mismo en Codex, si lo usas:

   ```bash
   codex plugin marketplace list             # si aparece el catálogo "engram" del Engram oficial:
   codex plugin remove engram@engram
   codex plugin marketplace remove engram
   ```

El catálogo del fork también se llama `engram`. Si te olvidas de los pasos 3 y 4, `engram setup
claude-code` y `engram setup codex` (del binario del fork) detectan un catálogo `engram` que apunta a
otro origen (o, en Codex, a otra rama que `main`), lo quitan y agregan el del fork; lo avisan con una
línea `[nexo] el catálogo de plugins "engram" ... se reemplaza por Adriel2503/engram-nexo`. Si no
pueden quitarlo, muestran los comandos para hacerlo a mano. Igual conviene hacer los pasos 3 y 4:
esa detección solo cambia el catálogo, no el registro MCP viejo. Los demás
asistentes no necesitan este paso: `engram setup` reescribe su configuración.

El plugin del fork tiene versión propia (`0.1.1201` en Claude Code, `0.1.501` en Codex; la de Engram
era `0.1.12` y `0.1.5`). Si `claude plugin list` muestra `engram@engram` con `0.1.12`, todavía tienes
el plugin oficial: vuelve a ejecutar `engram setup claude-code`.

## 5. Instalar el binario

La versión publicada es `v3.4.0-nexo.1` ("Nexo IA v3.4.0-nexo.1" en GitHub). Archivos del release:

| Sistema | Archivo |
|---|---|
| Windows, Intel o AMD | `engram_3.4.0-nexo.1_windows_amd64.zip` |
| Windows, ARM | `engram_3.4.0-nexo.1_windows_arm64.zip` |
| macOS, Apple Silicon (M1 o posterior) | `engram_3.4.0-nexo.1_darwin_arm64.tar.gz` |
| macOS, Intel | `engram_3.4.0-nexo.1_darwin_amd64.tar.gz` |
| Linux, x86-64 | `engram_3.4.0-nexo.1_linux_amd64.tar.gz` |
| Linux, ARM64 | `engram_3.4.0-nexo.1_linux_arm64.tar.gz` |
| Todos | `checksums.txt` (huellas SHA-256 para verificar la descarga) |

Cada archivo trae el binario `engram` (`engram.exe` en Windows) y una carpeta `tools/` que Nexo no
necesita.

También puedes descargarlo desde la web con tu sesión iniciada:
`https://github.com/Adriel2503/engram-nexo/releases/tag/v3.4.0-nexo.1`. Desde la terminal es mejor
(con `gh`), porque el sistema no marca el archivo como "descargado de internet".

### Windows (PowerShell)

```powershell
# 1. Descargar (cambia amd64 por arm64 si tu equipo es ARM)
$ver = "3.4.0-nexo.1"
$tmp = Join-Path $env:TEMP "engram-nexo"
gh release download "v$ver" -R Adriel2503/engram-nexo -p "engram_${ver}_windows_amd64.zip" -p checksums.txt -D $tmp --clobber

# 2. (Opcional) Verificar la huella: los dos valores deben ser iguales
(Get-FileHash "$tmp\engram_${ver}_windows_amd64.zip" -Algorithm SHA256).Hash.ToLower()
Select-String "windows_amd64.zip" "$tmp\checksums.txt"

# 3. Instalar en una carpeta propia y agregarla al PATH de tu usuario
$dir = Join-Path $env:LOCALAPPDATA "Programs\engram-nexo"
New-Item -ItemType Directory -Force $dir | Out-Null
Expand-Archive "$tmp\engram_${ver}_windows_amd64.zip" -DestinationPath $dir -Force
$path = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($path -notlike "*$dir*") { [Environment]::SetEnvironmentVariable('Path', "$path;$dir", 'User') }
```

Cierra y vuelve a abrir PowerShell. Si descargaste el zip con el navegador, desbloquéalo antes de
descomprimir: `Unblock-File <ruta del zip>`.

### macOS (Terminal)

```bash
ver=3.4.0-nexo.1
arch=arm64                      # Apple Silicon; en una Mac Intel usa amd64
tmp=$(mktemp -d)
gh release download "v$ver" -R Adriel2503/engram-nexo -p "engram_${ver}_darwin_${arch}.tar.gz" -p checksums.txt -D "$tmp"
(cd "$tmp" && shasum -a 256 -c checksums.txt --ignore-missing)    # debe decir OK
mkdir -p ~/.local/bin
tar -xzf "$tmp/engram_${ver}_darwin_${arch}.tar.gz" -C ~/.local/bin engram
chmod +x ~/.local/bin/engram
# Si ~/.local/bin no está en tu PATH (zsh):
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc
```

Si lo descargaste con el navegador y macOS no deja abrirlo, quita la marca de descarga:
`xattr -d com.apple.quarantine ~/.local/bin/engram`.

### Linux (bash)

```bash
ver=3.4.0-nexo.1
arch=amd64                      # o arm64
tmp=$(mktemp -d)
gh release download "v$ver" -R Adriel2503/engram-nexo -p "engram_${ver}_linux_${arch}.tar.gz" -p checksums.txt -D "$tmp"
(cd "$tmp" && sha256sum -c checksums.txt --ignore-missing)        # debe decir OK
mkdir -p ~/.local/bin
tar -xzf "$tmp/engram_${ver}_linux_${arch}.tar.gz" -C ~/.local/bin engram
chmod +x ~/.local/bin/engram
# Si ~/.local/bin no está en tu PATH:
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc
```

### Comprobar

```bash
engram version
```

Qué deberías ver:

```text
engram 3.4.0-nexo.1
```

Si ves una versión sin `-nexo` (por ejemplo `engram 3.4.0`), tu PATH está usando otro Engram: vuelve a
la [sección 4](#4-si-ya-tienes-el-engram-oficial).

Importante: deja el binario siempre en la misma carpeta. `engram setup` guarda la ruta completa del
binario en la configuración de cada asistente; si lo mueves, tendrás que repetir la
[sección 8](#8-configurar-tu-asistente).

## 6. Obtener tu token

Pide al administrador de Nexo:

- la **URL del servidor** (por ejemplo `https://<servidor-nexo>`);
- un **token personal** para tu equipo. Por convención se nombra por equipo, por ejemplo
  `laptop-<tu-usuario>` o `pc-<tu-usuario>`.

Lo que conviene saber del token:

- Es tu identidad ante Nexo: todo lo que hagas queda a tu nombre. No lo compartas ni lo pegues en
  chats, tickets o repositorios.
- El servidor lo muestra **una sola vez** al crearlo. Si lo pierdes, pide uno nuevo.
- Vence. Por defecto a los 90 días (el máximo lo fija el servidor; por defecto 365). Cuando venza,
  pide uno nuevo y repite la [sección 7](#7-conectar-tu-pc).
- Tiene un alcance: `write` (consultar y proponer) o `read` (solo consultar). Para proponer
  estándares desde el asistente necesitas `write`.

## 7. Conectar tu PC

El comando `engram nexo connect` hace cuatro cosas:

1. Comprueba el servidor y el token con el servidor (`/nexo/authz/v1/me`). Si algo falla, **no
   guarda nada**.
2. Guarda la URL y el token en la configuración cloud de Engram (`~/.engram/cloud.json`, con permisos
   solo para tu usuario).
3. Deja un marcador de conexión sin el token (`~/.engram/nexo.json`).
4. Descarga la copia local de los estándares que puedes ver (`~/.nexo/kb.db`).

El token nunca se imprime. Elige una de estas tres formas de pasarlo. Las dos primeras evitan que
quede en el historial de la terminal.

**Opción A (recomendada): `--token-stdin`.** Copia el token al portapapeles y envíalo por la entrada
estándar, sin que aparezca en pantalla:

```powershell
# Windows (PowerShell)
Get-Clipboard | engram nexo connect --server https://<servidor-nexo> --token-stdin
```

```bash
# macOS
pbpaste | engram nexo connect --server https://<servidor-nexo> --token-stdin
```

```bash
# Linux: escribe o pega el token y pulsa Enter (no se guarda en el historial)
engram nexo connect --server https://<servidor-nexo> --token-stdin
```

En el último caso el token se ve en pantalla mientras lo pegas; no queda en el historial.

**Opción B: variable `NEXO_TOKEN`.**

```powershell
# Windows (PowerShell): pide el token sin mostrarlo
$env:NEXO_TOKEN = Read-Host "Token" -MaskInput     # PowerShell 7.1 o posterior
engram nexo connect --server https://<servidor-nexo>
Remove-Item Env:NEXO_TOKEN
```

```bash
# macOS y Linux
read -rs NEXO_TOKEN && export NEXO_TOKEN
engram nexo connect --server https://<servidor-nexo>
unset NEXO_TOKEN
```

**Opción C: `--token <token>`.** Funciona, pero el token queda en el historial de la terminal. Úsala
solo si las otras no son posibles y borra la línea del historial después.

Si pasas más de una, gana `--token-stdin`; luego `--token`; y por último `NEXO_TOKEN`.

Qué deberías ver:

```text
Conectado a https://<servidor-nexo> como <tu-usuario> (token de alcance write).
Copia local <tu carpeta>/.nexo/kb.db: 3 espacios, 135 documentos.
Siguiente paso: engram setup <agente> (por ejemplo claude-code o codex) y reiniciar el asistente.
Si el asistente ya estaba configurado: engram nexo setup <agente>.
```

Los números dependen de los espacios que puedes ver. Otros mensajes posibles:

| Mensaje | Qué significa |
|---|---|
| `Aviso: la configuración cloud apuntaba a <otra URL>; ahora apunta a Nexo.` | Tenías Engram conectado a otro servidor. Ahora apunta a Nexo |
| `La copia local no se pudo sincronizar todavía (...); se reintentará sola.` | Te conectaste, pero la descarga de la copia falló. Se reintenta al abrir el asistente o con `engram nexo kb sync` |
| `No se conectó: el token no es válido o venció: no se guardó nada` | Pide un token nuevo |
| `No se conectó: el token no es de un usuario administrado (...)` | Ese token no es personal (por ejemplo, el token compartido del servidor). Pide un token personal |
| `No se conectó: el servidor no es un servidor Nexo (no tiene /nexo/authz/v1): no se guardó nada` | La URL apunta a otro servidor (por ejemplo, un Engram Cloud sin Nexo). Revisa la URL |
| `No se conectó: servidor inválido: scheme must be http or https` | La URL no empieza con `https://` (o `http://`) |
| `No se conectó: no se pudo validar la conexión (Sin conexión con el servidor de Nexo: ...)` | El servidor no respondió: revisa la URL, la VPN o la red |

Opciones de `engram nexo connect`: `--server` (obligatoria), `--token-stdin`, `--token`, `--json`
(salida en JSON).

## 8. Configurar tu asistente

Con la PC ya conectada, ejecuta `engram setup` con el nombre de tu asistente. Puedes repetirlo para
cada asistente que uses.

```bash
engram setup claude-code
```

Sin nombre, `engram setup` muestra un menú para elegir. Opción adicional: `--protocol=slim|full`
(cuánto texto inyecta Engram al iniciar sesión; por defecto `full`; `slim` solo aplica a
claude-code).

`engram setup` hace dos cosas:

1. **Lo de Engram:** registra el servidor MCP `engram` (con `mcp --tools=agent` y la ruta completa del
   binario) y las instrucciones de memoria del proyecto.
2. **Lo de Nexo:** instala el protocolo Nexo (skill `nexo-protocol`). **Solo si la PC está
   conectada.** Si corriste `engram setup` antes de conectar, ejecuta después
   `engram nexo setup <asistente>`.

Las herramientas `nexo_*` aparecen en el asistente porque el MCP de Engram las registra cuando la PC
está conectada. No hace falta configurarlas aparte.

### Asistentes soportados (14)

| `<asistente>` | Qué instala Engram | Qué instala Nexo (con la PC conectada) |
|---|---|---|
| `claude-code` | Plugin `engram` desde el catálogo del fork (`Adriel2503/engram-nexo`, rama `main`) y registro MCP de usuario | La skill `nexo-protocol` llega **dentro del plugin**. Si había una copia suelta anterior en `~/.claude/skills/nexo-protocol`, la borra |
| `codex` | Bloque `[mcp_servers.engram]` en `~/.codex/config.toml`, archivos de instrucciones y plugin `engram` desde el fork (`--ref main`) | La skill llega **dentro del plugin**. Borra copias sueltas anteriores en `~/.codex/skills/` y `~/.agents/skills/` |
| `opencode` | Plugin de OpenCode y protocolo de memoria | Skill en `~/.config/opencode/skills/nexo-protocol/SKILL.md` (o `$XDG_CONFIG_HOME/opencode/...`) |
| `pi` | Paquete `gentle-engram` de Pi | Bloque Nexo en `~/.pi/agent/AGENTS.md` (o `$PI_CODING_AGENT_DIR/AGENTS.md`) |
| `gemini-cli` | MCP en `~/.gemini/settings.json` y archivos de recuperación | Bloque Nexo en `~/.gemini/GEMINI.md` |
| `antigravity-cli` | MCP en `~/.gemini/config/mcp_config.json` y protocolo en `~/.gemini/GEMINI.md` | Bloque Nexo en `~/.gemini/GEMINI.md` (el mismo archivo que gemini-cli) |
| `windsurf` | MCP en `~/.codeium/windsurf/mcp_config.json` y protocolo en `global_rules.md` | Bloque Nexo en `~/.codeium/windsurf/memories/global_rules.md` |
| `qwen` | MCP en `~/.qwen/settings.json` y protocolo en `QWEN.md` | Bloque Nexo en `~/.qwen/QWEN.md` |
| `kiro` | MCP en `~/.kiro/settings/mcp.json` y protocolo en `steering/engram.md` | Bloque Nexo en `~/.kiro/steering/engram.md` |
| `cursor` | MCP en `~/.cursor/mcp.json` y `~/.cursor/engram-memory-protocol.md` para pegar | Archivo propio `~/.cursor/nexo-protocol.md` para pegar (ver abajo) |
| `vscode-copilot` | MCP en el `mcp.json` de usuario de VS Code y `prompts/engram.instructions.md` | Archivo propio `prompts/nexo.instructions.md` (con `applyTo: "**"`) en la carpeta de usuario de VS Code |
| `kilocode` | MCP en `~/.config/kilo/opencode.json` y protocolo en `AGENTS.md` | Bloque Nexo en `~/.config/kilo/AGENTS.md` |
| `commandcode` | MCP en `~/.commandcode/mcp.json` y protocolo en `AGENTS.md` | Bloque Nexo en `~/.commandcode/AGENTS.md` |
| `kimi` | MCP en `~/.kimi-code/mcp.json` y protocolo en `AGENTS.md` | Bloque Nexo en `~/.kimi-code/AGENTS.md` (o `$KIMI_CODE_HOME/AGENTS.md`) |

Detalles:

- **Bloque Nexo:** es un texto delimitado por `<!-- BEGIN NEXO PROTOCOL (NX-10) — managed by engram setup -->`
  y `<!-- END NEXO PROTOCOL -->`. Se agrega junto al bloque de Engram, sin tocarlo, y se reemplaza al
  repetir el comando.
- **Carpeta de usuario de VS Code:** `%APPDATA%\Code\User` en Windows,
  `~/Library/Application Support/Code/User` en macOS y `~/.config/Code/User` en Linux.
- **Cursor** no lee reglas globales desde archivos. Abre `~/.cursor/nexo-protocol.md`, copia su
  contenido y pégalo en *Settings > Rules > User Rules* (igual que el protocolo de Engram de
  `~/.cursor/engram-memory-protocol.md`).
- **Claude Code** necesita `claude`, `jq` y `curl` en el PATH. Si falta alguno, `engram setup
  claude-code` se detiene con un mensaje que lo nombra. Si Claude Code ya tenía el plugin, el comando
  lo actualiza.
- **Codex** necesita `codex` en el PATH para instalar el plugin. Si no estaba, el comando te dice qué
  ejecutar: `codex plugin marketplace add Adriel2503/engram-nexo --ref main` y
  `codex plugin add engram@engram`.
- Un error al instalar el protocolo Nexo no hace fallar `engram setup`: verás un aviso que empieza con
  `[nexo] protocolo Nexo no instalado para ...`.

Qué deberías ver al final: el resultado de Engram y, para los asistentes que reciben archivos, líneas
como `[nexo] protocolo Nexo: <ruta>`. En claude-code y codex no aparece esa línea, porque la skill
viene en el plugin.

## 9. Reiniciar y comprobar

1. **Cierra por completo y vuelve a abrir** tu asistente. Así carga el MCP y el protocolo nuevos.
2. Comprueba la conexión desde la terminal:

   ```bash
   engram nexo status
   ```

   Qué deberías ver:

   ```text
   Nexo: conectado a https://<servidor-nexo> como <tu-usuario> desde 2026-10-13 10:30.
   Servidor: https://<servidor-nexo> responde; identidad <tu-usuario>.
   Copia local <tu carpeta>/.nexo/kb.db: 3 espacios, 135 documentos; actualizada 2026-10-13 10:30 (hace 2 min).
   ```

   Con `--json` sale lo mismo en JSON.

3. Pregúntale a tu asistente:

   ```text
   ¿Nexo funciona?
   ```

   Debe responder con una tabla corta: servidor de Nexo (accesible o sin conexión), tu identidad,
   espacios visibles y tu rol en cada uno, copia local (fecha), memoria local y proyecto reconocido.

4. Solo en Claude Code, si quieres confirmar el plugin:

   ```bash
   claude plugin marketplace list     # "engram" con Source: GitHub (Adriel2503/engram-nexo)
   claude plugin list                 # engram@engram habilitado
   ```

Si el asistente dice que no tiene herramientas `nexo_*`, ve a
[Problemas frecuentes](#15-problemas-frecuentes).

## 10. Uso diario

Trabaja normal y pregunta en lenguaje natural. El protocolo le indica al asistente cuándo consultar
Nexo, cómo citar y qué hacer si no hay estándar.

| Quieres | Ejemplo de pregunta | Qué hace el asistente |
|---|---|---|
| Conocer un estándar | "¿Qué estándar de logging usamos?" | Busca en los espacios (`nexo_search`) y responde con el criterio, la cita (espacio, documento, sección, versión, vigencia) y el enlace |
| Saber si existe algo | "¿Hay un estándar para nombrar tópicos de Kafka?" | Si no hay, responde literal: "No existe un estándar oficial publicado sobre este tema." Si te da una idea propia, la marca como recomendación del asistente |
| Una regla en una fecha | "¿Qué regla de nomenclatura de tablas valía el 15 de marzo?" | Busca la versión vigente en esa fecha y aclara si hoy rige otra |
| Lo que estaba publicado | "¿Qué decía Nexo sobre versionado de APIs el 1 de marzo?" | Busca lo publicado a esa fecha (necesita el servidor) |
| Qué cambió | "¿Qué cambió en arquitectura desde el 1 de octubre?" | Lista secciones agregadas, modificadas y retiradas, con fecha y enlace (necesita el servidor) |
| Proponer un estándar | "Esto debería ser estándar para todos los servicios, propónlo" | Te pide confirmación, revisa si ya existe y envía la propuesta. Queda **abierta** hasta que la revise el responsable del espacio. Necesita un token `write` y el servidor |
| Ver tus propuestas | "¿En qué quedaron mis propuestas?" | Muestra su estado y, si se descartaron, el motivo |
| Guardar algo del proyecto | "Recuerda que en este repo usamos Flyway para migraciones" | Lo guarda en la memoria del proyecto y avisa en una línea |
| Retomar una tarea | "¿En qué me quedé?" | Busca las tareas abiertas del proyecto y te resume avances y pendientes |
| Repasar la memoria | "Repasa lo que sabes de este proyecto" | Lista lo guardado por tipo y estado, y ofrece corregir o descartar |

En Claude Code también puedes invocar la skill directamente: `/nexo-protocol estado`,
`/nexo-protocol busca <tema>`, `/nexo-protocol cambios <fecha>`, `/nexo-protocol propuestas`,
`/nexo-protocol cierra`, `/nexo-protocol repaso`. En Codex se usa `$nexo-protocol` en lugar de `/`.

**Memoria de proyecto.** Lo que el asistente guarda del repositorio (decisiones, soluciones, tareas)
queda en tu PC, en `~/.engram/engram.db`. Los estándares oficiales no se guardan en la memoria del
proyecto: se consultan en los espacios y, si algo debería ser estándar, se propone.

**Nunca** escribas secretos (contraseñas, tokens, cadenas de conexión) en lo que pides guardar o
proponer. El asistente los omite y el servidor rechaza propuestas con secretos evidentes.

## 11. Trabajar sin conexión

Nexo guarda en tu PC una copia de solo lectura de los estándares de los espacios que puedes ver, con
sus vigencias: `~/.nexo/kb.db` (o la ruta de `NEXO_HOME` o `NEXO_KB_LOCAL_DB`).

- **Cuándo se actualiza:** al conectar, al arrancar el MCP (cuando abres el asistente), cada 10 minutos
  mientras el asistente está abierto y cuando el servidor vuelve a responder tras una caída. También
  a mano con `engram nexo kb sync`.
- **Cuándo se usa:** si el servidor no responde en 8 segundos (o responde 502, 503 o 504), las
  consultas del asistente salen de la copia. El asistente lo dice: "Respuesta desde la copia local de
  los estándares (actualizada el ..., hace ...); no pude verificarla con el servidor."
- **Qué funciona sin conexión:** buscar y leer estándares, con las vigencias aplicadas según el reloj
  de tu PC (no devuelve lo vencido ni lo que aún no entra en vigor). La memoria del proyecto funciona
  siempre, porque está en tu PC.
- **Qué no funciona sin conexión:** proponer (no se guarda nada y el asistente lo dice), revisar
  propuestas, "qué cambió" y las preguntas sobre lo publicado en una fecha (`known_at`).
- **Seguridad:** si pierdes acceso a un espacio, su contenido se borra de tu copia en la siguiente
  sincronización. Si cambias de servidor o de token, la copia se reconstruye.

Desde la terminal:

```bash
engram nexo kb sync --status                 # estado de la copia, sin conectarse
engram nexo kb sync                          # actualizar ahora (termina bien aunque no haya red)
engram nexo kb search --local "nomenclatura de tablas"
engram nexo kb get --space arquitectura <doc_key>     # usa la copia si el servidor no responde
```

`engram nexo kb search` sin `--local` consulta al servidor y, si no responde, usa la copia local con el aviso "El servidor de Nexo no respondió (...) Respuesta desde la copia local del <fecha>". Con `--known-at` no cae a la copia (no guarda el historial).

## 12. Comandos de terminal

No los necesitas para el uso diario, pero sirven para comprobar o consultar sin el asistente.

| Comando | Qué hace |
|---|---|
| `engram nexo status [--json]` | Conexión, identidad, servidor y copia local |
| `engram nexo connect --server <url> (--token-stdin \| --token <token>)` | Conectar o reconectar (por ejemplo, con un token nuevo) |
| `engram nexo setup <asistente>` | Instalar o reinstalar solo el protocolo Nexo en un asistente |
| `engram nexo disconnect [--keep-copy]` | Desconectar ([sección 14](#14-desconectar-y-desinstalar)) |
| `engram nexo kb spaces [--json]` | Espacios que ves y tu rol en cada uno |
| `engram nexo kb search [--space S] [--category C] [--tag T] [--at AAAA-MM-DD] [--known-at AAAA-MM-DD] [--limit N] [--mode fts\|exact] [--local] [--json] <consulta>` | Buscar. `--limit` por defecto 10 (máximo 50). `--mode exact` para nombres de componentes, códigos o siglas. Si el servidor no responde, usa la copia local y lo avisa |
| `engram nexo kb get --space S [--version N] [--anchor A] [--local] [--json] <doc_key>[#ancla]` | Leer un documento o una sección |
| `engram nexo kb changes --since AAAA-MM-DD [--until AAAA-MM-DD] [--space S] [--clock valid\|recorded]` | Qué cambió desde una fecha |
| `engram nexo kb sync [--status] [--json]` | Actualizar o ver la copia local |
| `engram nexo help` y `engram nexo kb help` | Ayuda completa (`engram help` también lista `nexo`) |

Las consultas de terminal usan el mismo servidor y token que guardó `engram nexo connect`.

## 13. Actualizar a una versión nueva

Cuando el administrador anuncie una versión nueva (`3.4.0-nexo.N`):

1. **Cierra todos tus asistentes.** En Windows, un asistente abierto mantiene `engram.exe` en uso y
   no deja reemplazarlo.
2. Descarga e instala igual que en la [sección 5](#5-instalar-el-binario), cambiando `ver` por la
   versión nueva y **en la misma carpeta** (así la ruta guardada en los asistentes sigue sirviendo).
3. Comprueba con `engram version`.
4. Vuelve a ejecutar `engram setup <asistente>` para cada asistente. En Claude Code y Codex actualiza el
   plugin y su skill; en los demás, reescribe el bloque o archivo de Nexo.
5. Abre el asistente y pregunta "¿Nexo funciona?".

No hace falta volver a conectar: la conexión y la copia local se conservan.

## 14. Desconectar y desinstalar

### Desconectar

```bash
engram nexo disconnect
```

Qué deberías ver:

```text
Desconectado de Nexo: se quitaron la conexión y el token guardados.
Copia local borrada: <tu carpeta>/.nexo/kb.db
Las herramientas nexo_* dejan de aparecer al reiniciar el asistente. La memoria de proyectos de Engram no se toca.
```

Qué hace:

- Borra el marcador `~/.engram/nexo.json`.
- Vacía la URL y el token de `~/.engram/cloud.json` solo si son los de la conexión Nexo. Si `cloud.json` apunta a otro servidor (por ejemplo, un Engram Cloud que no es Nexo) o no existía `nexo.json`, no lo toca y lo dice: "La configuración cloud de Engram (cloud.json, servidor ...) no se tocó porque no es la de la conexión Nexo". Para quitarla: `engram cloud config --clear`.
- Borra la copia local (y sus archivos `-wal` y `-shm`). Con `--keep-copy` la conserva.

Qué **no** hace (queda instalado):

- El binario `engram`.
- La memoria de proyectos (`~/.engram/engram.db`).
- El plugin `engram` de Claude Code y Codex, los registros MCP y la skill o el bloque Nexo de cada
  asistente. Sin conexión, las herramientas `nexo_*` ya no aparecen, pero el texto del protocolo
  sigue ahí (le indica al asistente que diga que Nexo no está conectado).

### Desinstalar del todo

Después de `engram nexo disconnect`:

1. Claude Code:

   ```bash
   claude plugin uninstall engram@engram --scope user
   claude plugin marketplace remove engram
   claude mcp remove engram --scope user
   ```

2. Codex: `codex plugin remove engram@engram`, `codex plugin marketplace remove engram` y borra el
   bloque `[mcp_servers.engram]` de `~/.codex/config.toml`.
3. Otros asistentes: borra el bloque entre `<!-- BEGIN NEXO PROTOCOL (NX-10) ...` y
   `<!-- END NEXO PROTOCOL -->` del archivo de la tabla de la [sección 8](#8-configurar-tu-asistente),
   o el archivo propio (`nexo-protocol.md` en Cursor, `nexo.instructions.md` en VS Code, la carpeta
   `skills/nexo-protocol` en OpenCode). En Cursor, quita también el texto de *User Rules*. Si ya no vas
   a usar Engram, quita además su registro MCP `engram` y su bloque de memoria.
4. Borra el binario (`%LOCALAPPDATA%\Programs\engram-nexo` en Windows o `~/.local/bin/engram`) y, si
   la agregaste, su carpeta del PATH.
5. Si tampoco quieres tu memoria de proyectos, borra `~/.engram`. **No se puede recuperar.**

Engram no tiene un comando de desinstalación; estos pasos son manuales.

## 15. Problemas frecuentes

| Síntoma | Causa probable | Solución |
|---|---|---|
| `No se conectó: el token no es válido o venció` | Token mal copiado, vencido o revocado | Pide un token nuevo y repite `engram nexo connect`. Copia el token completo, sin espacios |
| El asistente o `engram nexo status` dice que el token venció, después de semanas funcionando | Los tokens vencen (por defecto a los 90 días) | Pide un token nuevo y vuelve a ejecutar `engram nexo connect`. No hace falta repetir `engram setup` |
| `No se conectó: el token no es de un usuario administrado` | Usaste un token que no es personal | Pide un token personal al administrador |
| `the Claude Code plugin requires jq in PATH ...` (o `curl`) | Falta `jq` o `curl` | Windows: `winget install jqlang.jq` y abre una terminal nueva. macOS: `brew install jq`. Linux: `sudo apt install jq curl`. Luego repite `engram setup claude-code` |
| `claude CLI not found in PATH` | Claude Code no está instalado o no está en el PATH | Instala Claude Code y abre una terminal nueva |
| `marketplace add failed: ...` o `codex plugin marketplace add failed: ...` | Sin acceso al repositorio privado o git sin credenciales | Comprueba `git ls-remote https://github.com/Adriel2503/engram-nexo.git`. Si da 404 o pide usuario, pide acceso o ejecuta `gh auth login` y `gh auth setup-git` |
| `warning: could not register Claude Code user MCP server (...): claude Code MCP conflict ...` | Ya existe un registro MCP `engram` que apunta a otro binario (el oficial, o una ubicación anterior del binario) | `claude mcp remove engram --scope user` y repite `engram setup claude-code` |
| El asistente no tiene herramientas `nexo_*` | La PC no está conectada, el asistente no se reinició, el MCP usa otro binario o `NEXO_KB_MCP=off` | 1) `engram nexo status` debe decir "conectado". 2) Cierra y abre el asistente. 3) `engram version` debe tener `-nexo`. 4) Repite `engram setup <asistente>`. 5) Revisa que `NEXO_KB_MCP` no valga `off` |
| El asistente no sigue el protocolo (no cita, no consulta Nexo) | Se configuró antes de conectar, o en Cursor falta pegar la regla | `engram nexo setup <asistente>` y reinicia. En Cursor, pega `~/.cursor/nexo-protocol.md` en *User Rules*. En Claude Code, confirma que el plugin viene de `Adriel2503/engram-nexo` ([sección 9](#9-reiniciar-y-comprobar)) |
| Las respuestas dicen "Respuesta desde la copia local" | El servidor no responde (red, VPN o servidor caído) | Revisa la VPN o la red. El asistente vuelve al servidor solo cuando responde. `engram nexo status` muestra el motivo |
| `engram nexo status` muestra el usuario o servidor de otra persona, o un token viejo | Quedaron variables `ENGRAM_CLOUD_SERVER` o `ENGRAM_CLOUD_TOKEN` (por ejemplo, del kit anterior). Tienen prioridad sobre `cloud.json` | Bórralas ([sección 3](#3-si-tenías-el-kit-anterior-de-nexo)) y abre una terminal nueva |
| `engram version` no muestra `-nexo` | Hay otro Engram antes en el PATH | [Sección 4](#4-si-ya-tienes-el-engram-oficial) |
| El antivirus bloquea o borra `engram.exe`, o Windows muestra "Windows protegió su PC" | El binario no está firmado y algunos antivirus marcan binarios de Go por error | No desactives el antivirus. Verifica la huella con `checksums.txt` ([sección 5](#5-instalar-el-binario)) y pide a Mesa de ayuda o Seguridad que lo revise y autorice. Si lo descargaste con el navegador, usa `Unblock-File` |
| macOS: "no se puede abrir porque no se puede verificar el desarrollador" | Marca de descarga del navegador | `xattr -d com.apple.quarantine ~/.local/bin/engram`, o descarga con `gh release download` |
| `No se conectó: no se pudo validar la conexión (Sin conexión ...)` | El servidor no responde o la URL está mal | Abre `https://<servidor-nexo>/health` en el navegador: debe responder `{"service":"engram-cloud","status":"ok"}` |
| Al proponer: 403 | Tu token es `read` o no tienes permiso en ese espacio | Pide un token `write` o el rol adecuado al responsable del espacio |

## 16. Dónde queda cada cosa

| Qué | Dónde |
|---|---|
| Binario | `%LOCALAPPDATA%\Programs\engram-nexo\engram.exe` (Windows) o `~/.local/bin/engram` (macOS, Linux), si seguiste este manual |
| Carpeta de datos de Engram | `~/.engram` (o `ENGRAM_DATA_DIR`) |
| Memoria de proyectos | `~/.engram/engram.db` |
| URL y token del servidor | `~/.engram/cloud.json` (permisos 0600) |
| Marcador de conexión (sin token) | `~/.engram/nexo.json` |
| Copia local de los estándares | `~/.nexo/kb.db` (o `$NEXO_HOME/kb.db`, o `NEXO_KB_LOCAL_DB`) |
| Protocolo Nexo | Según el asistente ([sección 8](#8-configurar-tu-asistente)) |

En Windows, `~` es tu carpeta de usuario (`C:\Users\<usuario>`).

## 17. Variables de entorno del cliente

No hace falta definir ninguna. Sirven para casos especiales.

| Variable | Por defecto | Para qué |
|---|---|---|
| `NEXO_TOKEN` | vacía | Token para `engram nexo connect` sin pasarlo como argumento |
| `NEXO_HOME` | `~/.nexo` | Carpeta de Nexo en la PC (la copia local va en `kb.db` dentro de ella) |
| `NEXO_KB_LOCAL_DB` | `$NEXO_HOME/kb.db` o `~/.nexo/kb.db` | Ruta exacta de la copia local |
| `NEXO_KB_MCP` | vacía | `off` apaga las herramientas `nexo_*` en el asistente |
| `NEXO_KB_TIMEOUT_SECONDS` | `8` | Segundos de espera del servidor antes de responder desde la copia local |
| `NEXO_KB_SYNC_MINUTES` | `10` | Cada cuántos minutos se actualiza la copia mientras el asistente está abierto (mínimo 1) |
| `NEXO_KB_SYNC_OVERLAP_SECONDS` | `120` | Ventana de solape de la sincronización |
| `NEXO_KB_MIN_COVERAGE` | `0.3` | Umbral para responder "no existe un estándar oficial" en la copia local |
| `NEXO_KB_MAX_PER_DOC` | `2` | Máximo de secciones de un mismo documento por respuesta en la copia local |
| `ENGRAM_DATA_DIR` | `~/.engram` | Carpeta de datos de Engram (cambia también dónde se guardan `cloud.json` y `nexo.json`) |
| `ENGRAM_CLOUD_SERVER`, `ENGRAM_CLOUD_TOKEN` | vacías | Si están definidas, **reemplazan** la URL y el token de `cloud.json`. No las uses junto con `engram nexo connect` |

Si cambias `NEXO_HOME` o `NEXO_KB_LOCAL_DB`, defínelas como variables de usuario (no solo en una
terminal), porque el asistente arranca el MCP con su propio entorno.
