# Publicar conocimiento desde GitHub Actions (plantilla opcional, D-04)

Estado: plantilla de soporte a futuro (NX-10, entrega 2b tanda 2). **No es el flujo principal.**
Por decisión del usuario (D-K12, 2026-10-13) el responsable o publicador de un espacio sube los
archivos desde el panel (`/dashboard/kb`) o con `engram nexo kb upload`; git y GitHub no son
requisito. Esta plantilla sirve a los equipos que ya mantienen sus lineamientos en un repositorio de
GitHub y quieren que cada merge a `main` publique solo.

> **Requiere `NEXO_SERVICE_ACCOUNTS_ENABLED=true` en el servidor.** Las cuentas de servicio están
> apagadas por defecto en la beta: sin esa variable el servidor no crea la cuenta ni emite su token
> (`403 service_accounts_disabled`) y los tokens que ya existían no autentican. Active la variable
> solo si va a usar esta plantilla; antes de eso, publique desde el panel o con `engram nexo kb upload`.

Archivo: `publicar-conocimiento.yml` (esta carpeta). Se copia a
`.github/workflows/publicar-conocimiento.yml` del repositorio de lineamientos.

## Qué hace

| Evento | Paso | Efecto |
|---|---|---|
| `pull_request` hacia `main` que toca `docs/**` o `nexo-kb.yaml` | `engram nexo kb upload --space <S> --dry-run <carpeta>` | No escribe nada. El resultado por documento (creado, actualizado, sin cambios, rechazado y por qué, por ejemplo un secreto detectado) queda en el resumen del job para revisarlo en el PR |
| `push` a `main` (merge) o ejecución manual | `engram nexo kb upload --space <S> <carpeta>` | Publica. Es idempotente por hash: lo que no cambió no crea versiones ni eventos |

La subida es la misma API que usan el panel y la CLI (`POST /nexo/kb/v1/spaces/{slug}/uploads`), con
las mismas defensas y el mismo registro de ejecuciones (`nexo_kb_publish_runs`, KP-02). El token
identifica a quien publica (RN-K5): todo queda atribuido a la cuenta de servicio del CI.

## Configuración del repositorio

| Tipo | Nombre | Valor |
|---|---|---|
| Secreto | `NEXO_KB_TOKEN` | Token de una **cuenta de servicio** con rol **publicador** en el espacio, alcance `write` y, si la política lo permite, con caducidad (NX-07 S3). Nunca un token personal ni de administrador |
| Variable | `NEXO_SERVER` | URL de Nexo (`https://...`) |
| Variable | `NEXO_KB_SPACE` | Slug del espacio |
| Variable | `NEXO_KB_DIR` | Carpeta con los documentos (opcional; por defecto `docs`) |
| Variable | `NEXO_FORK_REF` | Rama o etiqueta del fork a compilar (opcional; por defecto `develop`; conviene fijar una etiqueta `v3.4.0-nexo.N`) |

El token llega al binario solo por la variable de entorno `ENGRAM_CLOUD_TOKEN` del paso que lo usa;
nunca va en la línea de comandos (quedaría en los registros) ni en archivos. GitHub enmascara el
secreto en la salida. El servidor llega por `ENGRAM_CLOUD_SERVER`.

Alta de la cuenta de servicio (administrador de Nexo, SG-10): `engram nexo sa create --slug ci-<repo>
--owner <usuario>`, el responsable del espacio le da el rol con `engram nexo sa grant ci-<repo> --space
<slug> --role publicador` y el admin emite el token con `engram nexo sa token issue ci-<repo> --out
token.txt` (también desde el panel, `/dashboard/admin/service-accounts`). Una cuenta de servicio nunca
puede ser responsable (NX-05, R-10). Rotación y último uso: `docs/nexo/manuales/administracion.md` §3.8.

## Binario del fork

Las herramientas `engram nexo kb` solo existen en el fork `engram-nexo`.

- **Opción A (hoy):** el workflow clona el fork (`<organizacion>/engram-nexo`, reemplazar por la
  ubicación real) y lo compila con `actions/setup-go` y una versión de Go fijada (`1.27.2`, la de
  `go.mod`). Si el fork es privado, el `checkout` necesita un token de lectura de ese repositorio
  (otro secreto, por ejemplo `ENGRAM_NEXO_READ_TOKEN` en `with.token`).
- **Opción B (cuando exista un release, OP-01):** descargar el binario del release y verificar su
  SHA-256 antes de usarlo; reemplaza los pasos de compilación (está comentada en el workflow).

## Seguridad del workflow

- `permissions: contents: read`: el job no puede escribir en el repositorio.
- `concurrency`: nunca dos publicaciones del mismo espacio a la vez; un PR nuevo cancela su dry-run
  anterior; una publicación en curso no se cancela.
- Las acciones están fijadas por versión mayor (`@v4`, `@v5`). Recomendado: fijarlas por el SHA
  completo del commit y actualizarlas con Dependabot.
- Los PR desde forks externos no reciben secretos en `pull_request`: el dry-run fallará por falta de
  token (es lo esperado). No cambiar a `pull_request_target`, que ejecutaría código del PR con
  secretos.

## Variante para repositorios git: `publish --prune --repo`

Si el repositorio es la fuente única del espacio (o de una parte), se puede usar `publish` en lugar de
`upload`. El origen se registra como git (repositorio, rama, commit, autor del run) a partir de las
variables de GitHub Actions, y la **poda** retira los documentos de ese mismo repositorio que ya no
vienen en el lote (no los borra; los documentos subidos a mano u otros orígenes no se tocan, RF-13):

```bash
# PR
"$RUNNER_TEMP/engram" nexo kb publish --space "$NEXO_KB_SPACE" --dry-run --prune --repo "$GITHUB_REPOSITORY" "$NEXO_KB_DIR"
# main
"$RUNNER_TEMP/engram" nexo kb publish --space "$NEXO_KB_SPACE" --prune --repo "$GITHUB_REPOSITORY" "$NEXO_KB_DIR"
```

Diferencias con `upload`: `publish` lee `.md` y `.txt` (no `.docx` ni wikis exportadas) y acepta un
`nexo-kb.yaml` en la carpeta base con valores por defecto (`space`, `category`, `owner`, `tags`,
`repo`). La poda solo se aplica con el lote completo y tiene un tope de seguridad (KP-01): si
retiraría más del 25 % de los documentos del origen, se detiene y exige `--confirm-prune`. No
agregar `--confirm-prune` al workflow de forma fija: si la poda se detiene, revisarlo a mano.

Con `upload` también hay poda, por colección (`--prune --collection <nombre>`), con el mismo tope.

## Verificación

1. Abrir un PR que cambie un documento: el resumen del job muestra `dry-run` con `actualizados 1`.
2. Hacer merge: el resumen muestra la publicación; en Nexo, `engram nexo kb search` o el panel
   muestran la versión nueva con el commit en su origen (si se usa `publish`).
3. Repetir el run sin cambios: `sin cambios N`, ninguna versión nueva.
