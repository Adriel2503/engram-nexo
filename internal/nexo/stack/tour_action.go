package stack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/client"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
	"net/url"
	"strconv"
)

// tourAction expone las mismas acciones API del recorrido sin depender de un asistente.
func (a *App) tourAction(ctx context.Context, s state.State, o Options) error {
	role := "proponente"
	if o.Action == "aprobar" {
		role = "publicador"
	}
	if o.Action == "auditoria" {
		role = "auditor"
	}
	access, e := render.ReadAccess(s.Dir)
	if e != nil {
		return e
	}
	token := ""
	for _, p := range access {
		if p.Username == role {
			token = p.Token
		}
	}
	if token == "" {
		return errors.New("falta la persona del recorrido; solicita su rol al superadmin")
	}
	a.values = append(a.values, token)
	c := api(s, token)
	c.HTTP = a.HTTP
	switch o.Action {
	case "proponer":
		p := client.ProposeParams{Space: "backend", DocKey: "tour-jitter-ejemplo.md", Title: "Reintentos con jitter (EJEMPLO)", Content: "# Reintentos con jitter (EJEMPLO)\n\nAplicar una espera aleatoria entre reintentos para distribuir las solicitudes de prueba.", Rationale: "Recorrido sintético de Nexo"}
		var response struct {
			ID       json.Number `json:"id"`
			Proposal struct {
				ID json.Number `json:"id"`
			} `json:"proposal"`
		}
		if e = c.Do(ctx, "POST", "/nexo/kb/v1/spaces/backend/proposals", p, &response); e != nil {
			return errors.New("no se pudo crear la propuesta; carga el ejemplo y confirma el rol en backend")
		}
		id := response.ID.String()
		if id == "" {
			id = response.Proposal.ID.String()
		}
		if _, e = strconv.ParseUint(id, 10, 64); e != nil {
			return errors.New("la propuesta se creó; consulta su identificador en el panel")
		}
		_, _ = fmt.Fprintln(a.Out, "Propuesta de ejemplo creada. ID: "+id+". Aprueba con tour --action aprobar --proposal-id "+id+" y el mismo modo/nombre.")
	case "aprobar":
		if n, e := strconv.ParseUint(o.ProposalID, 10, 64); e != nil || n == 0 {
			return errors.New("indica --proposal-id con el identificador de la propuesta del recorrido")
		}
		if e = c.Do(ctx, "POST", "/nexo/kb/v1/proposals/"+url.PathEscape(o.ProposalID)+":approve", map[string]any{"note": "Aprobación del recorrido (EJEMPLO)", "force": false}, nil); e != nil {
			return errors.New("no se aprobó la propuesta; comprueba su versión y los permisos")
		}
		_, _ = fmt.Fprintln(a.Out, "Propuesta aprobada por publicador.")
	case "auditoria":
		var events struct {
			Items []json.RawMessage `json:"items"`
		}
		if e = c.Do(ctx, "GET", "/nexo/admin/audit?limit=20", nil, &events); e != nil {
			return errors.New("no se pudo consultar la auditoría")
		}
		_, _ = fmt.Fprintf(a.Out, "Auditoría consultada como auditor: %d eventos, sin mostrar contenido ni credenciales.\n", len(events.Items))
	default:
		return errors.New("--action admite proponer, aprobar o auditoria")
	}
	return nil
}
