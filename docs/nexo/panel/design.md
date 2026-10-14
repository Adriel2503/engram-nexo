# Panel web de Nexo IA: diseño

Estado: implementado para la presentación (2026-10-13, sin commit). Alcance: login, menú y páginas de
conocimiento (`/dashboard/kb`, espacio, documento). Las demás páginas de Engram heredan el tema sin
rediseño.

## 1. Tema y audiencia

Nexo IA es la biblioteca de estándares de ingeniería de Real Plaza (operador de centros comerciales en
Perú) y la memoria compartida de los asistentes de IA del equipo.

| Quién | Qué viene a hacer |
|---|---|
| Desarrollador | Encontrar la regla vigente sobre un tema, con su fuente, versión y vigencia, y saber si no existe |
| Responsable de un espacio (arquitectura, frontend, seguridad) | Publicar y actualizar documentos (Markdown, Word, carpeta o zip) y retirar los que ya no rigen |
| Administrador | Crear espacios, nombrar responsables y revisar quién hace qué |

El trabajo principal es **encontrar la regla vigente con su fuente**. El segundo es **publicar**.

## 2. Trabajo principal de cada página

| Página | Trabajo principal | Lo demás |
|---|---|---|
| Login | Entrar con el token asignado | Identificar Nexo IA y Real Plaza; enlace de activación |
| Menú | Llegar a Conocimiento en un clic | Memorias, proyectos, colaboradores y administración de Engram |
| `/dashboard/kb` | Elegir un espacio | Entender los 4 pasos del flujo y en cuál está el usuario; crear espacio (admin) |
| Espacio | Preguntar y ver la respuesta citada | Recorrer los documentos por carpeta; subir (responsable) |
| Documento | Leer la sección citada | Estado, vigencia, versiones y qué cambió entre versiones |

## 3. Wireframes

Login (una columna centrada, también en móvil; simplificado el 2026-10-14):

```
               Nexo IA
              Real Plaza

       +---------------------------+
       | Iniciar sesión            |
       | Ingresa con tu token      |
       | de acceso.                |
       | Token de acceso           |
       | [ Pega aquí tu token    ] |
       | [ Iniciar sesión        ] |
       | Activar mi cuenta         |
       +---------------------------+
```

Marco común (encabezado, menú, contenido alineado a la izquierda, ancho máximo 1180 px):

```
Nexo IA  Memoria y estándares del equipo                usuario  Cerrar sesión
Inicio   Conocimiento   Memorias   Proyectos   Colaboradores   Administración
------------------------------------------------------------------------------
<contenido>
```

`/dashboard/kb`:

```
Espacios de conocimiento
Texto de una línea: para qué sirven.

Cómo funciona
 1 Crear un espacio          Hecho / Pendiente / No aplica a tu rol   [acción]
 2 Nombrar responsable ...
 3 Subir documentos ...
 4 Consultar ...

+-------------------+ +-------------------+
| Arquitectura      | | Frontend          |   tarjetas: nombre, visibilidad,
| Abierto. Tu rol   | | ...               |   tu rol, vigentes, retirados,
| 187 vigentes ...  | |                   |   última actualización
+-------------------+ +-------------------+
```

Espacio:

```
Arquitectura
Descripción. Abierto. Tu rol: responsable. 187 vigentes, 12 retirados.

[ ¿Qué quieres saber? ......................... ] [en fecha] [Buscar]

  Respuesta citada (elemento memorable)
  Documento > Sección > Subsección                Vigente  desde 2026-01-01
  | "fragmento marcado como con resaltador"
  Abrir en el documento

Documentos  [Todos] [Vigentes] [Retirados]        | Subir documentos
> carpeta-a (12)                                   | (solo responsable/publicador)
    Título del documento        Vigente            | archivos, carpeta o zip
    ruta/en/gris.md                                | [Probar sin publicar] [Publicar]
> carpeta-b (40)                                   | Últimas subidas
```

Documento:

```
Espacio > carpeta
Título del documento
Vigente  versión 3 de 3  desde 2026-01-01  publicado por X

Texto renderizado (máximo 68 caracteres por línea, serif de lectura)
## Sección   (ancla para las citas)

Versiones: v3 (qué cambió) v2 (qué cambió) v1
```

## 4. Sistema de tokens

Colores (archivo `internal/cloud/dashboard/static/nexo_theme.css`):

| Nombre | Hex | Uso |
|---|---|---|
| Papel | `#F3F5F4` | Fondo de la página (gris frío, no crema) |
| Superficie | `#FFFFFF` | Tarjetas de espacio, respuesta, formularios |
| Tinta | `#17231F` | Texto principal (verde casi negro, no `#111`) |
| Tinta suave | `#4A5853` | Texto secundario y rutas (7:1 sobre papel) |
| Línea | `#D3DBD7` | Separadores y bordes de campos |
| Verde norma | `#0E5E4B` | Acción principal, enlaces y pestaña activa (sello de "aprobado") |
| Resaltador | `#FBEEA6` | Solo el fragmento citado de la respuesta |

Estados (texto oscuro sobre fondo claro, AA): Vigente `#1E6B3A`/`#E2F0E6`, Programado `#22518A`/`#E3EBF5`,
Vencido `#8A4B00`/`#FAEAD5`, Retirado `#4D5552`/`#E6EAE8`.

Tipografía, sin descargas (pilas del sistema elegidas: los equipos usan Windows):

| Rol | Pila |
|---|---|
| Interfaz | `"Segoe UI Variable Text", "Segoe UI", system-ui, -apple-system, "Helvetica Neue", Arial, sans-serif` |
| Lectura y títulos | `"Sitka Text", "Iowan Old Style", Charter, "Bitstream Charter", Georgia, serif` |
| Código | `"Cascadia Mono", Consolas, "SFMono-Regular", Menlo, monospace` (solo código) |

Escala (razón 1,25, base 16 px): 13 / 14 / 16 / 20 / 25 / 31 / 39. Texto de documento 17 px con
interlineado 1,65 y ancho máximo 68ch. Todo en tipo oración; sin mayúsculas sostenidas ni espaciado de
letras en etiquetas.

Componentes: encabezado, pestañas de texto con subrayado de 3 px, botón principal (verde lleno),
botón secundario (contorno), chip de estado, tarjeta de espacio, lista de pasos numerada, árbol de
carpetas (`<details>`), bloque de respuesta citada, formulario de subida con zona de archivos.

## 5. Principios

1. **La fuente siempre a la vista.** Toda regla aparece con su documento, ruta de títulos, versión,
   estado y vigencia. El elemento memorable es la respuesta citada con el fragmento resaltado.
2. **Estado en palabras.** El color solo marca el estado (chips) y la acción principal; nada decorativo.
3. **Estructura con espacio, no con cajas.** Se separa con blanco y líneas finas; solo son tarjetas los
   objetos que se eligen (espacios) y la respuesta. Radios distintos por jerarquía (4 px controles,
   8 px tarjetas).

## 6. Revisión contra clichés (cambios al plan)

- El panel de Engram usa fondo casi negro, acento morado, monospace en etiquetas, etiquetas en
  mayúsculas espaciadas y bordes en todo: el tema Nexo los reemplaza (modo claro, sans del sistema en
  etiquetas, tipo oración, bordes solo en campos y tarjetas).
- Plan inicial con títulos en serif de alto contraste sobre fondo cálido: se cambió a papel gris frío y
  una serif de lectura (Sitka/Charter) para que no caiga en "crema + serif + terracota".
- Plan inicial con tarjetas para cada paso de "Cómo funciona": se cambió a una lista numerada (es una
  secuencia real) con el estado en texto; tarjetas solo para los espacios.
- Sin degradados, sin sombras repetidas (solo una sombra suave en la respuesta citada), sin flechas en
  botones, sin separadores con punto medio.
- La franja "CLOUD ACTIVE" de Engram queda como una línea discreta en español ("Conectado").

## 7. Accesibilidad y respuesta

Foco visible (contorno de 3 px verde con separación), contraste AA en todo texto, `prefers-reduced-motion`
sin transiciones, etiquetas en todos los campos, `lang="es"`, sin desborde horizontal a 1366 px ni en
móvil (la lista de documentos no es tabla; las tablas restantes se envuelven en contenedores con
desplazamiento propio).

## 8. Cambios tras la revisión con capturas

Capturas en `nexo_ia/06-pruebas/conocimiento/evidencia/panel/` (Chrome sin interfaz, 1366 px y 500 px).

- La columna principal se ensanchaba con el buscador (scroll horizontal): `grid-template-columns: minmax(0, 1fr)`.
- "Cerrar sesión" salía como botón principal: pasa a enlace.
- Pico daba sombra y margen de tarjeta a `<article>` y repartía los filtros del `<nav>` a lo ancho: se anulan.
- La lista por carpetas plana tenía 30 filas con rutas largas: pasa a árbol anidado (carpetas con una sola subcarpeta se muestran en una línea; el primer nivel abierto).
- El fragmento citado mezclaba la negrita del documento con el resaltado del buscador: solo se resaltan términos sueltos y se limpian barras de tabla y comillas de código.
- Los bloques de código dentro de listas conservaban la sangría: se quita la sangría común.
- Botones "Retirar" y de elegir archivo más discretos.

## 9. Fase K6: miembros, propuestas y panel en español

Estado: implementado en la rama `feat/panel-k6` (2026-10-13), diseño escrito antes de implementar. Cubre KP-12 (miembros de un espacio), KP-13
(propuestas), SG-07 (pantallas de revisión) y OP-06 (textos en inglés del panel de Engram). No hay estilo
nuevo: se reutilizan los tokens, el encabezado, las pestañas de texto, la lista de documentos, los chips y los
formularios de las secciones 4 a 7. Solo se agregan unas pocas reglas a `nexo_theme.css` (lista de miembros,
comparación lado a lado, insignia de la pestaña).

### 9.1 Trabajo principal de cada pantalla

| Pantalla | Quién la usa | Trabajo principal | Lo demás |
|---|---|---|---|
| Miembros del espacio | Responsable del espacio, administrador; lectura para el resto con acceso | Saber quién tiene cada rol y cambiarlo sin salir del panel | Ver desde cuándo y si es persona o cuenta de servicio |
| Propuestas (lista) | Revisor (publicador, responsable, administrador con rol en el espacio); autor | Ver qué falta revisar | Historial de aprobadas, descartadas y retiradas |
| Propuesta (detalle) | Revisor | Comparar el texto propuesto con la versión vigente y decidir | Ver el motivo del autor, la vigencia propuesta y el rastro de la decisión |
| Panel de Engram (usuarios, tokens, salud, auditoría) | Administrador | Lo mismo que antes, en español | Sin cambios de comportamiento |

### 9.2 Navegación

El espacio gana una fila de secciones bajo su encabezado, con el mismo estilo de los filtros de documentos:
Documentos, Miembros, Propuestas (con el número de propuestas abiertas si hay). El menú común gana la
pestaña Propuestas, con una insignia que solo ven los revisores y que cuenta lo pendiente en todos sus
espacios (se pide con htmx al cargar la página; sin permisos o sin pendientes no muestra nada).

```
Conocimiento / arquitectura
Arquitectura
Descripción. Abierto. Tu rol: responsable.
[ Documentos ] [ Miembros ] [ Propuestas 3 ]
```

### 9.3 Wireframes

Miembros (una columna; en pantallas anchas la lista y el formulario de alta van lado a lado, como la subida de documentos):

```
Miembros de Arquitectura
Abierto: quienes no aparecen aquí tienen el rol lector. Solo el responsable y los administradores cambian roles.

Responsables (1)
  Ana Torres              Persona, desde 2026-10-01 por Carla     [ responsable v ] [Cambiar rol] [Quitar]
  ana
Publicadores (2)
  ci-lineamientos         Cuenta de servicio, desde 2026-10-05    [ publicador  v ] [Cambiar rol] [Quitar]
  ...
                                                        | Agregar un miembro
                                                        | Usuario o cuenta  [ ana              ]
                                                        | Rol               [ publicador     v ]
                                                        | [Agregar]
```

Sin permiso para cambiar: la misma lista sin selector, botones ni formulario de alta. Sin `space.read` (ni
lectura de directorio): 404 como el resto del espacio.

Propuestas (lista):

```
Propuestas de Arquitectura                       [ Pendientes ] [ Aprobadas ] [ Descartadas ] [ Retiradas ]
  Regla de trazas en servicios            Pendiente
  Cambia logging, versión 3. Propuso Luis el 2026-10-12
  Guía nueva de despliegue                Pendiente
  Documento nuevo. Propuso Ana el 2026-10-10
```

Propuesta (detalle):

```
Conocimiento / arquitectura / propuestas
Regla de trazas en servicios                          Pendiente
Propuso Luis el 2026-10-12. Cambia logging (versión 3). Entra en vigencia el 2026-11-01.
Motivo del autor: texto breve.
[ aviso si el documento cambió desde la versión que vio el autor ]

+--------------------------+  +--------------------------+   | Decidir
| Versión vigente (v3)     |  | Propuesta                |   | Nota para el autor (opcional)
| texto del documento      |  | texto propuesto          |   | Vigente desde / hasta (opcional)
+--------------------------+  +--------------------------+   | [Aprobar y publicar]
                                                             | Motivo del descarte (obligatorio)
                                                             | [Descartar]
```

El texto se muestra con `mdhtml` (Markdown sin HTML crudo). Si la propuesta es un documento nuevo, solo se
muestra la columna de la propuesta. Una propuesta ya decidida muestra quién la cerró, cuándo y la nota, y
no ofrece acciones.

### 9.4 Decisiones

- Los formularios de miembros y de decisión repiten la página en el mismo POST con el mensaje de éxito o de
  error, sin perder el contexto (igual que cuentas de servicio). Los errores de reglas salen en español y
  dicen qué hacer: R-10 ("Una cuenta de servicio no puede ser responsable..."), R-9 (último responsable),
  R-11 (subirse el rol a uno mismo), usuario no encontrado, propuesta ya cerrada y conflicto de versión.
- Aprobar sobre un documento que cambió exige marcar "Aprobar aunque el documento cambió"; la casilla solo
  aparece cuando hay diferencia o tras el conflicto. Descartar exige motivo.
- El directorio de usuarios (para sugerir nombres al agregar un miembro) solo se carga si quien mira puede
  leer el directorio (administrador, auditor). El responsable escribe el nombre de usuario, igual que al
  crear el espacio: no se expone la lista de personas a quien no la tiene.
- No hay confirmación en JavaScript al quitar un miembro: quitar es reversible (se vuelve a agregar) y queda
  en el registro de eventos. Los botones llevan el nombre de la persona en `aria-label`.
- Cada acción usa el servicio existente (`authz.Service.SetScopeMember`, `kbservice.ApproveProposal`,
  `DiscardProposal`): las reglas y los eventos de auditoría son los mismos que por la API y la CLI.

### 9.5 Revisión contra clichés

- Sin tarjetas por miembro ni por propuesta: filas de lista con una línea de contexto en gris, como los
  documentos. Las únicas superficies con borde son los dos textos comparados (hojas de lectura) y el panel de
  decisión.
- Sin degradados, sin iconos, sin etiquetas en mayúsculas, sin flechas en los botones.
- La insignia de la pestaña es un número en un chip ámbar (el color de "pendiente" ya existe); no se anima.
- El rojo se reserva para errores; "Descartar" es un botón secundario, no rojo, porque descartar es una
  decisión normal de revisión.

### 9.6 Accesibilidad y respuesta

- Cada selector y campo tiene `<label>` (visible o `nx-visually-hidden` cuando la fila ya nombra a la persona
  en el `aria-label` del botón), foco visible heredado del tema, mensajes de éxito con `role="status"` y de
  error con `role="alert"`.
- A 360 px las filas de miembros y de propuestas pasan a una columna; la comparación lado a lado se apila
  (versión vigente arriba, propuesta abajo) desde 1080 px; no hay tablas anchas.
- Todo texto de usuarios, documentos y propuestas se escapa; el Markdown pasa por `mdhtml`.

### 9.7 Panel de Engram en español (OP-06)

Los textos fijos de `components.templ` (usuarios, tokens, permisos de proyecto, salud, auditoría, memorias,
sesiones, proyectos y colaboradores) pasan por `nx()`, que devuelve el original si la marca de Nexo no está
activa. Es solo envolver cadenas: no cambian estructura, rutas ni comportamiento. Los diccionarios viven en
`nexo_brand.go` (`nexoText`). Lo que ya viene del servidor en inglés (mensajes de error de la API de Engram)
no se traduce aquí.

### 9.8 Cambios tras la revisión con capturas

Capturas con Chrome sin interfaz contra un servidor local con espacios, personas, una cuenta de servicio y
propuestas reales (1366 px y 500 px, el ancho mínimo de la ventana sin interfaz; a 360 px las mismas reglas
apilan filas y formularios).

- Los formularios de la columna lateral heredaban la rejilla de varias columnas del formulario de crear
  espacio y se salían de la columna: dentro de `.nx-aside` el formulario pasa a un campo por fila.
- El selector de rol de cada fila salía alto (Pico): se reduce a la altura de los botones.
- En una cuenta de servicio el usuario y el nombre coinciden y la línea gris decía `ci-wiki, ci-wiki, ...`:
  si son iguales se muestra una sola vez.
- Los controles de la fila de miembros caben junto al nombre hasta 720 px; por debajo se apilan.
## 10. Pantallas del auditor (SG-07)

Estado: diseño de la rama `feat/panel-auditor`. Mismo lenguaje visual que Conocimiento y Cuentas de
servicio (papel gris frío, tinta verde oscura, chips de estado, tablas dentro de `.nx-scroll`); no se
agregan colores, tipografías ni componentes de otro estilo.

### 9.1 Audiencia y trabajo principal

| Quién | Qué viene a hacer |
|---|---|
| Auditor (rol de plataforma `auditor`) | Responder "qué pasó, quién y cuándo" y dejar constancia de que la evidencia está íntegra |
| Administrador y superadmin | Lo mismo, y atender las alertas |

El trabajo principal es **encontrar un hecho con su actor y su hora** (registro). El segundo es **demostrar
que la evidencia no se alteró** (integridad) y **cerrar el ciclo** (reconocer alertas, registrar la revisión
trimestral de accesos).

### 9.2 Estructura

Una pestaña "Auditoría" en el menú (solo para quien tiene `platform.audit.read`) y, dentro, una fila de
seis enlaces con la forma de los filtros de Conocimiento (`.nx-filters`, la actual con `aria-current`):

```
Auditoría
Registro   Actividad   Historial   Integridad   Alertas   Revisión de accesos
------------------------------------------------------------------------------
Registro:   [Seguridad | Roles]   Desde [..] Hasta [..] Actor [..] Acción [..] [Filtrar]
            Fecha (UTC)  Actor  Acción  Resultado  Ámbito  Origen  Detalle
            ...                                          [Ver más antiguos]

Integridad: [Tabla: todas v] [Verificar ahora]
            | La cadena está íntegra: 3 tablas, 1 284 filas verificadas.        |   <- elemento memorable
            Tabla  Capa  Estado  Verificadas  Sin cadena  Alteradas  Faltantes
            Fila alterada (la primera): id 57, posición 58, contenido ...
            Huecos: de la posición 12 a la 14 (3 faltantes), en el medio

Alertas:    [Pendientes | Todas]  Regla [..] Severidad [..]
            Fecha  Severidad  Regla  Sujeto  Resumen  Estado / [Reconocer v nota]

Revisión:   Última revisión: 2026-07-02 por Luis (hace 103 días), huella a1b2...   [Marcar revisión como hecha]
            Resumen: personas, inactivas, cuentas de servicio sin uso, tokens sin caducidad ...
            Descargar como evidencia:  [Descargar CSV] [Descargar JSON]
            Requieren atención (solo personas con marcas)  |  Todas las personas
            Persona  Estado  Rol  Roles por ámbito  Tokens  Última actividad  Marcas
```

El elemento memorable es el veredicto de integridad: una frase plana a todo el ancho con barra lateral
verde ("La cadena está íntegra") o roja ("Se detectó una alteración"), seguida de las filas exactas. El
resto es tabla y filtros, callado.

La pestaña la decide un middleware de Nexo, una vez por petición de página: calcula si la sesión tiene
`platform.audit.read` y deja la marca en el contexto; la marca de Nexo solo la lee. Sale a todo el que
tiene el permiso (auditor, admin y superadmin) en todas las páginas del panel, y a nadie más: un admin
de Engram con token heredado no tiene roles de Nexo y no la ve. Es solo interfaz (OWASP: la
autorización se hace en el servidor): cada pantalla vuelve a comprobar el permiso. Si la consulta de
roles falla, la pestaña se oculta y la página carga igual.

### 9.3 Decisiones

1. **Tablas, no tarjetas.** Son datos para comparar: columnas fijas, `font-variant-numeric: tabular-nums`,
   encabezado pegado, desplazamiento horizontal dentro de `.nx-scroll` (a 360 px la página no se mueve).
2. **Hora en UTC con la columna rotulada.** `AAAA-MM-DD HH:MM:SS` dentro de `<time datetime>`; el rótulo
   dice "(UTC)" una sola vez en el encabezado. Las fechas sueltas dicen "hace N días".
3. **Filtros por GET y en la URL.** Se puede copiar el enlace de una búsqueda. Paginación por cursor con
   "Ver más antiguos" (el servidor solo avanza; para volver se usa el botón Atrás del navegador).
4. **El código y su glosa.** La acción se muestra con su código (es lo que se escribe en el filtro) y, si se
   conoce, una línea en español debajo.
5. **Solo lo que ya expone la API.** El detalle de un evento sale de los mismos campos que el JSON y se
   filtra de nuevo al dibujarlo: se omiten claves con nombre de secreto y valores con forma de token, hash
   o cabecera Authorization. Nunca se muestra el prefijo del token, solo su nombre.
6. **Acciones con CSRF y PRG.** Reconocer una alerta y registrar la revisión son POST con el token CSRF del
   panel; al terminar redirigen (303) a la lista con un aviso fijo. Si la sesión es de un token de lectura,
   el botón no aparece y el servidor igual lo rechaza.
7. **Vacíos que orientan.** "No hay eventos con esos filtros. Amplía el rango de fechas o quita el actor."
   en lugar de una tabla vacía.
8. **Bloque propio en el tema.** Los estilos nuevos (veredicto, tablas densas, resumen, clases `nxa-`) van
   en un bloque separado al final de `nexo_theme.css`; no se tocan las reglas existentes.
9. **Descargar como evidencia.** En Revisión de accesos, debajo del resumen y su "Recalcular", un panel
   "Descargar como evidencia" con dos botones (`Descargar CSV` primario, `Descargar JSON` secundario;
   son enlaces con `download`, sin JavaScript) y una nota que dice qué incluye el archivo (todas las
   personas, o solo las marcadas si la tabla está filtrada), que el CSV se abre en Excel y que la huella
   va en el nombre. El archivo respeta el umbral y el filtro de la pantalla y los deja escritos
   dentro. La huella SHA-256 del contenido revisado se muestra completa, en `.nxa-code` (parte
   del bloque `nxa-`, se parte en líneas en pantallas angostas), en "Última revisión registrada".
   No se agregaron estilos: los botones son `.nexo-btn` y `.nexo-btn-secondary` del tema.

### 9.4 Revisión contra clichés

- No hay tablero de números grandes con degradado: el resumen de la revisión es una lista de pares
  etiqueta y valor, y lo importante son las filas.
- No hay semáforos decorativos: el color solo marca severidad y marcas (chips ya existentes).
- Sin mayúsculas sostenidas, sin "01 / 02", sin flechas en botones, sin tarjetas por fila.

### 9.5 Accesibilidad

Etiquetas visibles en cada campo, `scope="col"` en los encabezados, `<caption>` oculto a la vista por tabla
(`nx-sr`), foco visible heredado del tema, veredicto con `role="status"` (o `role="alert"` si falla),
formularios de reconocimiento dentro de `<details>` accesibles con teclado, contraste AA con los chips
del tema, y sin dependencia de JavaScript.

## 11. Estado de la búsqueda (rama `feat/panel-mejoras`)

Pantalla de solo lectura para quien administra la plataforma: responde "¿cómo está buscando Nexo hoy y está
al día el índice por significado?" sin entrar a la base ni a la CLI. Mismo lenguaje visual que Conocimiento y
Auditoría (papel gris frío, tinta verde oscura, chips, líneas finas); las clases nuevas llevan el prefijo
`nxs-` y viven en un bloque propio al final de `nexo_theme.css`.

### 11.1 Ruta, capacidad y datos

| Elemento | Decisión |
|---|---|
| Ruta | `GET /dashboard/kb/search-status`. Cuelga de `/dashboard/kb/`, que `nexoIsNexoPath` ya reconoce como ruta Nexo (sin tocar la prueba). Se registra con `s.nexoRoute(..., "platform.settings", ...)` |
| Capacidad | `platform.settings` (superadmin). Es la misma que exige `GET /nexo/kb/v1/admin/index` (estado del índice que usa `--wait-index`) y `POST .../admin/index:reindex`: la pantalla no abre una vía distinta de la API y la CLI, y el superadmin puede limitarla a ciertas redes (`NEXO_SUPERADMIN_ALLOWED_CIDRS`). Un administrador sin ese rol recibe 403 con el motivo y el intento queda como `authz.denied` |
| Servicio | `kbservice.SearchStatus` junta el estado que ya expone la API (versiones y cola del índice vectorial, estado del índice de texto) con lo que la pantalla necesita además: modo por defecto, proveedor sin clave, versiones retiradas con fechas, total de secciones, últimas cartas muertas, últimas evaluaciones y parámetros. Solo lecturas; sin tablas ni migraciones |
| Menú | Una pestaña "Estado de la búsqueda" para los administradores, junto a Cuentas de servicio |
| La clave | Nunca se muestra. La pantalla dice "Configurada" o "No configurada" y de qué variable sale (`NEXO_KB_EMBED_API_KEY`, `NEXO_KB_EMBED_API_KEY_FILE` o la del proveedor); el valor ni la ruta del archivo pasan por la plantilla. Los mensajes de error del proveedor se limpian otra vez (valor de la clave, `key=`, `Bearer`, formas de clave) y se recortan a 300 caracteres |

### 11.2 Wireframe

```
Estado de la búsqueda
Cómo responde hoy el buscador ... Esta pantalla solo muestra datos: no cambia nada.

| La búsqueda usa texto y significado.                                    |  <- veredicto (verde, azul o ámbar)
| Faltan 5 secciones o cambios por vectorizar; mientras tanto ...         |
| =========------  Versión 2 (activa): 3 de 5 secciones con vector (60 %) |  <- barra de avance
| ===-------------  Versión 3 (en construcción): 1 de 5 ... (20 %)        |

Configuración            Modo por defecto  [Texto y significado]
                         Índice de texto   [Necesita reindexar]  engram nexo kb reindex
                         Proveedor  gemini   NEXO_KB_EMBEDDER      Modelo, Dimensiones
                         Clave del proveedor  [Configurada]  Se lee de la variable NEXO_KB_EMBED_API_KEY
                         pgvector  [Disponible]  Versión 0.8.7, esquema public
Versiones del índice     tabla: versión, estado, modelo, dimensiones, creada, activada, retirada (UTC)
Avance de la indexación  por versión: pendientes, espera más antigua, en proceso, con fallos,
                         cambios sin leer (retraso del feed), cartas muertas
Secciones que no se pudieron vectorizar
                         N secciones ...  `engram nexo kb reindex --vectors --requeue-dead`
                         últimos errores del proveedor (solo el mensaje)
Últimas evaluaciones     fecha, buscador, preguntas, Hit@5, MRR, sin respuesta, conjunto
Parámetros en uso        RRF (k y pesos), umbrales, consulta por significado, indexador (con su variable)
Reordenamiento (R3)      [No configurado]   <- lugar reservado
```

El elemento memorable es el veredicto con su barra: una frase plana, en la serif de lectura, que dice qué
buscador responde hoy (solo texto, construyendo, texto y significado al día o con pendientes). Lo demás es
lista y tabla, callado. El color solo marca estado (verde: al día; azul: informa; ámbar: requiere atención).

### 11.3 Cómo agregar el bloque de R3 (reordenamiento)

El bloque existe y dice "No configurado". Cuando R3 se integre:

1. Llenar `dashboard.NexoSearchRerank` en `searchStatusRerank` (`cloudserver/nexo_search_status.go`) con
   `Configured: true` y las filas de etiqueta y valor (proveedor, modelo, candidatos que reordena, clave
   configurada sin mostrarla). Si el servicio de R3 expone su estado, agregar el campo a
   `kbservice.SearchStatus`.
2. Nada más: la plantilla `nexoSearchRerank` ya dibuja las filas y la nota.

Para cualquier otro bloque nuevo: un campo en `NexoSearchStatusPage` y una llamada en
`nexo_search_status.templ`.

### 11.4 Revisión contra clichés y accesibilidad

- Sin tablero de números grandes con degradado: el veredicto es una frase y la barra es un `<progress>` nativo.
  Las cifras de avance son pares etiqueta y valor con una línea fina, no tarjetas.
- Sin mayúsculas sostenidas, sin "01 / 02", sin flechas en botones, sin iconos, sin movimiento.
- `role="status"` en el veredicto, `<progress>` con `aria-label`, `<caption>` oculto y `scope="col"` en las tablas,
  títulos de sección enlazados con `aria-labelledby`, foco heredado del tema, tablas en `.nx-scroll`
  (a 360 px la página no se mueve), pares etiqueta y valor que se apilan por debajo de 600 px.

## 12. Selectores, menú y textos que faltaban en español

### 12.1 Selectores con flecha

Los selectores del tema parecían campos de texto: `body.nexo select` usa el atajo `background`, que borra la
flecha (`background-image`) que Pico ponía. El arreglo es global, en el bloque final de `nexo_theme.css`:

- Se quita la apariencia nativa y se dibuja una flecha con un SVG en línea (gris tinta; verde con el cursor
  encima o con foco). La separación se ajusta con `--nx-select-pad` y `--nx-select-arrow` (el selector de rol
  de cada fila de miembros usa valores más chicos).
- Foco visible: contorno de 3 px verde con separación (`:focus-visible`), igual que el resto del tema.
- Móvil y pantallas táctiles: alto mínimo de 2,75 rem y 16 px de letra (iOS no amplía la página al enfocar).
- Contraste alto del sistema (`forced-colors`): vuelve la flecha nativa.
- El tema solo tiene modo claro (`data-theme="light"` fijo), así que no hay variante oscura.
- Selectores revisados: rol de miembros (alta y cada fila), alcance del token de una cuenta de servicio,
  filtros del registro de auditoría, "Cómo buscar", visibilidad al crear un espacio, tipo de fuente de la
  subida, y los del panel de Engram (proyecto de Memorias, rol y alcance de usuarios, resultado de la
  auditoría de Engram).

### 12.2 Menú

Con diez pestañas, las últimas quedaban fuera de la vista (el menú se desplazaba en horizontal sin barra
visible). Desde 721 px las pestañas se reparten en dos filas; en móvil sigue el desplazamiento.

### 12.3 Textos de Go en español (OP-06, resto)

- El "Never" de las fechas (`formatTimePtr`, `formatTimestampStr`) y "No content captured." pasan por `nx()`.
- `EmptyState` (títulos y mensajes de "no encontrado" y "no disponible") pasa sus dos textos por `nx()`;
  `renderStoreError` traduce el título, el mensaje y el rótulo "DEGRADED". Los textos con datos pegados
  ("<contexto> unavailable", "User X is currently disabled.") se resuelven con patrones en `nexoTextPattern`.
- Los mensajes de texto plano de los handlers de `/dashboard/` (`http.Error` en inglés, a veces con el error de
  la base pegado) se traducen al responder, sin editar esos handlers: un middleware de Nexo
  (`nexo_panel_errors.go`) retiene el cuerpo de un 4xx o 5xx de texto plano y lo pasa por `NexoErrorText`
  (`dashboard/nexo_errors_es.go`). Un error interno (5xx) nunca muestra el detalle: queda en el log y la
  persona ve "Ocurrió un error interno. Vuelve a intentarlo; si se repite, avisa al administrador."
- Los mensajes que solo existen en el panel de Nexo (conocimiento, cuentas de servicio, sesión de solo lectura)
  se escribieron en español en su propio código.
- Pruebas: `TestNexoGoTextsAreTranslated` y `TestNexoErrorTextCoversEngramMessages` recorren los archivos de
  Engram y fallan si una cadena fija no tiene traducción.

## 13. Administración > Usuarios con el rol de Nexo (rama `feat/roles-unificados`)

Antes, la pantalla de Engram ("Managed Users") ofrecía solo `member` y `admin` y mostraba la columna
de Engram. Con la unificación de roles (`docs/nexo/roles/design.md` §11) la persona tiene un solo rol, el
de Nexo, y es el único que se elige y se muestra.

### 13.1 Audiencia y trabajo principal

- **Superadmin:** crea personas con cualquiera de los cuatro roles y cambia el rol de quien ya existe,
  con confirmación. Es la única persona que ve habilitado todo el selector.
- **Admin:** crea miembros. La interfaz le muestra auditor, admin y superadmin desactivados con la razón
  ("Solo un superadmin puede asignarlo"); el servidor decide igual (403 sin cambios y evento
  `authz.denied`).
- **Auditor y miembro:** no ven la administración (como antes).

### 13.2 Estructura

```
Usuarios
[Resumen] [Proyectos] [Usuarios] [Salud del servidor] [Auditoría]

Crear usuario
 Usuario [______]  Correo opcional [______]  Nombre opcional [______]
 Rol de plataforma
 ( ) Miembro        Usa la memoria ... No administra.      En Engram figura como member
 ( ) Auditor        Lee la auditoría ...                    En Engram figura como member
 ( ) Administrador  Crea miembros, emite sus tokens ...     En Engram figura como admin
 ( ) Superadmin     Todo lo del administrador, y además...  En Engram figura como admin
 [Crear usuario]

Usuarios  (tabla: Usuario | Nombre | Rol de plataforma | Estado | Creado | Acción)

Detalle de una persona
 Rol de plataforma:  [Auditor] Lee la auditoría y el directorio de personas. No cambia nada.
                     Engram lo ve como miembro. Se deriva de este rol; no se edita aparte.
 Cambiar a  (cuatro filas, la actual marcada)         [Cambiar rol]  Te pediremos confirmar antes de aplicarlo.

Confirmar cambio de rol
 Vas a cambiar el rol de plataforma de Ana Torres (ana):  [Miembro] pasa a [Auditor]
 Descripción del rol nuevo y cómo lo ve Engram       [Confirmar cambio] [Cancelar]
```

- **Una sola información de rol.** El chip del rol de Nexo es lo único que dice "quién es" en la lista,
  el detalle y la cabecera (junto al nombre, con una explicación al pasar el cursor). La columna de
  Engram solo aparece como equivalencia en una línea ("En Engram figura como ..."), nunca como segundo rol.
- **Chip por privilegio:** miembro gris, auditor azul, admin verde claro, superadmin verde pleno
  (`nx-chip-ret`, `-sched`, `-ok`, `-top`), los tonos del tema (`nexo_theme.css`).
- **Selector:** cuatro filas de elección única en dos columnas (una en pantallas angostas), con nombre,
  qué permite y su equivalencia con Engram. La fila elegida lleva fondo verde claro y borde verde; la
  desactivada, fondo neutro y la razón en el color de aviso. Sin iconos ni adornos: la estructura es el
  borde y el espacio.
- **Confirmación:** pantalla propia (no un diálogo del navegador) con "de -> a" en chips, lo que permite el
  rol nuevo y, si es la propia persona, el aviso de que bajarse de rol quita la administración. El POST
  lleva token CSRF del panel; el segundo POST (`confirm=1`) aplica. Tras aplicar, redirige al detalle con
  "Rol actualizado. El cambio quedó en la auditoría."; si la persona se bajó a sí misma, al inicio.
- **Errores en el detalle** (misma estructura, texto accionable): 403 "Solo un superadmin puede cambiar el
  rol de plataforma", 403 auto-promoción, 409 "No se puede cambiar: es el último superadmin habilitado.
  Asigna primero el rol de superadmin a otra persona.", 400 rol no válido, 404.

### 13.3 Decisiones

| Id | Decisión | Motivo |
|---|---|---|
| D-U1 | La interfaz refleja, el servidor decide | Las opciones desactivadas son ayuda; `nexoCreateUserRole` y `usersPanelSetRole` comprueban `platform.admins.manage` |
| D-U2 | La capacidad se comprueba antes de la confirmación | Una denegación queda en la auditoría y no se confirma algo imposible |
| D-U3 | Confirmación en página propia con CSRF | Cambiar un rol de plataforma es sensible; un diálogo del navegador no deja explicar el efecto |
| D-U4 | El rol de la cabecera sale del mismo cálculo y caché de 30 s de la pestaña Auditoría (`nexo_audit_nav.go`); al cambiar un rol se descarta la caché de esa persona | Una sola consulta por petición; sin mostrar el rol anterior tras un cambio |
| D-U5 | Los POST de `/dashboard/admin/*` que responden una página completa también dibujan el menú y el rol | La confirmación y los errores no pierden la cabecera |
| D-U6 | Rol nuevo al crear sin CSRF: 403 | Elevar privilegios es un cambio de estado; un miembro no lo necesita (compatibilidad con el formulario de Engram) |

### 13.4 Revisión contra clichés y accesibilidad

Sin iconos, sin tarjetas idénticas para decorar (las filas del selector son controles, no contenido), sin
etiquetas en mayúsculas, sin numeración. Un solo énfasis: el chip de rol y su gradación. El selector es un
`fieldset` con `legend` y botones de opción nativos (teclado y lector de pantalla), foco visible con
contorno verde, el estado "desactivado" se comunica con el atributo y con texto, no solo con color. Texto
de 14 a 16 px, contraste del tema. En 500 px los campos y las opciones pasan a una columna.
