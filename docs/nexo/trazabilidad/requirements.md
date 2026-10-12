# Requerimientos funcionales — Trazabilidad en Engram Cloud

**Proyecto:** Nexo IA (fork `engram-nexo`, base Engram v3.3.2)
**Cambios:** NX-01 autoría por cambio · NX-02 registro de conexiones · NX-04 metadatos de la conexión · NX-03 auditoría consultable
**Orden de construcción:** NX-01 → NX-02 → NX-04 → NX-03
**Estado:** borrador para aprobación · **Fecha:** 2026-10-12

---

## 1. Contexto y problema

Engram Cloud reparte entre las PCs del equipo lo que cada persona sube (en Nexo, sobre todo `nexo-transversal`). Hoy:

- Se guarda **qué** cambió y **cuándo**, pero **no quién**: la bitácora de cambios no tiene autor.
- El único autor que existe (por paquete subido) lo **declara la propia PC** (usuario de Windows), así que se puede falsificar.
- No queda registro de **quién se conecta**: solo el último uso de cada token, que se sobrescribe.
- Engram guarda una auditoría de seguridad (logins, rechazos, gestión de usuarios y tokens), pero **no se puede consultar** desde ninguna parte, y no guarda la IP de origen.

**Objetivo:** que el administrador pueda responder, con datos confiables, **quién creó, modificó o borró cada recuerdo**, **quién se conecta, cuándo y desde dónde**, y **qué pasó en la seguridad del servidor**.

**Principio:** cada persona se identifica **únicamente por el token que el administrador le asignó**. Lo que declare la PC nunca es identidad.

## 2. Actores

| Actor | Descripción |
|---|---|
| **Desarrollador** | Persona con usuario y token asignado. Sincroniza desde su PC con el Engram oficial |
| **Administrador** | Usuario con rol `admin` y token asignado. Gestiona usuarios y tokens en el panel y consulta la trazabilidad |
| **Servidor** | `engram cloud serve` del fork. Decide la autoría y registra la actividad |

## 3. Alcance

**Incluye:** cambios en el servidor (`engram cloud serve`) y en su base Postgres.

**No incluye:**
- Cambios en las PCs: ni en el SQLite local ni en el cliente. Los desarrolladores siguen con el **Engram oficial**.
- Pantallas nuevas en el panel: en esta entrega la consulta es por API JSON (NX-03).
- Datos que solo la PC puede enviar (correo de git, versión del cliente): requieren modificar el cliente; se evaluarán después.
- Permisos o roles (quién **puede** escribir), caducidad de tokens y scope por token: etapa posterior.

## 4. NX-01 — Autoría por cambio

**RF-01 · Autor de cada cambio.** Cada cambio que llega al servidor (crear, modificar o borrar un recuerdo, una sesión o una relación) queda registrado con su autor.

**RF-02 · Identidad por token asignado.** El autor es **el usuario dueño del token** con el que se autenticó la petición. El servidor lo determina buscando el token en Postgres; lo que declare la PC se ignora como identidad.

**RF-03 · Token usado.** Además de la persona, se registra **qué token** se usó (identificador y nombre del token, nunca su valor), para distinguir máquinas o agentes de una misma persona.

**RF-03b · Copia de nombre y email.** Cada cambio guarda además una **copia del nombre y del email** del dueño del token en ese momento, para que el historial siga mostrando quién fue aunque luego el usuario se renombre, cambie de email o se desactive.

**RF-04 · Sin token asignado = no identificado.** Si la petición no usa un token asignado a una persona (token compartido o servidor sin autenticación), el cambio queda como **«no identificado»**. Lo que declare la PC se guarda solo como referencia, en un campo aparte, y nunca se muestra como autor.

**RF-05 · Ambos caminos de subida.** La regla aplica igual a la sincronización manual (CLI, hooks, `Sync-Nexo.ps1`) y a la automática.

**RF-06 · Borrados.** Un borrado registra su autor igual que una creación.

**RF-07 · Historial.** Para un recuerdo (`sync_id`) se puede obtener la lista ordenada de sus versiones con fecha, operación, autor, email, token y si estaba identificado.

**RF-08 · Datos anteriores.** Los cambios guardados antes de esta versión quedan sin autor (vacío); no se inventa uno.

| ID | Dado | Cuando | Entonces |
|---|---|---|---|
| CA-01 | Ana tiene usuario y token asignado | sube un recuerdo nuevo a `nexo-transversal` con `engram sync --cloud` | el cambio queda con autor `ana` y su token |
| CA-02 | La PC de Ana declara ser `Administrador` (variable `USERNAME`) | Ana sube un cambio con su token | el autor guardado es `ana`, no `Administrador` |
| CA-03 | Luis edita el recuerdo creado por Ana | sincroniza | el historial muestra la versión 1 de `ana` y la versión 2 de `luis` |
| CA-04 | Luis borra un recuerdo | sincroniza | el cambio de borrado queda con autor `luis` |
| CA-05 | Alguien usa el token compartido | sube un cambio | el cambio queda **«no identificado»**; lo que declaró la PC solo queda como referencia |
| CA-06 | Ana tiene dos tokens («laptop-ana» y «ci») | sube desde cada uno | cada cambio indica qué token se usó |
| CA-07 | Hay cambios antiguos sin autor | se consulta el historial | aparecen con autor vacío, sin error |
| CA-08 | Ana subió un cambio con email `ana@rp.pe`; luego el admin cambia su email | se consulta el historial | el cambio muestra `ana@rp.pe` (copia) y el email actual del usuario |

## 5. NX-02 — Registro de conexiones

**RF-10 · Registro de subidas.** Cada subida aceptada (paquete o cambios) queda registrada con: persona, token, proyecto, operación, cantidad de cambios, fecha y hora, IP de origen y cliente.

**RF-11 · Registro de descargas útiles.** Cada descarga que **entrega datos** queda registrada con los mismos campos. Las consultas periódicas que no devuelven nada **no** se registran una por una (ver RF-12).

**RF-12 · Presencia.** Para cada combinación persona + token se mantiene: primera conexión, última conexión, última IP, último cliente y contadores de subidas y descargas.

**RF-13 · Inmutable.** El registro de actividad no se puede editar ni borrar desde la aplicación; solo se agregan filas.

**RF-14 · Sin secretos.** Nunca se guarda el valor de un token ni el contenido de los recuerdos en el registro de actividad.

**RF-15 · Conservación.** El registro detallado se conserva un número de días configurable (por defecto 180); la presencia se conserva siempre.

**RF-16 · Sin impacto.** Si falla la escritura del registro, la sincronización del desarrollador **no** falla; el error se reporta en el log del servidor.

| ID | Dado | Cuando | Entonces |
|---|---|---|---|
| CA-10 | Ana sube 3 cambios | la subida termina bien | hay una fila de actividad: `ana`, su token, `nexo-transversal`, subida, 3 cambios, IP, hora |
| CA-11 | Luis descarga y recibe 3 cambios | termina bien | hay una fila de descarga con 3 cambios |
| CA-12 | La sincronización automática de Luis consulta 100 veces sin novedades | — | no hay 100 filas; su presencia muestra la última conexión actualizada |
| CA-13 | El administrador consulta la presencia | — | ve cada persona y token con primera y última conexión, última IP y contadores |
| CA-14 | Una subida es rechazada (proyecto pausado o sin permiso) | — | no se registra como actividad exitosa (queda en la auditoría de seguridad) |
| CA-15 | Postgres rechaza la escritura del registro | Ana sube | la subida de Ana funciona igual |
| CA-16 | Se intenta `UPDATE` o `DELETE` sobre el registro | — | la base lo rechaza |

## 6. NX-04 — Metadatos de la conexión (enfoque Utopia: sin tocar el cliente)

**RF-20 · Contexto de la petición.** Para toda petición al servidor se obtienen, una sola vez, la **IP de origen** y el **cliente** (User-Agent), y quedan disponibles para todos los registros (actividad, presencia y auditoría de seguridad).

**RF-21 · IP detrás del proxy.** Si el servidor está detrás del proxy HTTPS, la IP real se toma de la cabecera del proxy **solo** si ese proxy está configurado como confiable; si no, se usa la IP de la conexión.

**RF-22 · Lo que la PC ya envía.** El usuario de sistema o nombre de equipo que el cliente oficial ya manda (`created_by`) se guarda como **referencia** en la actividad y en la presencia («último valor declarado por la PC»). Nunca se usa como identidad.

**RF-23 · Token = máquina o agente.** Al crear tokens, el administrador les da un nombre que identifique la máquina o el uso (por ejemplo `laptop-ana`, `ci-pipeline`). Los registros muestran ese nombre junto al usuario.

**RF-24 · Auditoría con origen.** Los eventos de la auditoría de seguridad de Engram (logins al panel, rechazos de acceso, gestión de usuarios, tokens y permisos) guardan también la IP y el cliente.

| ID | Dado | Cuando | Entonces |
|---|---|---|---|
| CA-20 | Ana sincroniza desde la IP 10.0.0.15 | — | su actividad y su presencia muestran `10.0.0.15` y el cliente |
| CA-21 | El servidor está detrás de Caddy, configurado como proxy confiable | Ana sincroniza | se registra la IP de Ana, no la de Caddy |
| CA-22 | Una petición trae una cabecera de IP falsa y no viene de un proxy confiable | — | se ignora la cabecera y se registra la IP real de la conexión |
| CA-23 | La PC de Luis envía `created_by = LUIS-PC` | sincroniza | la presencia muestra `LUIS-PC` como «declarado por la PC», separado del autor |
| CA-24 | Alguien intenta entrar al panel con un token inválido | — | el rechazo queda auditado con su IP y cliente |

## 7. NX-03 — Auditoría consultable

**RF-30 · Solo administradores.** Solo un usuario con rol `admin` y token asignado puede consultar la trazabilidad. Los demás reciben «prohibido».

**RF-31 · Auditoría de seguridad.** Consultar los eventos de seguridad (logins, rechazos, alta y baja de usuarios, emisión y revocación de tokens, permisos) con filtros por fechas, acción, actor y resultado.

**RF-32 · Actividad.** Consultar el registro de conexiones con filtros por persona, token, proyecto, operación y fechas.

**RF-33 · Presencia.** Consultar quién se conecta: cada persona y token con primera y última conexión, última IP, último cliente y contadores.

**RF-34 · Historial de un recuerdo.** Consultar todas las versiones de un recuerdo con autor, email, token, fecha y operación. El contenido completo de cada versión solo se incluye si se pide explícitamente.

**RF-35 · Paginación.** Todas las consultas devuelven resultados por páginas (máximo 200 por página), de lo más reciente a lo más antiguo.

**RF-36 · La consulta se audita.** Cada consulta de trazabilidad queda registrada en la auditoría de seguridad (quién consultó y qué).

| ID | Dado | Cuando | Entonces |
|---|---|---|---|
| CA-30 | Ana (rol member) | consulta la auditoría | recibe «prohibido» |
| CA-31 | El admin | consulta la auditoría de la última semana filtrando rechazos | ve los rechazos con fecha, actor, IP y motivo |
| CA-32 | El admin | consulta la actividad de Luis en `nexo-transversal` | ve sus subidas y descargas con fecha, cantidad, IP y token |
| CA-33 | El admin | consulta la presencia | ve a Ana y Luis con sus tokens y última conexión |
| CA-34 | El admin | consulta el historial de un recuerdo editado por Ana y Luis | ve las versiones en orden, con autor y email de cada una |
| CA-35 | Hay 1.000 eventos | el admin consulta | recibe páginas de hasta 200 y un cursor para seguir |
| CA-36 | El admin consulta la presencia | — | la consulta queda en la auditoría de seguridad |

## 8. Reglas de negocio

| ID | Regla |
|---|---|
| RN-A1 | La única identidad es el token asignado por el administrador. Lo que declara la PC nunca es identidad |
| RN-A2 | Cada persona tiene su propio token (uno o varios, por ejemplo uno por máquina); los tokens no se comparten |
| RN-A3 | En el servidor del equipo nadie usa el token compartido de Engram: su valor no se entrega a ninguna persona |
| RN-A4 | La IP es un dato personal: se guarda solo para auditoría y con conservación limitada |
| RN-A5 | Los tokens se nombran por máquina o uso (`laptop-ana`, `ci-pipeline`) |

## 9. Requerimientos no funcionales

| ID | Requerimiento |
|---|---|
| RNF-01 | Compatibilidad: el cliente **oficial** Engram 3.3.2 sincroniza sin cambios contra el servidor del fork |
| RNF-02 | Migración automática e idempotente al iniciar el servidor; sin pérdida de datos |
| RNF-03 | Sobrecosto por sincronización menor a 5 ms en condiciones normales |
| RNF-04 | Las actualizaciones futuras de Engram deben poder integrarse con conflictos mínimos (cambios aislados y marcados) |
| RNF-05 | Las consultas de NX-03 responden en menos de 1 s con 1 millón de filas de actividad (índices adecuados) |

## 10. Decisiones

| # | Tema | Decisión | Estado |
|---|---|---|---|
| D1 | Conservación del registro detallado | 180 días, configurable | Acordado |
| D2 | IP detrás del proxy | Solo se confía en la cabecera del proxy si está configurado como confiable | Acordado |
| D3 | Presencia por IP | No: una fila por persona y token, con la última IP | Acordado |
| D4 | Metadatos de la PC | Enfoque Utopia: se obtienen en el servidor (IP, cliente, nombre del token) más lo que la PC ya envía. El correo de git queda para después | Acordado |
| D5 | Forma de consultar la auditoría | API JSON solo para administradores; pantallas después | Propuesta |
| D6 | Identidad | Solo el token asignado identifica a la persona | Acordado |
