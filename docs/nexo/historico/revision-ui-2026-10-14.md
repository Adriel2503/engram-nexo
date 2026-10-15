# Revisión de UI y UX del panel

Fecha: 2026-10-14. Revisión del panel en Chrome con rol de superadmin.
Archivada el 2026-10-15. La segunda versión se publicó en el commit `380ebdd` y se integró en
develop y main mediante `e6368f8`: marco lateral, inicio con búsqueda, espacios compactos,
pestañas unificadas y alta de usuarios desplegable después de la lista. La tabla conserva el
diagnóstico que motivó estos cambios; los flujos de publicación y auditoría mantienen su estructura.
Las mejoras propuestas y los límites de revisión que siguen son los de aquella fecha.
Consulta el [resumen](../README.md#4-revisión-visual-del-panel) y el [diseño mantenido](../panel/design.md).

## Lo observado

| Pantalla | Problema | Mejora propuesta |
|---|---|---|
| Marco común | La navegación desplegada aumenta la altura del encabezado y desplaza el contenido. | Navegación lateral estable en escritorio y menú compacto en móvil. |
| Inicio | La tarjeta de bienvenida ocupa casi toda la primera vista y aporta poco trabajo útil. | Entrada a la búsqueda y espacios; bienvenida discreta, sin tarjeta grande. |
| Conocimiento | Título, explicación y título del buscador repiten la intención. Las tarjetas repiten rol, visibilidad, ceros y fecha. | Un encabezado breve, buscador y lista de espacios con nombre, descripción corta y documentos. |
| Espacio | Mucho espacio vertical antes de la lista. Metadatos y botones secundarios compiten con documentos y búsqueda. | Título compacto, búsqueda, lista de documentos y una acción de subida para quien puede publicar. |
| Documento | El título del panel se repite al principio del contenido; demasiados metadatos ocupan la cabecera. | Una entrada de lectura clara; estado y versión visibles, resto en detalles, índice discreto. Preservar anclas y citas. |
| Publicación | Tres entradas de archivo, carpeta y zip aparecen juntas, además de categoría e historial de subidas. | Elegir un tipo de subida y mostrar sus campos; opciones secundarias bajo demanda. No cambiar las reglas de publicación. |
| Propuestas | Títulos repetidos y explicaciones sobre roles, API y herramientas en el estado vacío. | Título único, filtros sencillos y una explicación breve de qué falta o qué se puede hacer. |
| Memorias | Título repetido, pestañas con mayúsculas, fondos grises y degradado violeta del tema anterior. | Título único, controles planos coherentes y filtros etiquetados. |
| Proyectos y Colaboradores | Etiquetas redundantes y vacíos que parecen una búsqueda fallida cuando todavía no hay datos. | Distinguir falta de datos de falta de coincidencias; indicar cómo aparecerá el contenido. |
| Administración | Varias filas de navegación y dos destinos llamados Auditoría; estadísticas antes de tareas útiles. | Agrupar administración en un solo sistema de navegación y distinguir los dos registros por su alcance. |
| Usuarios | El formulario de creación ocupa el comienzo de la página; descripciones largas y equivalencias de Engram en cada rol. | Lista primero, acción «Crear usuario» y descripción breve del rol seleccionado. Mantener confirmaciones y controles del servidor. |
| Cuentas de servicio | Advertencia extensa con variable de entorno y varias restricciones antes de la lista. | Estado breve y explicación técnica desplegable. Mantener visibles las restricciones relevantes. |
| Estado de búsqueda | Aun simplificado, la navegación ocupa mucho alto; aparecen términos internos y «Problemas: 0». | Resumen breve con estados claros; avisos solo cuando corresponden y diagnósticos técnicos agrupados. |
| Auditoría | Tres niveles de navegación, varios filtros y datos técnicos como códigos de acción y agentes de usuario. | Título y filtros principales compactos; detalles técnicos por evento y etiquetas humanas sin perder evidencia. |
| Revisión de accesos | La acción de registrar revisión aparece antes del contenido que se debe revisar, seguida de muchos contadores y notas técnicas. | Personas y hallazgos primero; registrar revisión después de revisar, con resumen y evidencia disponibles. |

## Dirección propuesta

Priorizar encontrar, leer y publicar conocimiento. La memoria de proyectos y la administración
son destinos diferenciados. Un mismo lenguaje visual para todas las páginas.

- Base blanca `#ffffff`, fondo suave `#f3f5f4`, texto `#17231f`, secundario `#4a5853`,
  acento verde Nexo `#0e5e4b`, separadores `#d3dbd7`.
- Una familia sans serif para interfaz y marca; lectura cómoda en documentos, sin descargar fuentes
  externas en esta etapa. Texto base de 16 px, títulos de página de 24–28 px y metadatos legibles.
- Cabecera compacta; contenido alineado a la izquierda. Espaciado consistente, sin grandes huecos
  entre encabezado, filtros y listas. Bordes para distinguir estructuras, no para encerrar cada bloque.
- Un título por página y una acción principal. Tablas y listas para explorar; formularios de alta
  accesibles desde una acción explícita. No retirar permisos, vigencias, citas, errores ni confirmaciones.
- Estados vacíos con causa y siguiente paso. Español sencillo; nombres de variables y códigos internos
  quedan en detalles técnicos.

```text
Nexo IA                 Conocimiento                         persona
-------------------     --------------------------------------------
Inicio                  Busca en los documentos del equipo
Conocimiento            [ Pregunta o tema                  ] [Buscar]
Propuestas
                        Espacios
Memoria del equipo      Backend       Guías de servicios   2 documentos
  Memorias              Datos         Bases de datos       1 documento
  Proyectos             Frontend      Interfaces web       5 documentos
  Colaboradores

Administración
```

## Referencias y criterio

- `frontend-design`, repositorio `anthropics/skills`: jerarquía visual y textos centrados en la tarea.
- `ui-ux-pro-max`, repositorio `nextlevelbuilder/ui-ux-pro-max-skill`: navegación, accesibilidad
  y consistencia de controles.
- La consulta local «knowledge base dashboard minimal» recomienda minimalismo y jerarquía clara.
  Su patrón de landing de documentación y colores azules no se adoptan literalmente: Nexo es una
  herramienta de trabajo con identidad verde existente, no una página comercial o de soporte.
- Stack actual: Go, templ, HTML renderizado en servidor, htmx y CSS. Esta revisión no propone migrar
  de framework ni incorporar librerías para rediseñar la interfaz.

## Límites de esta revisión

Se inspeccionaron las pantallas principales, estados vacíos, lectura de documento, publicación,
formularios de usuarios y las secciones Registro, Alertas y Revisión de accesos de Auditoría.
No se crearon, publicaron, retiraron, habilitaron ni deshabilitaron datos. No se emitieron tokens.
Faltan la revisión del login vigente, las variantes con otras identidades, móvil, teclado, contraste
y los estados de formularios con errores. Deben comprobarse sobre el nuevo candidato visual.
