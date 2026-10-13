package authz

// NX-07 fase S2-04 (RS-13, ASVS 8.4.2): las capacidades de administración de
// plataforma más sensibles solo se usan desde redes permitidas. La lista la
// arma el servidor desde NEXO_SUPERADMIN_ALLOWED_CIDRS
// (internal/nexo/security); este paquete solo decide.

import (
	"net/netip"
	"slices"
	"strings"
)

// NetworkRestrictedCapabilities son las capacidades que exigen estar en una
// red permitida (design §3.4).
var NetworkRestrictedCapabilities = []Capability{CapPlatformAdminsManage, CapPlatformSettings}

// NetworkPolicy es la restricción por red. Sin Restricted no restringe nada.
// Restricted con Allowed vacía niega siempre (configuración inválida: fallar
// cerrado).
type NetworkPolicy struct {
	Restricted bool
	Allowed    []netip.Prefix
}

// Restricts informa si la capacidad está sujeta a la política.
func (p NetworkPolicy) Restricts(c Capability) bool {
	return p.Restricted && slices.Contains(NetworkRestrictedCapabilities, c)
}

// Allows informa si la IP está en una red permitida. Una IP ilegible o vacía
// no está permitida.
func (p NetworkPolicy) Allows(ip string) bool {
	if !p.Restricted {
		return true
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range p.Allowed {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// WithNetworkPolicy fija la restricción por red del motor.
func WithNetworkPolicy(p NetworkPolicy) Option {
	return func(e *Engine) { e.network = p }
}
