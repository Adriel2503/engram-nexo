# Ejecutable Windows AMD64

Entrega puntual del cliente y servidor Engram/Nexo, para Windows Intel/AMD de 64 bits.
El usuario final no necesita instalar Go ni compilar. Sigue la
[instalación del README principal](../../README.md#cliente-windows-sin-compilar).

| Dato | Valor |
|---|---|
| Código fuente | `main`, commit `f4d24ea9b1b21d609d837c44fde24cadf2163549` |
| Estado al compilar | Árbol limpio (`vcs.modified=false`) |
| Compilador | Go 1.27.2 |
| Plataforma | `windows/amd64` |
| Versión del programa | `3.4.1-0.20261016232305-f4d24ea9b1b2` |
| Huella | Archivo `engram.exe.sha256`, SHA256 del ejecutable sin comprimir |

El commit que incorpora este archivo será posterior al commit de compilación: el programa no
puede incluir en sus metadatos el hash del commit que todavía no existía al generarlo.
Entre ambos no hay cambios al código Go del programa.

Comprobaciones en PowerShell, desde esta carpeta:

```powershell
$esperado = (Get-Content -LiteralPath '.\engram.exe.sha256' -Raw).Trim()
$actual = (Get-FileHash -LiteralPath '.\engram.exe' -Algorithm SHA256).Hash
if ($actual -ine $esperado) { throw 'La huella del ejecutable no coincide' }
.\engram.exe version
```

Este binario no contiene tokens ni la configuración privada del servidor. Las conexiones y
credenciales se configuran en cada instalación. La huella detecta una copia incorrecta; no es
una firma digital de Windows.

Se incluye en Git por petición expresa del usuario para esta entrega, como excepción a la
política general de artefactos. No se modificó la comprobación de binarios del CI; las siguientes
entregas deberían volver a distribuirse como archivos de una versión.
