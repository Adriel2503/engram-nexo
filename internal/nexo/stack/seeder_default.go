package stack

import (
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/seed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/stackapi"
)

// NewSeeder devuelve la semilla de ejemplo del stack (internal/nexo/stack/seed).
func NewSeeder() stackapi.Seeder { return seed.New() }
