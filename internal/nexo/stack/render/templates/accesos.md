# Nexo IA — entorno {{.State.Mode}}
{{if ne .State.Mode "prod"}}Solo pruebas: no usar datos reales.{{end}}

Panel: {{.State.ServerURL}}/dashboard/
Servidor: {{.State.ServerURL}}

Archivo privado: no compartir ni versionar. Los tokens vencen; renovar desde el panel.

| Usuario | Rol | Token |
| --- | --- | --- |
{{range .Access}}| {{.Username}} | {{.Role}} | {{.Token}} |
{{end}}

## Conectar el asistente

Copiar el token: engram nexo stack accesos --mode {{.State.Mode}}{{if .State.Name}} --name {{.State.Name}}{{end}} --copiar <usuario>.
Conectar: engram nexo connect --server {{.State.ServerURL}} --token-stdin (pegar el portapapeles y Enter).

- Claude Code: engram setup claude-code.
- Codex: engram setup codex.
- OpenCode: engram setup opencode.

Panel de cada persona: {{.State.ServerURL}}/dashboard/ (entrar con su token).
Detener: engram nexo stack down --mode {{.State.Mode}}{{if .State.Name}} --name {{.State.Name}}{{end}}.
Borrar: engram nexo stack reset --mode {{.State.Mode}}{{if .State.Name}} --name {{.State.Name}}{{end}}.
Recorrido: engram nexo tour --mode {{.State.Mode}}{{if .State.Name}} --name {{.State.Name}}{{end}}.
