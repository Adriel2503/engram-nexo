package nexocli

// engram nexo connect | status | disconnect | setup (NX-10 tanda 2, F-04
// dentro del fork): reemplaza la parte Nexo de Install-Nexo.ps1 en Windows,
// Mac y Linux. El token nunca se imprime.

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/setup"
)

// stdin es la entrada de --token-stdin (las pruebas la reemplazan).
var stdin io.Reader = os.Stdin

func runConnection(cmd string, args []string, dataDir string, stdout, stderr io.Writer) int {
	switch cmd {
	case "connect":
		return runConnect(args, dataDir, stdout, stderr)
	case "status":
		return runStatus(args, dataDir, stdout, stderr)
	case "disconnect":
		return runDisconnect(args, dataDir, stdout, stderr)
	case "setup":
		return runSetup(args, stdout, stderr)
	}
	return 2
}

func runConnect(args []string, dataDir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "URL del servidor Nexo (https://...)")
	token := fs.String("token", "", "token personal (mejor --token-stdin o NEXO_TOKEN: no queda en el historial)")
	fromStdin := fs.Bool("token-stdin", false, "leer el token de la entrada estándar")
	asJSON := fs.Bool("json", false, "salida JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tok := strings.TrimSpace(*token)
	if *fromStdin {
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		tok = strings.TrimSpace(line)
	}
	if tok == "" {
		tok = strings.TrimSpace(os.Getenv("NEXO_TOKEN"))
	}
	if strings.TrimSpace(*server) == "" || tok == "" {
		fmt.Fprintln(stderr, "uso: engram nexo connect --server <url> (--token-stdin | --token <token> | NEXO_TOKEN)")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := client.Connect(ctx, dataDir, *server, tok)
	if err != nil {
		fmt.Fprintln(stderr, "No se conectó: "+err.Error())
		return 1
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(res)
		return 0
	}
	fmt.Fprintf(stdout, "Conectado a %s como %s (token %s).\n", res.Server, res.Label, scopeLabel(res.TokenScope))
	if res.Replaced != "" {
		fmt.Fprintf(stdout, "Aviso: la configuración cloud apuntaba a %s; ahora apunta a Nexo.\n", res.Replaced)
	}
	if res.SyncError != "" {
		fmt.Fprintf(stdout, "La copia local no se pudo sincronizar todavía (%s); se reintentará sola.\n", res.SyncError)
	} else {
		st := res.Sync.Status
		fmt.Fprintf(stdout, "Copia local %s: %d espacios, %d documentos.\n", st.Path, st.Spaces, st.Documents)
	}
	fmt.Fprintln(stdout, "Siguiente paso: engram setup <agente> (por ejemplo claude-code o codex) y reiniciar el asistente.")
	fmt.Fprintln(stdout, "Si el asistente ya estaba configurado: engram nexo setup <agente>.")
	return 0
}

func scopeLabel(s string) string {
	if s == "" {
		return "sin alcance declarado"
	}
	return "de alcance " + s
}

func runStatus(args []string, dataDir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "salida JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	conn, err := client.LoadConnection(dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	out := map[string]any{"connected": conn != nil}
	if conn != nil {
		out["connection"] = conn
	}
	kb, err := client.OpenKB(dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer kb.Close()
	if kb.API != nil {
		kb.API.HTTP.Timeout = 10 * time.Second
	}
	w := kb.Whoami(context.Background())
	for k, v := range w {
		out[k] = v
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(out)
		return 0
	}
	if conn == nil {
		fmt.Fprintln(stdout, "Nexo: esta PC no está conectada (engram nexo connect --server <url> --token-stdin).")
	} else {
		fmt.Fprintf(stdout, "Nexo: conectado a %s como %s desde %s.\n", conn.Server, conn.Label, conn.ConnectedAt.Local().Format("2006-01-02 15:04"))
	}
	if srv, ok := w["server"].(map[string]any); ok {
		switch {
		case srv["configured"] == false:
			fmt.Fprintln(stdout, "Servidor: no configurado.")
		case srv["reachable"] == true && srv["message"] == nil:
			label := ""
			if id, ok := w["identity"].(map[string]any); ok {
				label = fmt.Sprint(id["label"])
			}
			fmt.Fprintf(stdout, "Servidor: %v responde; identidad %s.\n", srv["url"], label)
		default:
			fmt.Fprintf(stdout, "Servidor: %v (%v).\n", srv["url"], srv["message"])
		}
	}
	if st, ok := w["local_copy"].(client.Status); ok {
		if st.SyncedAt == nil {
			fmt.Fprintf(stdout, "Copia local %s: vacía.\n", st.Path)
		} else {
			fmt.Fprintf(stdout, "Copia local %s: %d espacios, %d documentos; actualizada %s (%s).\n", st.Path, st.Spaces, st.Documents,
				st.SyncedAt.Local().Format("2006-01-02 15:04"), st.Age)
		}
	}
	return 0
}

func runDisconnect(args []string, dataDir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("disconnect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keep := fs.Bool("keep-copy", false, "conservar la copia local")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	removed, err := client.Disconnect(dataDir, *keep)
	if err != nil {
		fmt.Fprintln(stderr, "No se pudo desconectar: "+err.Error())
		return 1
	}
	fmt.Fprintln(stdout, "Desconectado de Nexo: se quitaron la conexión y el token guardados.")
	if removed != "" {
		fmt.Fprintf(stdout, "Copia local borrada: %s\n", removed)
	}
	fmt.Fprintln(stdout, "Las herramientas nexo_* dejan de aparecer al reiniciar el asistente. La memoria de proyectos de Engram no se toca.")
	return 0
}

func runSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "uso: engram nexo setup <agente>   (agentes: los de engram setup)")
		return 2
	}
	files, err := setup.NexoInstallProtocol(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "No se instaló el protocolo Nexo: "+err.Error())
		return 1
	}
	for _, f := range files {
		fmt.Fprintf(stdout, "Protocolo Nexo: %s\n", f)
	}
	fmt.Fprintln(stdout, "Reinicia el asistente para que cargue el protocolo.")
	return 0
}
