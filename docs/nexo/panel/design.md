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

## 9. Pantallas del auditor (SG-07)

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

Revisión:   Última revisión: 2026-07-02 por Luis (hace 103 días)   [Marcar revisión como hecha]
            Resumen: personas, inactivas, cuentas de servicio sin uso, tokens sin caducidad ...
            Requieren atención (solo personas con marcas)  |  Todas las personas
            Persona  Estado  Rol  Roles por ámbito  Tokens  Última actividad  Marcas
```

El elemento memorable es el veredicto de integridad: una frase plana a todo el ancho con barra lateral
verde ("La cadena está íntegra") o roja ("Se detectó una alteración"), seguida de las filas exactas. El
resto es tabla y filtros, callado.

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
