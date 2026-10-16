# Pruebas aisladas: no cambian PATH, datos de Engram ni instalaciones personales.
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot 'install-nexo-windows.ps1'
if (-not (Test-Path -LiteralPath $installer)) { throw 'Falta el instalador que se quiere probar' }
$binary = Join-Path $PSScriptRoot '..\bin\windows-amd64\engram.exe'
$root = Join-Path ([IO.Path]::GetTempPath()) ('nexo-installer-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
$originalPath = $env:Path
$originalUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')

# El equipo de desarrollo puede tener MCP activos: se simula su detección, nunca se detienen.
function Get-Process { param($Name, $ErrorAction) if ($global:nexoInstallerTestRunning) { [pscustomobject]@{ Id = 123 } } }
function Get-Command { param($Name, $CommandType, $ErrorAction) if ($Name -eq 'engram' -and $global:nexoInstallerTestPrevious) { [pscustomobject]@{ Source = $global:nexoInstallerTestPrevious } } }
function Copy-Item {
    [CmdletBinding()]
    param([string]$LiteralPath, [string]$Destination, [switch]$Force)
    Microsoft.PowerShell.Management\Copy-Item @PSBoundParameters
    if ($global:nexoInstallerTestCorrupt -and $Destination -eq $global:nexoInstallerTestDestination) {
        $global:nexoInstallerTestCorrupt = $false
        [IO.File]::WriteAllText($Destination, 'copy failure fixture')
    }
}

foreach ($case in @('fresh', 'replace', 'detect-existing', 'bad-checksum', 'running', 'missing-binary', 'rollback', 'shim-directory')) {
    $global:nexoInstallerTestCorrupt = $false
    $caseDir = Join-Path $root $case
    New-Item -ItemType Directory -Path $caseDir | Out-Null
    $source = Join-Path $caseDir 'package\engram.exe'
    New-Item -ItemType Directory -Path (Split-Path -Parent $source) | Out-Null
    Copy-Item -LiteralPath $binary -Destination $source
    $hash = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash
    $checksum = if ($case -eq 'bad-checksum') { '0' * 64 } else { $hash }
    Set-Content -LiteralPath ($source + '.sha256') -Value $checksum -Encoding ascii
    $destDir = Join-Path $caseDir 'installed'
    if ($case -eq 'shim-directory') { $destDir = Join-Path $caseDir 'shims' }
    $dest = Join-Path $destDir 'engram.exe'
    $global:nexoInstallerTestPrevious = $null
    $global:nexoInstallerTestRunning = $case -eq 'running'
    if ($case -in @('replace', 'detect-existing', 'rollback')) {
        New-Item -ItemType Directory -Path $destDir | Out-Null
        Set-Content -LiteralPath $dest -Value 'previous executable fixture'
        $before = (Get-FileHash -LiteralPath $dest).Hash
        if ($case -eq 'detect-existing') { $global:nexoInstallerTestPrevious = $dest }
    }
    $global:nexoInstallerTestDestination = $dest
    $global:nexoInstallerTestCorrupt = $case -eq 'rollback'
    if ($case -eq 'missing-binary') { $source = Join-Path $caseDir 'absent.exe' }
    $argsForInstall = @{ SourcePath = $source; InstallDirectory = $destDir; NoPath = $true; SkipDependencies = $true }
    $failed = $false
    try { $result = & $installer @argsForInstall } catch { $failed = $true }
    $mustFail = $case -in @('bad-checksum', 'running', 'missing-binary', 'rollback', 'shim-directory')
    if ($failed -ne $mustFail) { throw "Resultado incorrecto: $case" }
    if (-not $mustFail) {
        if ($result.InstalledPath -ne $dest) { throw "Destino incorrecto: $case" }
        if ($case -eq 'detect-existing' -and $result.PreviousPath -ne $dest) { throw 'No detecto la instalacion anterior' }
        if ((Get-FileHash -LiteralPath $dest).Hash -ne $hash) { throw "Copia incorrecta: $case" }
        if ($case -in @('replace', 'detect-existing')) {
            if (-not $result.BackupPath -or (Get-FileHash -LiteralPath $result.BackupPath).Hash -ne $before) { throw "Respaldo incorrecto: $case" }
        }
    } elseif ($case -eq 'rollback') {
        if ((Get-FileHash -LiteralPath $dest).Hash -ne $before) { throw 'No restauro el ejecutable tras fallo de copia' }
    } elseif (Test-Path -LiteralPath $dest) { throw "Se modificó el destino al fallar: $case" }
    Write-Host "PASS $case"
}
if ($env:Path -ne $originalPath -or [Environment]::GetEnvironmentVariable('Path', 'User') -ne $originalUserPath) { throw 'Las pruebas cambiaron PATH' }

# Regresion real: -File evalua los parametros de forma distinta de & script.
# No pasar SourcePath: debe encontrarse junto al instalador incluso desde otro directorio.
$engines = @((Join-Path $env:WINDIR 'System32\WindowsPowerShell\v1.0\powershell.exe'))
$pwsh = Microsoft.PowerShell.Core\Get-Command pwsh -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($pwsh) { $engines += $pwsh.Source }
foreach ($engine in $engines) {
    $cliDestination = Join-Path $root ([IO.Path]::GetFileNameWithoutExtension($engine) + '-file')
    Push-Location $root
    $savedErrorAction = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & $engine -NoProfile -ExecutionPolicy Bypass -File $installer -InstallDirectory $cliDestination -NoPath -SkipDependencies 2>&1
        $exitCode = $LASTEXITCODE
    } finally { $ErrorActionPreference = $savedErrorAction; Pop-Location }
    $message = ($output | ForEach-Object { "$_" }) -join "`n"
    if ($exitCode -ne 0 -and $message -notmatch 'Hay procesos de Engram') {
        throw "Fallo -File sin SourcePath con $engine : $message"
    }
    if ($exitCode -eq 0 -and -not (Test-Path -LiteralPath (Join-Path $cliDestination 'engram.exe'))) {
        throw 'El comando -File no instalo en el destino temporal'
    }
    Write-Host "PASS -File sin SourcePath: $engine"
}
Write-Host 'Pruebas correctas. Solo se crearon archivos en carpetas temporales.'
