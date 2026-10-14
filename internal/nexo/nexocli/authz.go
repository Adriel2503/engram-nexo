package nexocli

// engram nexo authz — administración de los roles de plataforma desde la
// terminal del servidor (NX-05, unificación de roles, docs/nexo/roles/design.md
// §11.5).
//
//	engram nexo authz reconcile-roles [--apply] [--json]
//
// El rol de plataforma de Nexo (miembro, auditor, admin, superadmin) es el
// único; la columna role de Engram (admin | member) se deriva de él. Con los
// triggers de la base no debería haber diferencias, pero pueden aparecer si
// alguien los desactiva o restaura una copia vieja. El comando las lista (en
// seco, por defecto) y, con --apply, recalcula la columna desde el rol de Nexo
// (nunca al revés), con un evento engram_role.synced por cada corrección.
// Es idempotente. Se conecta con ENGRAM_DATABASE_URL.
//
// Códigos de salida: 0 sin diferencias o corregidas; 1 en seco con
// diferencias (para scripts y monitoreo) o error; 2 uso incorrecto.

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/auditcli"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const authzUsage = `Uso: engram nexo authz <comando> [opciones]

Comandos:
  reconcile-roles [--apply] [--json]   Detecta personas y cuentas cuya columna de rol de Engram no coincide con
                                       su rol de plataforma de Nexo (miembro, auditor, admin, superadmin).
                                       Por defecto solo informa (en seco, sale con 1 si hay diferencias);
                                       con --apply recalcula la columna desde el rol de Nexo y deja un
                                       evento engram_role.synced por cada corrección. Es idempotente.

Conexión: ENGRAM_DATABASE_URL.
`

func runAuthz(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, authzUsage)
		return 0
	}
	switch args[0] {
	case "reconcile-roles":
		return runReconcileRoles(args[1:], stdout, stderr)
	}
	sayf(stderr, "comando desconocido: %s\n\n%s", args[0], authzUsage)
	return 2
}

func openAuthzDB() (*sql.DB, error) {
	dsn := strings.TrimSpace(os.Getenv(auditcli.DatabaseURLEnv))
	if dsn == "" {
		return nil, fmt.Errorf("%s no está definida", auditcli.DatabaseURLEnv)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("conectar con la base: %w", err)
	}
	return db, nil
}

func runReconcileRoles(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reconcile-roles", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	apply := fs.Bool("apply", false, "corregir")
	asJSON := fs.Bool("json", false, "salida JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		sayf(stderr, "opciones no válidas\n\n%s", authzUsage)
		return 2
	}
	db, err := openAuthzDB()
	if err != nil {
		sayln(stderr, "error:", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	svc := authz.NewService(db, authz.NewPostgres(db))
	res, err := svc.ReconcilePlatformRoles(context.Background(), authz.Identity{}, *apply, "cli")
	if err != nil {
		sayln(stderr, "error:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		printReconcile(stdout, res)
	}
	if !res.Applied && len(res.Drift) > 0 {
		return 1
	}
	return 0
}

func printReconcile(w io.Writer, res authz.ReconcileResult) {
	if len(res.Drift) == 0 {
		sayln(w, "Sin diferencias: la columna de rol de Engram coincide con el rol de plataforma de Nexo en todas las personas y cuentas.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	sayln(tw, "ID\tUSUARIO\tROL DE NEXO\tENGRAM HOY\tENGRAM DEBE SER")
	for _, d := range res.Drift {
		name := d.Username
		if name == "" {
			name = d.DisplayName
		}
		sayf(tw, "%s\t%s\t%s\t%s\t%s\n", d.PrincipalID, name, authz.PlatformRoleLabel(d.NexoRole), d.EngramRole, d.Expected)
	}
	_ = tw.Flush()
	if res.Applied {
		sayf(w, "\nCorregidas %d de %d diferencias (el rol de Nexo manda). Cada una quedó como evento engram_role.synced.\n", res.Fixed, len(res.Drift))
		return
	}
	sayf(w, "\n%d diferencias. En seco: no se cambió nada. Para corregirlas: engram nexo authz reconcile-roles --apply\n", len(res.Drift))
}

func sayf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func sayln(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
