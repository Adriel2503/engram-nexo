#Requires -Version 5.1
<#
.SYNOPSIS
Instala el ejecutable incluido de Nexo/Engram, sin Go ni compilacion.
.DESCRIPTION
Reemplaza un exe anterior cuando su carpeta permite escritura; conserva respaldo.
No sobrescribe shims de gestores ni elimina memoria/configuracion. Cierra asistentes antes.
.PARAMETER InstallDirectory
Carpeta de destino opcional. Sin ella se reutiliza la instalacion directa encontrada o .nexo-bin.
.PARAMETER NoPath
Instalacion portable: no modifica PATH. Los comandos finales usan la ruta completa.
.PARAMETER SkipDependencies
No instala dependencias con winget. Util para equipos administrados o herramientas ya instaladas.
#>
[CmdletBinding()]
param(
    [string]$SourcePath,
    [string]$InstallDirectory,
    [string]$ServerUrl = 'https://nexo-34-51-82-225.sslip.io',
    [switch]$NoPath,
    [switch]$SkipDependencies
)
$ErrorActionPreference = 'Stop'

# En Windows PowerShell -File, PSScriptRoot puede estar vacio al evaluar param().
# Resuelve el valor predeterminado en el cuerpo del script, no en la firma.
if ([string]::IsNullOrWhiteSpace($SourcePath)) {
    $SourcePath = Join-Path $PSScriptRoot '..\bin\windows-amd64\engram.exe'
}

function Get-NexoApplication([string]$Name) {
    Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
}

function Test-NexoWritableDirectory([string]$Directory) {
    $probe = Join-Path $Directory ('.nexo-write-' + [guid]::NewGuid().ToString('N'))
    try {
        New-Item -ItemType Directory -Force -Path $Directory | Out-Null
        [IO.File]::WriteAllText($probe, '')
        return $true
    } catch { return $false }
    finally { if ([IO.File]::Exists($probe)) { [IO.File]::Delete($probe) } }
}

function Get-NexoBinaryVersion([string]$Path) {
    $output = & $Path version 2>&1
    if ($LASTEXITCODE -ne 0) { throw 'El ejecutable no pudo responder a version.' }
    return (($output | ForEach-Object { "$_" }) -join [Environment]::NewLine).Trim()
}

function Install-NexoDependency([string]$Command, [string]$Package) {
    if (Get-NexoApplication $Command) { return }
    if (-not (Get-NexoApplication 'winget')) {
        throw "Falta $Command y no hay winget. Instala $Package y vuelve a ejecutar."
    }
    Write-Host "Instalando dependencia: $Package"
    & winget install --id $Package --exact --source winget | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "No se pudo instalar $Package." }
    $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' +
        [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (Get-NexoApplication $Command)) {
        throw "$Package se instalo, pero $Command no esta disponible. Abre otra terminal y repite."
    }
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'Este instalador es para Windows.' }
if (-not [Environment]::Is64BitOperatingSystem) { throw 'Se necesita Windows de 64 bits.' }
if ($ServerUrl -notmatch '^https://[A-Za-z0-9.-]+(?::[0-9]+)?/?$') {
    throw 'ServerUrl debe ser una URL base HTTPS, sin credenciales, rutas ni parametros.'
}
$ServerUrl = $ServerUrl.TrimEnd('/')
$source = (Resolve-Path -LiteralPath $SourcePath).Path
$expected = (Get-Content -LiteralPath ($source + '.sha256') -Raw).Trim()
if ($expected -notmatch '^[a-fA-F0-9]{64}$' -or
    (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash -ine $expected) {
    throw 'La huella SHA256 no coincide. Vuelve a descargar el paquete completo.'
}
if (Get-Process engram -ErrorAction SilentlyContinue) {
    throw 'Hay procesos de Engram abiertos. Cierra sus asistentes y vuelve a ejecutar; no se detuvieron procesos.'
}
$version = Get-NexoBinaryVersion $source
$previous = Get-NexoApplication 'engram'
$previousPath = if ($previous) { $previous.Source } else { $null }
if ($previousPath) { Write-Host "Engram detectado: $previousPath" }
else { Write-Host 'No se encontro Engram en el PATH: instalacion nueva.' }

if ($InstallDirectory) {
    $directory = [IO.Path]::GetFullPath($InstallDirectory)
} elseif ($previousPath -and [IO.Path]::GetExtension($previousPath) -ieq '.exe' -and
    $previousPath -notmatch '(?i)[\\/](shims|chocolatey[\\/]bin)[\\/]' -and
    (Test-NexoWritableDirectory (Split-Path -Parent $previousPath))) {
    $directory = Split-Path -Parent $previousPath
} else {
    $directory = Join-Path $HOME '.nexo-bin'
    if ($previousPath) { Write-Host 'La instalacion anterior se conserva; se usara una carpeta propia para no sobrescribir un lanzador o una ruta protegida.' }
}
if ($directory -match '(?i)[\\/](shims|chocolatey[\\/]bin)([\\/]|$)') {
    throw 'No instales sobre los shims de un gestor de paquetes; elige otra carpeta.'
}
if (-not (Test-NexoWritableDirectory $directory)) { throw "No se puede escribir en $directory." }
$destination = Join-Path $directory 'engram.exe'
if ([IO.Path]::GetFullPath($source) -ieq [IO.Path]::GetFullPath($destination)) {
    throw 'El destino es el mismo archivo del repositorio. Elige otra carpeta con -InstallDirectory.'
}

# Las dependencias se instalan solo tras comprobar paquete, procesos y destino.
if (-not $SkipDependencies) {
    Install-NexoDependency 'git' 'Git.Git'
    if (Get-NexoApplication 'claude') {
        Install-NexoDependency 'jq' 'jqlang.jq'
        Install-NexoDependency 'curl' 'cURL.cURL'
    }
}

$backup = $null
$stage = Join-Path $directory ('engram-install-' + [guid]::NewGuid().ToString('N') + '.exe')
$hadDestination = Test-Path -LiteralPath $destination
try {
    Copy-Item -LiteralPath $source -Destination $stage
    if ($hadDestination) {
        $backup = $destination + '.backup-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N')
        Copy-Item -LiteralPath $destination -Destination $backup
    }
    Copy-Item -LiteralPath $stage -Destination $destination -Force
    if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash -ine $expected) {
        throw 'La copia instalada no coincide con la huella.'
    }
    $version = Get-NexoBinaryVersion $destination
} catch {
    if ($backup) { Copy-Item -LiteralPath $backup -Destination $destination -Force }
    elseif (-not $hadDestination -and [IO.File]::Exists($destination)) { [IO.File]::Delete($destination) }
    throw
} finally { if ([IO.File]::Exists($stage)) { [IO.File]::Delete($stage) } }

if (-not $NoPath) {
    $oldPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $otherPaths = @($oldPath -split ';' | Where-Object {
        $_ -and $_.TrimEnd('\') -ine $directory.TrimEnd('\')
    })
    [Environment]::SetEnvironmentVariable('Path', (@($directory) + $otherPaths) -join ';', 'User')
    $env:Path = $directory + ';' + $env:Path
}

$quoted = "'" + $destination.Replace("'", "''") + "'"
Write-Host "Instalado: $destination"
Write-Host "Version: $version"
if ($backup) { Write-Host "Respaldo: $backup" }
Write-Host 'La memoria y las credenciales existentes no se borraron. No se inicio un servidor.'
Write-Host ''
Write-Host 'Abre una terminal nueva. Para comprobar la instalacion:'
Write-Host "& $quoted version"
Write-Host 'Get-Command engram | Select-Object -ExpandProperty Source'
Write-Host ''
Write-Host 'Copia tu codigo de activacion al portapapeles y ejecuta:'
Write-Host "Get-Clipboard | & $quoted nexo connect --server $ServerUrl --activate-stdin"
Write-Host 'Si ya tienes un token personal, usa --token-stdin en lugar de --activate-stdin.'
Write-Host "& $quoted nexo status"
Write-Host ''
Write-Host 'Instala la integracion de cada asistente que uses y reinicialo:'
foreach ($agent in @(@('Codex','codex'), @('Claude Code','claude-code'), @('OpenCode','opencode'))) {
    Write-Host ($agent[0] + ': & ' + $quoted + ' setup ' + $agent[1])
}
Write-Host 'Los asistentes se instalan aparte. Los plugins de Codex/Claude necesitan acceso al fork en GitHub.'
Write-Host 'Si Claude tenia un MCP anterior y muestra conflicto: claude mcp remove engram --scope user; repite setup claude-code.'
Write-Host 'Si Get-Command muestra otra ruta, utiliza los comandos con ruta completa anteriores y revisa el PATH de sistema.'

[pscustomobject]@{ InstalledPath = $destination; BackupPath = $backup; PreviousPath = $previousPath; Version = $version }
