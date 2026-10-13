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
| Login | Entrar con el token asignado | Explicar en tres frases qué es Nexo |
| Menú | Llegar a Conocimiento en un clic | Memorias, proyectos, colaboradores y administración de Engram |
| `/dashboard/kb` | Elegir un espacio | Entender los 4 pasos del flujo y en cuál está el usuario; crear espacio (admin) |
| Espacio | Preguntar y ver la respuesta citada | Recorrer los documentos por carpeta; subir (responsable) |
| Documento | Leer la sección citada | Estado, vigencia, versiones y qué cambió entre versiones |

## 3. Wireframes

Login (dos columnas; una en móvil):

```
+---------------------------------------------------------------+
| Nexo IA                          | Iniciar sesión              |
| Memoria y estándares del equipo  | Ingresa el token que te     |
|                                  | asignó el administrador.    |
| Busca la regla vigente ...       | [ Token de acceso        ]  |
| Publica documentos ...           | [ Iniciar sesión ]          |
| Conecta tu asistente ...         |                             |
+---------------------------------------------------------------+
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

Estado: diseño previo a la implementación (2026-10-13). Cubre KP-12 (miembros de un espacio), KP-13
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
