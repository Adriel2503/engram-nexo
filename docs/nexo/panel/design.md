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
