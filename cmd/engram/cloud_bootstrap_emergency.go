package main

// Nexo IA · SG-06 — `engram cloud bootstrap emergency-token`.
//
// Acceso de emergencia ("romper el vidrio") para cuando ningún superadmin
// habilitado tiene un token vigente con escritura. Corre dentro del servidor
// (por ejemplo `docker compose exec cloud engram cloud bootstrap
// emergency-token --user <superadmin>`), con la conexión a la base de la
// configuración (ENGRAM_DATABASE_URL) y ENGRAM_CLOUD_TOKEN_PEPPER; no usa la
// red HTTP. Las reglas viven en cloudstore.NexoEmergencyToken
// (internal/cloud/cloudstore/nexo_emergency.go).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	cloudauth "github.com/Gentleman-Programming/engram/v3/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security/evidence"
)

type cloudBootstrapEmergencyArgs struct {
	user   string
	name   string
	reason string
}

// cloudBootstrapEmergencyStore lo cumple *cloudstore.CloudStore; se detecta por
// type assertion para no ampliar cloudBootstrapStore (y sus dobles de prueba).
type cloudBootstrapEmergencyStore interface {
	NexoEmergencyToken(ctx context.Context, params cloudstore.NexoEmergencyTokenParams, audit cloudstore.AuthAuditEvent) (cloudstore.NexoEmergencyResult, error)
}

func parseCloudBootstrapEmergencyArgs(args []string) (cloudBootstrapEmergencyArgs, error) {
	var out cloudBootstrapEmergencyArgs
	for i := 0; i < len(args); i++ {
		value := func(flag string) (string, error) {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", fmt.Errorf("%s requires a value", flag)
			}
			i++
			return strings.TrimSpace(args[i]), nil
		}
		var err error
		switch args[i] {
		case "--user":
			out.user, err = value("--user")
		case "--name":
			out.name, err = value("--name")
		case "--reason":
			out.reason, err = value("--reason")
		default:
			return out, fmt.Errorf("unknown flag: %s", args[i])
		}
		if err != nil {
			return out, err
		}
	}
	if out.user == "" {
		return out, fmt.Errorf("--user is required (username of an existing, enabled superadmin)")
	}
	return out, nil
}

func cmdCloudBootstrapEmergencyToken() {
	args, err := parseCloudBootstrapEmergencyArgs(os.Args[4:])
	if err != nil {
		printCloudBootstrapUsage()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		exitFunc(1)
		return
	}
	runtimeCfg := cloud.ConfigFromEnv()
	pepper := strings.TrimSpace(runtimeCfg.TokenPepper)
	if pepper == "" {
		fmt.Fprintln(os.Stderr, "error: emergency-token requires ENGRAM_CLOUD_TOKEN_PEPPER to be configured (a dedicated cloud token pepper, distinct from ENGRAM_JWT_SECRET)")
		exitFunc(1)
		return
	}
	hasher, err := cloudauth.NewManagedTokenHasher([]byte(pepper))
	if err != nil {
		fatal(fmt.Errorf("cloud bootstrap emergency-token: %w", err))
		return
	}
	cs, err := newCloudBootstrapStore(runtimeCfg)
	if err != nil {
		fatal(fmt.Errorf("cloud bootstrap emergency-token: connect cloud store: %w", err))
		return
	}
	defer func() { _ = cs.Close() }()
	store, ok := cs.(cloudBootstrapEmergencyStore)
	if !ok {
		fatal(fmt.Errorf("cloud bootstrap emergency-token: the configured store does not support emergency access"))
		return
	}

	managedToken, err := cloudauth.GenerateManagedToken("live")
	if err != nil {
		fatal(fmt.Errorf("cloud bootstrap emergency-token: generate token: %w", err))
		return
	}
	tokenHash, err := hasher.Hash(managedToken.Raw)
	if err != nil {
		fatal(fmt.Errorf("cloud bootstrap emergency-token: hash token: %w", err))
		return
	}

	ctx := context.Background()
	res, err := store.NexoEmergencyToken(ctx, cloudstore.NexoEmergencyTokenParams{
		User: args.user, TokenPrefix: managedToken.Prefix, TokenHash: tokenHash, Name: args.name, Reason: args.reason,
	}, cloudstore.AuthAuditEvent{ActorSource: string(cloudauth.PrincipalSourceBootstrapCLI)})
	if err != nil {
		var denied *cloudstore.NexoEmergencyError
		if errors.As(err, &denied) {
			// La negativa también queda en la auditoría (no es una mutación: si
			// la escritura falla solo se avisa).
			if aerr := cs.InsertAuthAuditEvent(ctx, cloudstore.AuthAuditEvent{
				ActorSource: string(cloudauth.PrincipalSourceBootstrapCLI), TargetPrincipalID: denied.TargetPrincipalID,
				Action: cloudstore.NexoEmergencyAuditAction, Outcome: cloudBootstrapAuditOutcomeDenied, ReasonCode: denied.Code,
				Metadata: map[string]any{"emergency": true, "username": args.user},
			}); aerr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to record the emergency-token denial audit event: %v\n", aerr)
			}
			fmt.Fprintf(os.Stderr, "error: acceso de emergencia denegado (%s): %s\n", denied.Code, denied.Message)
			exitFunc(1)
			return
		}
		fatal(fmt.Errorf("cloud bootstrap emergency-token: %w", err))
		return
	}

	fmt.Printf("Token de EMERGENCIA emitido para el superadmin %s: vale %d horas, hasta %s UTC, con escritura.\n",
		res.Username, int(cloudstore.EmergencyTokenTTL/time.Hour), res.ExpiresAt.Format("2006-01-02 15:04"))
	fmt.Println("SE MUESTRA UNA SOLA VEZ: copielo ahora; no se puede recuperar.")
	fmt.Println(managedToken.Raw)
	fmt.Println("Queda en la auditoria (nexo.emergency_token) y se creo la alerta emergency_access_used.")
	fmt.Println("Siguiente paso: con este token emita tokens normales para los superadmins y revoque este (manual de produccion, 7.5).")
	if res.Alert != nil {
		if w := evidence.WebhookFromEnv(); w != nil {
			wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if werr := w.Send(wctx, *res.Alert); werr != nil {
				fmt.Fprintf(os.Stderr, "warning: la alerta quedo en la base pero no se pudo enviar al webhook: %v\n", werr)
			}
		}
	}
}
