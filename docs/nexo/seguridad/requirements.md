# Endurecimiento de seguridad: requisitos

Estado: borrador para revisión. Fecha: 2026-10-12. Identificador: **NX-07**. Rama: `feat/seguridad`.
Origen: `05-investigacion/claudedocs/research_seguridad_bd_permisos_2026-10-12.md` (repositorio
nexo_ia), que compara lo construido (NX-01..NX-05) con OWASP (Top 10:2025 A01, ASVS 5.0 V8, guías de
autorización, base de datos y registros, API Security Top 10), ISO/IEC 27002:2022 y NIST SP 800-53.
Incluye NX-06 (caducidad y alcance de tokens), que estaba planificado como etapa R4 de roles.

## 1. Objetivo

Cerrar las brechas de seguridad alrededor de la base de datos, los tokens, el abuso de la API y la
evidencia de auditoría **antes** de construir los espacios de conocimiento (NX-10) y de distribuir el
cliente Nexo a los desarrolladores, para que todo lo nuevo nazca sobre una base segura.

Relación con el producto (MVP): RN-09 (no exponer secretos), HU-17 (información sensible y permisos),
RN-11 y HU-29/HU-30 (solo responsables publican: requiere que nadie pueda saltarse los permisos ni
borrar la evidencia), RU-12 (avisos comprensibles ante fallos).

## 2. Requisitos

### Fase S1. Base de datos

- **RS-01.** El servidor usa **dos cuentas de Postgres**: una dueña del esquema, usada solo para migrar, y una de aplicación sin superusuario, sin propiedad de tablas y sin derecho a crear objetos. (OWASP DB CS; CIS PostgreSQL)
- **RS-02.** La cuenta de aplicación tiene solo los privilegios necesarios por tabla. En las tablas de evidencia (`nexo_activity_log`, `nexo_authz_events`, `cloud_auth_audit_log`, `cloud_mutations` y, en NX-10, `nexo_kb_events` y `nexo_kb_versions`) solo `SELECT` e `INSERT`: **sin `UPDATE`, `DELETE` ni `TRUNCATE`**. (Logging CS; NIST AU-9; ISO 8.15)
- **RS-03.** La depuración por retención no depende de una variable que la aplicación pueda activar: la hace una función de la cuenta dueña que solo borra filas más antiguas que el plazo configurado, con un mínimo de 30 días, y deja un evento.
- **RS-04.** Compatibilidad: si solo se configura una cuenta (como hoy), el servidor funciona igual y registra una advertencia clara al iniciar (`seguridad degradada: una sola cuenta de base de datos`).
- **RS-05.** Conexión cifrada a Postgres configurable (`sslmode=verify-full` con certificado), documentada para producción; en demostración local puede seguir sin TLS.
- **RS-06.** Plantilla de despliegue (`deploy/nexo/`) que crea ambas cuentas y aplica los privilegios.

### Fase S2. Abuso y escalada

- **RS-10.** Límite de peticiones por IP y por token en el servidor, con valores por defecto razonables y configurables por grupo de rutas (sincronización, administración, login del panel, `/nexo/*`). Al exceder: `429` con `Retry-After`. (A01)
- **RS-11.** Fallos repetidos de autenticación (token inválido, login del panel) por IP o por usuario bloquean temporalmente ese origen y quedan auditados. (Logging CS; ISO 8.5)
- **RS-12.** Nadie puede darse permisos a sí mismo por la vía de Engram (`cloud_project_grants`, `/admin/*` y panel): asignarse acceso a un proyecto exige que otra persona con capacidad lo haga. Se aplica también a la API Nexo (R-11 de roles). (ISO 5.3; NIST AC-5)
- **RS-13.** Las capacidades de administración de plataforma (`platform.admins.manage`, `platform.settings`) solo se pueden usar desde las redes permitidas (`NEXO_SUPERADMIN_ALLOWED_CIDRS`; vacío = sin restricción, con advertencia). (ASVS 8.4.2)

### Fase S3. Tokens (NX-06)

- **RS-20.** Cada token tiene **caducidad**: por defecto 90 días, máximo configurable (365 por defecto); "sin caducidad" solo para cuentas de servicio y solo si lo emite un superadmin. (A01; ISO 5.17; NIST AC-2)
- **RS-21.** Cada token tiene **alcance** `read` o `write` (por defecto `write` para personas, como hoy). Un token `read` no puede subir cambios de proyecto, publicar, proponer ni administrar. (Utopia 0014; R-2 de roles)
- **RS-22.** Un token vencido o revocado se rechaza en la siguiente petición; el rechazo indica "token vencido" sin revelar más.
- **RS-23.** Los tokens existentes sin caducidad siguen funcionando y aparecen en el reporte de revisión (RS-33) para rotarlos.
- **RS-24.** La API admin, el panel y `/nexo/authz/v1/me` muestran caducidad, alcance y último uso.

### Fase S4. Evidencia y monitoreo

- **RS-30.** **Cadena de hashes** en `cloud_auth_audit_log`, `nexo_authz_events` y (en NX-10) `nexo_kb_events`: cada fila guarda el hash de su contenido y del hash anterior. Un comando y una ruta para el auditor verifican la cadena y reportan huecos o alteraciones. (Logging CS; ISO 8.15; NIST AU-9)
- **RS-31.** **Exportación** diaria de los registros de auditoría a archivos JSONL firmados (HMAC) fuera de la base, con su verificación. (Logging CS)
- **RS-32.** **Alertas**: reglas configurables (denegaciones o fallos de login repetidos, cambios de rol de plataforma, uso del token compartido, uso de token vencido, verificación de cadena fallida, registro detenido) que generan filas en `nexo_alerts` y, opcionalmente, un webhook (Teams o Slack). (A01; Logging CS; ISO 8.16)
- **RS-33.** **Revisión de accesos**: reporte para auditor y superadmin con roles por persona y ámbito, antigüedad, último uso de cada token, tokens sin caducidad, cuentas inactivas más de N días; registrar que la revisión se hizo. Opción de deshabilitar cuentas inactivas (desactivada por defecto). (ISO 5.18; NIST AC-2, AC-2(3))
- **RS-34.** **Retención** documentada y configurable por tabla (actividad 180 días; auditoría, eventos de roles y alertas 365 días como mínimo); nada se borra antes del plazo.

### Fase S5. Respuestas y pruebas

- **RS-40.** Ninguna respuesta de `/admin/*`, `/nexo/*` ni del panel incluye hashes de token, secretos ni metadatos internos a quien no tiene la capacidad correspondiente (BOPLA, ASVS 8.2.3). Revisión y pruebas por ruta.
- **RS-41.** Todas las rutas Nexo declaran su capacidad en un registro; una prueba falla si aparece una ruta sin capacidad declarada o una ruta de Engram nueva sin revisar. (API5)
- **RS-42.** Pruebas de autorización (matriz, BOLA, BFLA) se ejecutan en CI como puerta. (A01; API1)
- **RS-43.** Los registros nunca contienen tokens, contraseñas ni cabeceras `Authorization`; prueba automática que lo verifica. (Logging CS; RN-09)

## 3. Requisitos no funcionales

| ID | Requisito | Valor |
|---|---|---|
| RNF-S1 | Sobrecosto del límite de peticiones | < 0,2 ms por petición |
| RNF-S2 | Sobrecosto de la cadena de hashes al insertar | < 1 ms por evento |
| RNF-S3 | Verificación de cadena | 1 millón de eventos en < 60 s |
| RNF-S4 | Compatibilidad | Cliente oficial Engram 3.3.2 sigue sincronizando; prueba E2E de trazabilidad sin cambios salvo los tokens de prueba, que se emiten con caducidad |
| RNF-S5 | Aislamiento | Código en `internal/nexo/...` y archivos `nexo_*.go`; enganches marcados `// NX-07` |

## 4. Criterios de aceptación

| ID | Criterio |
|---|---|
| CA-S01 | Con dos cuentas configuradas, la cuenta de aplicación no puede `UPDATE`, `DELETE`, `TRUNCATE`, `ALTER TABLE ... DISABLE TRIGGER` ni crear objetos (verificado con SQL directo) |
| CA-S02 | La depuración borra solo filas más antiguas que el plazo; intentar borrar filas recientes falla aunque se active cualquier variable de sesión |
| CA-S03 | Con una sola cuenta, el servidor arranca y avisa de la seguridad degradada |
| CA-S04 | 1 000 peticiones en 10 s desde una IP reciben 429 a partir del límite; otra IP no se ve afectada |
| CA-S05 | 20 tokens inválidos seguidos desde una IP bloquean esa IP temporalmente y queda un evento |
| CA-S06 | Un admin no puede darse acceso a un proyecto por `/admin/*`, el panel ni la API Nexo |
| CA-S07 | Con `NEXO_SUPERADMIN_ALLOWED_CIDRS` configurado, crear un admin desde otra red da 403 auditado |
| CA-S08 | Un token emitido hoy vence a los 90 días; uno vencido da 401 "token vencido"; uno `read` no puede subir cambios de proyecto ni administrar |
| CA-S09 | Alterar una fila de auditoría por SQL (con la cuenta dueña) hace fallar la verificación de la cadena, indicando la fila |
| CA-S10 | La exportación diaria genera un archivo firmado; modificarlo hace fallar su verificación |
| CA-S11 | Cinco denegaciones en un minuto de la misma persona generan una alerta y un webhook de prueba la recibe |
| CA-S12 | El reporte de revisión lista tokens sin caducidad y cuentas inactivas; la revisión queda registrada |
| CA-S13 | Ninguna respuesta ni registro contiene un hash o un token completo (prueba automática) |
| CA-S14 | Agregar una ruta Nexo sin capacidad declarada hace fallar las pruebas |
| CA-S15 | E2E de trazabilidad con cliente oficial 3.3.2 sigue pasando |

## 5. Decisiones propuestas

- DS-1. Límite de peticiones en el servidor (en memoria por réplica). Con varias réplicas, el límite efectivo se multiplica; se documenta y se puede mover al proxy después.
- DS-2. Tokens de personas: 90 días por defecto, máximo 365; cuentas de servicio pueden no caducar solo si lo decide un superadmin.
- DS-3. Webhook de alertas opcional; sin configurarlo, las alertas quedan en la base y en la API.
- DS-4. Deshabilitar cuentas inactivas: desactivado por defecto; se activa con un plazo (por ejemplo, 90 días).
- DS-5. Segundo factor para superadmin queda fuera de esta especificación (requiere OIDC corporativo); se mitiga con restricción por red (RS-13).
- DS-6. Seguridad por fila de Postgres (RLS) y la revisión por segunda persona en espacios críticos se definen dentro de NX-10.
