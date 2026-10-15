# Documentación de Nexo IA

Los documentos de esta carpeta son propios del fork. Empieza por los manuales para instalar o
usar Nexo; consulta las especificaciones para desarrollar y el histórico para revisar antecedentes.

| Necesito… | Consultar |
|---|---|
| Instalar, conectar o administrar Nexo | [Manuales](manuales/README.md) |
| Instalar el servidor en Ubuntu | [Guía paso a paso](manuales/servidor-ubuntu.md) |
| Entender permisos y roles | [Roles](roles/design.md) |
| Entender documentos, búsqueda y vigencias | [Conocimiento](conocimiento/design.md) |
| Revisar seguridad y despliegue | [Seguridad](seguridad/design.md) y [guía de despliegue](seguridad/guia-despliegue.md) |
| Entender autoría y auditoría | [Trazabilidad](trazabilidad/design.md) |
| Revisar el diseño actual del panel | [Panel](panel/design.md) |
| Consultar ideas y pendientes | [Mejoras y referencias](mejoras-y-referencias.md) |

## Resumen de los cinco documentos revisados

Revisión documental: 2026-10-15. Los cuatro antecedentes se conservan en `historico/` y la lista
de mejoras permanece activa. Este resumen reúne lo útil para orientarse; las pruebas y los
resultados de cada informe corresponden a su entrega, no a una nueva validación del código.

### 1. Modelo funcional inicial

El [modelo original](historico/modelo-funcional.md) comparaba Nexo con Utopia y proponía organizar
el conocimiento en dos proyectos de Engram: oficial y propuestas. Ese modelo fue reemplazado
por los espacios de conocimiento y las especificaciones de roles.

La decisión de distribuir un cliente y servidor propios sigue reflejada en el fork actual.
Las propuestas del documento sobre vigencias en SQLite y nuevas herramientas no deben leerse
como una descripción de lo implementado: el diseño actual se consulta en
[roles](roles/design.md) y [conocimiento](conocimiento/design.md). La vigencia del conocimiento
y la memoria local de proyectos son conceptos distintos.

### 2. Auditoría y activación

El [informe de implementación](historico/nexo_informe_auditoria_activacion.md) documenta dos cambios:
limitar el contenido expuesto por la auditoría según los permisos del ámbito y entregar códigos
de activación para que otras personas obtengan su token. Incluye redacción de credenciales,
canje de un solo uso, concurrencia y proyección de exportaciones y alertas.

Conserva un problema abierto: quien administra conoce el código que entrega y podría canjearlo
sin identificarse como emisor. Las mitigaciones no resuelven por completo ese riesgo; la solución
pendiente está registrada como **SG-17**, requisito antes de producción, en el
[backlog](mejoras-y-referencias.md).
También registra validaciones pendientes de dos cuentas PostgreSQL para aquella entrega.
La operación actual se explica en [administración](manuales/administracion.md) y
[producción](manuales/servidor-produccion.md). Archivar el informe no cierra esos pendientes.

### 3. Conexiones PostgreSQL: OP-13

El [informe OP-13](historico/op13-conexiones.md) documenta bloqueos al pedir otra conexión mientras
se retenía una transacción o un cursor: publicación/aprobación, fallback vectorial y relleno de
sesiones. Registra regresiones con una conexión y una prueba concurrente con un pool de cuatro.

El criterio que deja es reutilizar el ejecutor transaccional y liberar recursos antes de pedir
otra conexión. El límite de 25 conexiones por réplica es un presupuesto inicial, no una garantía
para cualquier carga; la configuración se consulta en [producción](manuales/servidor-produccion.md).
La suite amplia de aquella entrega tuvo fallos de entorno, conservados en el informe.

### 4. Revisión visual del panel

La [revisión del 2026-10-14](historico/revision-ui-2026-10-14.md) detectó navegación extensa,
encabezados repetidos y formularios que desplazaban el trabajo principal. La implementación
`380ebdd`, integrada mediante `e6368f8`, añadió navegación lateral, búsqueda en Inicio, espacios
compactos, pestañas planas y lista de usuarios antes del formulario de alta.

Otras mejoras de la tabla eran propuestas, no entregas confirmadas. La revisión de móvil,
teclado, contraste y variantes de identidad quedó pendiente en ese informe. Para el criterio
de diseño mantenido consulta [panel/design.md](panel/design.md).

### 5. Mejoras y referencias

[Mejoras y referencias](mejoras-y-referencias.md) reúne ideas, decisiones y pendientes sobre
búsqueda, formatos, producto, permisos y operación. Conserva sus identificadores `RC`, `IN`,
`KP`, `PR`, `SG` y `OP`, usados por las especificaciones y el inventario del fork.

Entre los temas abiertos figuran validar las preguntas de evaluación con personas, completar
formatos y adjuntos, revisar roles de proyectos y resolver la entrega de accesos de SG-17.
Algunos estados son anteriores a implementaciones posteriores, como el stack: deben contrastarse
con el código y [NEXO_CHANGES.md](../../NEXO_CHANGES.md) antes de planificar trabajo.
Este resumen no reemplaza la lista detallada ni cambia sus prioridades.
