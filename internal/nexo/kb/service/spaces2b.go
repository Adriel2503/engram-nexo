package service

import (
	"context"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
)

// CanCreateSpace informa si la identidad puede crear espacios (panel, NX-10
// 2b). No registra denegación: solo decide si se muestra el formulario.
func (s *Service) CanCreateSpace(ctx context.Context, id authz.Identity) (bool, error) {
	d, err := s.eng.Can(ctx, id, authz.PlatformScope(), authz.CapPlatformScopeCreate)
	if err != nil {
		return false, err
	}
	return d.Allowed, nil
}
