package authz

import "github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"

// NX-07: redactar la copia de salida nunca cambia el ámbito autorizado.
func nexoRedactEvent(r EventRecord) EventRecord {
	r.NexoRememberScope(Scope{Type: ScopeType(r.ScopeType), Key: r.ScopeKey})
	redact.Struct(&r)
	return r
}
