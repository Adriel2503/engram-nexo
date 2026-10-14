package stack

import (
	"context"
	"errors"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
)

var ErrSeedUnavailable = errors.New("semilla no disponible")

type unavailableSeeder struct{}

// NewSeeder es el punto de unión con seed.New(); el núcleo no implementa la semilla.
func NewSeeder() stackapi.Seeder { return unavailableSeeder{} }
func (unavailableSeeder) Personas() []stackapi.Persona {
	return []stackapi.Persona{{Username: "superadmin", Email: "superadmin@demo.local", Plataforma: "superadmin"}, {Username: "admin", Email: "admin@demo.local", Plataforma: "admin"}, {Username: "publicador", Email: "publicador@demo.local"}, {Username: "proponente", Email: "proponente@demo.local"}, {Username: "lector", Email: "lector@demo.local"}, {Username: "auditor", Email: "auditor@demo.local", Plataforma: "auditor"}}
}
func (unavailableSeeder) Load(context.Context, stackapi.Env, stackapi.LoadOptions) (stackapi.Report, error) {
	return stackapi.Report{}, ErrSeedUnavailable
}
func (unavailableSeeder) RemoveExample(context.Context, stackapi.Env) (stackapi.Report, error) {
	return stackapi.Report{}, ErrSeedUnavailable
}
func (unavailableSeeder) LoadOwn(context.Context, stackapi.Env, stackapi.OwnOptions) (stackapi.Report, error) {
	return stackapi.Report{}, ErrSeedUnavailable
}
