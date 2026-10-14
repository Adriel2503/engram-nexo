# Roles: requisitos

Estado: borrador para revisión. Fecha: 2026-10-12. Identificador: **NX-05**. Rama: `feat/conocimiento`.
Lo usan la plataforma, la memoria de proyectos (Engram) y los espacios de conocimiento (NX-10,
`docs/nexo/conocimiento/`). Reemplaza las secciones de roles de `docs/nexo/modelo-funcional.md`.

## 1. Idea central

Un único modelo de autorización con **tres niveles de ámbito**:

| Nivel | Ámbito | Para qué |
|---|---|---|
| Plataforma | Todo el servidor (`platform`) | Administrar usuarios, tokens, ámbitos y la propia administración |
| Proyecto | Un proyecto de Engram (por nombre) | Memoria de trabajo de un equipo |
| Espacio | Un espacio de conocimiento (por `slug`) | Conocimiento oficial de la empresa |

En los tres niveles un rol es un **conjunto de capacidades**, el código comprueba capacidades (no
nombres de rol) y una persona tiene **un rol por ámbito**. Los roles de proyecto y espacio son **los
mismos** y significan lo mismo; los de plataforma son propios porque administran, no manejan
contenido. Agregar un rol "según necesidad" es agregar una fila a la tabla de capacidades, sin tocar
rutas ni datos.

## 2. Roles de plataforma

| Rol | Pensado para | Qué puede | Qué no puede |
|---|---|---|---|
| **superadmin** | Dueño técnico de la plataforma (2 personas recomendado) | Todo lo de admin; además crear, cambiar y quitar admins y superadmins, configurar la plataforma y recuperar cualquier ámbito sin responsable | Leer o cambiar contenido de proyectos y espacios sin tener rol en ellos (la recuperación asigna un responsable y queda auditada) |
| **admin** | Equipo de plataforma | Crear, habilitar y deshabilitar usuarios **miembros**; emitir y revocar tokens; cuentas de servicio; crear y archivar proyectos compartidos y espacios; nombrar responsables; leer la auditoría global (NX-03) | Crear, cambiar o deshabilitar admins o superadmins; contenido sin rol en el ámbito |
| **auditor** | Seguridad o cumplimiento | Leer la auditoría global y los eventos de todos los ámbitos, y la lista de usuarios y asignaciones | Cambiar cualquier cosa; leer contenido sin rol en el ámbito |
| **miembro** | Todos los demás (por defecto) | Lo que le den sus roles en proyectos y espacios; ver sus propios roles y tokens | Administrar |

Roles futuros previstos como ejemplo de extensión (no se construyen ahora): **soporte** (revocar y
reemitir tokens de miembros, sin crear usuarios), **gestor de espacios** (crear espacios sin
administrar usuarios).

## 3. Roles de ámbito (proyectos y espacios)

Ordenados; cada uno incluye lo del anterior.

| Rol | Significado común | En un **proyecto** | En un **espacio** |
|---|---|---|---|
| **lector** | Consulta, no aporta | Descarga y busca la memoria | Busca y lee documentos vigentes, historial, copia local |
| **proponente** | Aporta lo suyo | Guarda, corrige y descarta **sus** memorias; se sincronizan directo | Envía **propuestas**; no son oficiales hasta revisarse |
| **publicador** | Gobierna contenido de otros | Además corrige, descarta y borra memorias **de otros** | Además publica, actualiza, retira y reemplaza documentos; revisa propuestas, incluidas las propias |
| **responsable** | Gobierna el ámbito | Además gestiona miembros y configuración del proyecto | Además gestiona miembros y configuración del espacio |

### 3.1 Uso habitual

| | Proyectos | Espacios |
|---|---|---|
| Dev normal | **proponente**: escribe y sincroniza directo, como hoy | **lector** (implícito si el espacio es abierto): consulta |
| Quién actualiza | Todo el equipo, cada uno lo suyo | **Solo ciertos usuarios**: publicadores y responsables asignados (por ejemplo, el jefe de frontend y el CI del repositorio de lineamientos) |
| Aportes de otros | Directos | Como propuesta, si el espacio lo habilita |
| Líder | responsable o publicador | responsable del espacio que le toca |

### 3.2 Ejemplo

| Persona | Plataforma | `ms-factura` (proyecto) | `lineamientos-frontend` (espacio abierto) | `seguridad` (espacio restringido) |
|---|---|---|---|---|
| Carla | superadmin | sin acceso | lector (implícito) | sin acceso |
| Pedro | admin | sin acceso | lector (implícito) | sin acceso |
| Sofía | auditor | sin acceso | lector (implícito) | sin acceso |
| Ana (líder frontend) | miembro | responsable | responsable | lector |
| Luis (dev) | miembro | proponente | lector (implícito) | no lo ve |
| Marta (seguridad) | miembro | lector | lector (implícito) | publicador |
| CI `lineamientos` | cuenta de servicio | sin acceso | publicador | sin acceso |

## 4. Capacidades

### 4.1 Plataforma

| Capacidad | superadmin | admin | auditor | miembro |
|---|---|---|---|---|
| `platform.admins.manage` (crear, cambiar, deshabilitar admins, superadmins y auditores) | Sí | | | |
| `platform.settings` | Sí | | | |
| `platform.scope.recover` (asignar responsable en un ámbito sin responsable) | Sí | | | |
| `platform.users.manage` (miembros y cuentas de servicio) | Sí | Sí | | |
| `platform.tokens.manage` (tokens de otros) | Sí | Sí | | |
| `platform.scope.create`, `platform.scope.archive` | Sí | Sí | | |
| `platform.scope.assign_owner` (nombrar responsable al crear) | Sí | Sí | | |
| `platform.audit.read` | Sí | Sí | Sí | |
| `platform.directory.read` (usuarios y asignaciones) | Sí | Sí | Sí | |
| `self.read` (mis roles, mis tokens) | Sí | Sí | Sí | Sí |

### 4.2 Proyectos

| Capacidad | Qué permite | lector | proponente | publicador | responsable |
|---|---|---|---|---|---|
| `project.read` | Descargar manifiesto, paquetes y cambios | Sí | Sí | Sí | Sí |
| `project.write.own` | Subir memorias nuevas y cambiar o borrar las propias | | Sí | Sí | Sí |
| `project.write.any` | Cambiar o borrar memorias de otros | | | Sí | Sí |
| `project.audit.read` | Historial y actividad del proyecto | | | Sí | Sí |
| `project.members.manage` | Asignar roles en el proyecto | | | | Sí |
| `project.configure` | Pausar sincronización, visibilidad | | | | Sí |

### 4.3 Espacios

| Capacidad | Qué permite | lector | proponente | publicador | responsable |
|---|---|---|---|---|---|
| `space.read` | Buscar, leer, historial, copia local | Sí | Sí | Sí | Sí |
| `space.propose` | Propuestas propias | | Sí | Sí | Sí |
| `space.publish` | Publicar, actualizar, retirar, reemplazar | | | Sí | Sí |
| `space.review` | Aprobar o descartar propuestas | | | Sí | Sí |
| `space.audit.read` | Eventos del espacio | | | Sí | Sí |
| `space.members.manage` | Asignar roles en el espacio | | | | Sí |
| `space.configure` | Nombre, descripción, visibilidad, rol implícito, categorías | | | | Sí |

### 4.4 Alcance del token

| Alcance | Capacidades permitidas |
|---|---|
| `read` | `*.read`, `*.audit.read`, `self.read` |
| `write` (por defecto) | todas las del rol |

## 5. Reglas

- **R-1.** Un rol por persona y ámbito; cambiarlo reemplaza el anterior.
- **R-2.** Permiso efectivo = capacidades del rol efectivo ∩ alcance del token, con persona habilitada y token vigente. Se evalúa en cada petición.
- **R-3.** Rol efectivo en un ámbito: la asignación explícita; si no hay y el ámbito es abierto, su rol implícito (`default_role`: lector por defecto, o proponente; nunca publicador ni responsable); si no, ninguno.
- **R-4.** Ámbito restringido sin rol: no aparece en listados, búsquedas, descargas ni copias locales; pedirlo por nombre responde 404.
- **R-5.** Visibilidad por defecto: proyectos **restringidos**, espacios **abiertos**.
- **R-6.** Los roles de plataforma **no** dan acceso a contenido. Un superadmin o admin que necesita publicar en un espacio recibe el rol en ese espacio, como cualquiera.
- **R-7.** Solo un superadmin crea, cambia o deshabilita admins, superadmins y auditores. Un admin solo gestiona miembros y cuentas de servicio.
- **R-8.** Debe existir siempre al menos un superadmin habilitado; el último no se puede degradar ni deshabilitar. Se recomiendan dos.
- **R-9.** Todo ámbito activo tiene al menos un responsable; el último no se puede quitar. Si queda sin ninguno (por ejemplo, la persona fue deshabilitada), un superadmin lo recupera asignando uno.
- **R-10.** Las cuentas de servicio solo pueden ser miembro en plataforma y lector, proponente o publicador en ámbitos.
- **R-11.** Nadie se asigna a sí mismo un rol mayor al que tiene en ese nivel.
- **R-12.** En proyectos, la memoria pertenece a quien la creó (primer cambio con autor, NX-01). Las memorias sin autor (anteriores al fork) solo las cambia un publicador.
- **R-13.** Deshabilitar a una persona o revocar su token corta el acceso en la siguiente petición; asignaciones y autoría se conservan como historial.
- **R-14.** Toda asignación, cambio de rol y denegación queda auditada con quién, a quién, antes, después, token, IP y cliente.
- **R-15.** El token compartido (`ENGRAM_CLOUD_TOKEN`) y el modo inseguro no tienen roles: sin acceso a espacios ni a la administración Nexo; en proyectos conservan el comportamiento de Engram (compatibilidad, no recomendado).

## 6. Requisitos funcionales

- **RF-01.** La primera persona creada con `engram cloud bootstrap admin` es superadmin.
- **RF-02.** Un superadmin asigna y quita roles de plataforma; un admin crea y gestiona miembros y cuentas de servicio.
- **RF-03.** Un admin crea un proyecto compartido o un espacio indicando su responsable inicial.
- **RF-04.** El responsable de un ámbito (o un superadmin en recuperación) asigna, cambia y quita roles en él.
- **RF-05.** Cualquier persona consulta sus roles en todos los niveles (`me`); su asistente puede mostrarlos.
- **RF-06.** Un auditor o admin consulta quién tiene qué rol en cada ámbito y el historial de cambios.
- **RF-07.** Las rutas de administración de Engram (`/admin/*` y el panel) respetan R-7: un admin no puede crear ni promover a otro admin.
- **RF-08.** Las rutas de sincronización de proyectos aplican `project.read`, `project.write.own` y, en una etapa posterior, `project.write.any`.
- **RF-09.** El cliente Nexo solo inscribe para subida los proyectos donde la persona tiene `project.write.own`, para no trabar su autosync.

## 7. Requisitos no funcionales

| ID | Requisito | Valor |
|---|---|---|
| RNF-R1 | Costo de autorización por petición | < 2 ms (una consulta indexada por persona) |
| RNF-R2 | Escala | 2 000 personas, 5 000 ámbitos, 100 000 asignaciones |
| RNF-R3 | Extensión | Nuevo rol o capacidad sin migración de datos de contenido |
| RNF-R4 | Compatibilidad | Clientes oficiales de Engram sincronizan igual; asignaciones previas de Engram respetadas (sección 9) |

## 8. Criterios de aceptación

| ID | Criterio |
|---|---|
| CA-R01 | El usuario de bootstrap es superadmin y puede crear a Pedro como admin |
| CA-R02 | Pedro (admin) crea a Luis como miembro, pero no puede crear ni promover a otro admin, ni por la API Nexo ni por `/admin/*` de Engram ni por el panel |
| CA-R03 | No se puede deshabilitar ni degradar al último superadmin |
| CA-R04 | Sofía (auditor) lee la auditoría global y las asignaciones; cualquier escritura le da 403 |
| CA-R05 | Pedro crea el espacio `lineamientos-frontend` con Ana responsable; Ana agrega al CI como publicador; el CI no puede ser responsable |
| CA-R06 | Luis, sin asignación, lee el espacio abierto; cambia `default_role` a proponente y Luis puede proponer |
| CA-R07 | En el proyecto `ms-factura` (restringido), Marta como lectora descarga pero su subida se rechaza; Luis como proponente sube |
| CA-R08 | Un token `read` de Ana (responsable) no puede asignar roles ni publicar |
| CA-R09 | Ser admin no da acceso a un espacio restringido |
| CA-R10 | Cada asignación, cambio y denegación aparece en la auditoría con actor, objetivo, antes y después |
| CA-R11 | La prueba E2E de trazabilidad con el cliente oficial 3.3.2 sigue pasando |

## 9. Compatibilidad con Engram

| Dato existente de Engram | Equivale a |
|---|---|
| Principal con `role = admin` sin rol Nexo | Hasta la unificación: admin (el de bootstrap: superadmin). Desde `design.md` §11 la migración lo materializa una vez como asignación de Nexo y la columna se deriva del rol de Nexo; una columna `admin` sin asignación ya no concede nada |
| Principal con `role = member` | miembro |
| Fila en `cloud_project_grants` sin rol Nexo en ese proyecto | publicador en ese proyecto (es lo que permite Engram hoy) |

## 10. Decisiones

Tomadas: mismos roles en proyectos y espacios; rol por ámbito; espacios abiertos por defecto;
plataforma con superadmin, admin y otros roles según necesidad; un publicador puede aprobar su propia
propuesta; solo ciertos usuarios actualizan los espacios y los devs escriben directo en proyectos.

Aprobadas por el usuario el 2026-10-12, tal como se proponen:

- D-1. Rol **responsable** por ámbito.
- D-2. En proyectos, proponente cambia lo suyo y publicador también lo de otros.
- D-3. Proyectos restringidos por defecto.
- D-4. Equivalencias de compatibilidad de la sección 9.
- D-5. Rol implícito por espacio: lector por defecto, o proponente.
- D-6. Rol **auditor** de plataforma desde el inicio.
- D-7. Los roles de plataforma no dan acceso a contenido (R-6), incluido el superadmin.
