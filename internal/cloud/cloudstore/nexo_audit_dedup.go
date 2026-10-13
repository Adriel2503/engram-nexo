package cloudstore

// Nexo IA · NX-07 arreglo A2 — agrupación de rechazos repetidos en
// cloud_auth_audit_log (docs/nexo/seguridad/design.md §8.4).
//
// Enganche // NX-07 en insertAuthAuditEvent (el punto común de auditoría de
// Engram, donde NX-04 ya agrega client_ip y user_agent): nexoAuditPrepare
// decide si un rechazo de autenticación (sync.auth, dashboard.login con
// outcome denied) se escribe completo o solo se cuenta. La clave es (acción,
// motivo, IP, huella del token presentado); la huella es un HMAC con clave
// aleatoria del proceso (security.CredentialFingerprint), nunca el token.
//
//   - Primer rechazo de la clave: fila completa, con metadata.credential_fp.
//   - Repetidos durante la ventana (NEXO_AUDIT_DEDUP_SECONDS, 600 por
//     defecto): no se escriben; se cuentan.
//   - Al cerrar la ventana: una fila resumen con la misma acción y motivo y
//     metadata nexo_repeated (conteo), nexo_first_at, nexo_last_at,
//     nexo_window_seconds y nexo_user_agents (hasta 5 clientes distintos).
//     La escribe el monitor de cloudserver cada minuto o, si llega otro
//     rechazo de la misma clave, este mismo enganche antes de seguir contando.
//
// Además, los rechazos por token revocado, persona deshabilitada o token
// vencido llevan nexo_principal_id y credential_id (id del token, no el
// token) para la alerta revoked_token_used (arreglo A1).
//
// El agrupador viaja en el contexto (WithNexoAuditDedup): lo pone el
// middleware de Nexo, uno por servidor. Sin él (pruebas de Engram con
// almacenes falsos, CLI) todo se escribe como antes.

import (
	"context"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// Claves de metadata del agrupador (A2).
const (
	NexoAuditMetaCredentialFP = "credential_fp"
	NexoAuditMetaRepeated     = "nexo_repeated"
)

type nexoAuditDedupKey struct{}

// WithNexoAuditDedup deja el agrupador de rechazos en el contexto.
func WithNexoAuditDedup(ctx context.Context, d *security.AuditDedup) context.Context {
	if d == nil {
		return ctx
	}
	return context.WithValue(ctx, nexoAuditDedupKey{}, d)
}

func nexoAuditDedupFrom(ctx context.Context) *security.AuditDedup {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(nexoAuditDedupKey{}).(*security.AuditDedup)
	return d
}

// nexoDedupActions: rechazos de autenticación que se agrupan.
var nexoDedupActions = map[string]bool{"sync.auth": true, "dashboard.login": true}

// nexoTokenReasons: rechazos con un token encontrado (se agrega su persona).
var nexoTokenReasons = map[string]bool{"token_revoked": true, "principal_disabled": true, "token_expired": true, "invalid_token": true}

// nexoAuditPrepare es el enganche de insertAuthAuditEvent. Devuelve el evento
// (con metadata enriquecida) y si hay que escribirlo.
func nexoAuditPrepare(ctx context.Context, exec authAuditEventExecutor, event AuthAuditEvent) (AuthAuditEvent, bool, error) {
	if strings.TrimSpace(event.Outcome) != "denied" || !nexoDedupActions[strings.TrimSpace(event.Action)] {
		return event, true, nil
	}
	if _, summary := event.Metadata[NexoAuditMetaRepeated]; summary {
		return event, true, nil // resumen del agrupador: se escribe tal cual
	}
	fp := security.CredentialFingerprintFrom(ctx)
	facts, hasFacts := security.TokenFactsFrom(ctx)
	reason := strings.TrimSpace(event.ReasonCode)
	addFacts := hasFacts && facts.TokenID != "" && nexoTokenReasons[reason]
	if fp == "" && !addFacts {
		return event, true, nil
	}
	meta := make(map[string]any, len(event.Metadata)+3)
	for k, v := range event.Metadata {
		meta[k] = v
	}
	if fp != "" {
		meta[NexoAuditMetaCredentialFP] = fp
	}
	if addFacts {
		meta["nexo_principal_id"] = facts.PrincipalID
		meta["credential_id"] = facts.TokenID
	}
	event.Metadata = meta

	d := nexoAuditDedupFrom(ctx)
	if d == nil || fp == "" {
		return event, true, nil
	}
	origin, _ := NexoRequestOriginFrom(ctx)
	key := security.DedupKey{Action: strings.TrimSpace(event.Action), Reason: reason, IP: strings.TrimSpace(origin.ClientIP), Fingerprint: fp}
	info := security.DedupInfo{UserAgent: origin.UserAgent, ActorSource: event.ActorSource, Project: event.Project}
	if addFacts {
		info.PrincipalID, info.CredentialID = facts.PrincipalID, facts.TokenID
	}
	write, pending := d.Observe(key, info)
	if pending != nil {
		sctx, sev := NexoAuditSummaryEvent(context.WithoutCancel(ctx), *pending)
		if err := insertAuthAuditEvent(sctx, exec, sev); err != nil {
			return event, write, err
		}
	}
	return event, write, nil
}

// NexoAuditSummaryEvent arma el evento resumen de una ventana del agrupador y
// el contexto con su IP (sin User-Agent: el resumen lleva la lista).
func NexoAuditSummaryEvent(ctx context.Context, s security.DedupSummary) (context.Context, AuthAuditEvent) {
	source := strings.TrimSpace(s.ActorSource)
	if source == "" {
		source = "request"
	}
	agents := s.UserAgents
	if agents == nil {
		agents = []string{}
	}
	meta := map[string]any{
		"source":                  source,
		NexoAuditMetaCredentialFP: s.Key.Fingerprint,
		NexoAuditMetaRepeated:     s.Repeated,
		"nexo_first_at":           s.FirstAt.UTC().Format(time.RFC3339Nano),
		"nexo_last_at":            s.LastAt.UTC().Format(time.RFC3339Nano),
		"nexo_window_start":       s.WindowStart.UTC().Format(time.RFC3339Nano),
		"nexo_window_seconds":     int(s.Window.Seconds()),
		"nexo_user_agents":        agents,
	}
	if s.OverflowAgent {
		meta["nexo_user_agents_truncated"] = true
	}
	if s.PrincipalID != "" {
		meta["nexo_principal_id"] = s.PrincipalID
	}
	if s.CredentialID != "" {
		meta["credential_id"] = s.CredentialID
	}
	ctx = WithNexoRequestOrigin(ctx, NexoRequestOrigin{ClientIP: s.Key.IP})
	return ctx, AuthAuditEvent{
		ActorSource: source,
		Project:     s.Project,
		Action:      s.Key.Action,
		Outcome:     "denied",
		ReasonCode:  s.Key.Reason,
		Metadata:    meta,
	}
}

// NexoFlushAuditDedup escribe los resúmenes de las ventanas vencidas (all =
// true: también las abiertas). Devuelve cuántos escribió.
func (cs *CloudStore) NexoFlushAuditDedup(ctx context.Context, d *security.AuditDedup, all bool) (int, error) {
	if cs == nil || cs.db == nil || d == nil {
		return 0, nil
	}
	n := 0
	for _, s := range d.Flush(all) {
		sctx, ev := NexoAuditSummaryEvent(ctx, s)
		if err := insertAuthAuditEvent(sctx, cs.db, ev); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
