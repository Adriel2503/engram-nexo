# Manuales de Nexo IA

Manuales de usuario del fork `engram-nexo` (versión `3.4.0-nexo.2`). Empieza por el que corresponda a
tu rol.

Para especificaciones, pendientes y antecedentes, consulta el [resumen de Nexo](../README.md).

| Manual | Para quién | Qué cubre |
|---|---|---|
| [instalacion-dev.md](instalacion-dev.md) | Desarrolladores (Windows, macOS, Linux) | Instalar el binario, conectar la PC con su token, configurar el asistente, uso diario, trabajo sin conexión, actualizar, desinstalar y problemas frecuentes |
| [cliente-windows-desde-codigo.md](cliente-windows-desde-codigo.md) | Colegas con Windows, con o sin Engram oficial | Compilar main, respaldar e instalar el ejecutable y conectar Codex, Claude Code u OpenCode |
| [stack.md](stack.md) | Quien empieza, prueba o monta un entorno | start, demo/dev/prod, accesos, búsqueda, recorrido y Postgres externo |
| [servidor-local.md](servidor-local.md) | Quien prueba, desarrolla o hace demos | Levantar un servidor Nexo en la propia PC con Docker, crear el primer administrador, tokens y espacios, subir documentos y conectar el asistente a ese servidor |
| [servidor-produccion.md](servidor-produccion.md) | Operación e infraestructura | Desplegar el servidor del equipo con HTTPS, dos cuentas de base de datos y evidencia de seguridad |
| [servidor-ubuntu.md](servidor-ubuntu.md) | Quien instala una VM Ubuntu desde cero | Git, Go, compilación, PostgreSQL externo, credenciales, systemd, HTTPS y primer acceso |
| [postgresql-ubuntu-tls.md](postgresql-ubuntu-tls.md) | Quien prepara su propia base para esa VM | PostgreSQL 18 sin Docker, pgvector, dos cuentas, CA propia, TLS y embeddings |
| [traefik-ubuntu.md](traefik-ubuntu.md) | Quien publica el servidor por HTTPS | Configuración realizada con Dokploy y alternativa manual en otra VM, sin Docker |
| [administracion.md](administracion.md) | Administradores de Nexo y responsables de espacios | Personas, roles, tokens, espacios, propuestas y consultas de auditoría |

Referencias técnicas: `NEXO_CHANGES.md` (inventario de cambios y variables de entorno),
`docs/nexo/conocimiento/design.md` §19 (cliente e instalación) y
`docs/nexo/seguridad/guia-despliegue.md`.
