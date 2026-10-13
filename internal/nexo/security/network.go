package security

// Fase S2-04 (RS-13): redes desde las que se pueden usar las capacidades de
// administración de plataforma (platform.admins.manage, platform.settings).

import (
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
)

// SuperadminAllowedCIDRsEnv es la lista de redes permitidas (CIDR o IP,
// separadas por comas o espacios). Vacía = sin restricción (con aviso).
const SuperadminAllowedCIDRsEnv = "NEXO_SUPERADMIN_ALLOWED_CIDRS"

// NetworkAllowlist es la lista interpretada.
type NetworkAllowlist struct {
	Configured bool           // la variable tiene algún valor
	Prefixes   []netip.Prefix // redes válidas
	Invalid    []string       // entradas ignoradas
}

// ParseCIDRs interpreta la lista. Una IP suelta equivale a /32 (o /128).
func ParseCIDRs(raw string) NetworkAllowlist {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == ';' || r == '\n' })
	out := NetworkAllowlist{Configured: strings.TrimSpace(raw) != ""}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				out.Invalid = append(out.Invalid, f)
				continue
			}
			if p.Addr().Is4In6() {
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out.Prefixes = append(out.Prefixes, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			out.Invalid = append(out.Invalid, f)
			continue
		}
		a = a.Unmap()
		out.Prefixes = append(out.Prefixes, netip.PrefixFrom(a, a.BitLen()))
	}
	return out
}

func (n NetworkAllowlist) String() string {
	if !n.Configured {
		return "sin restricción"
	}
	parts := make([]string, 0, len(n.Prefixes))
	for _, p := range n.Prefixes {
		parts = append(parts, p.String())
	}
	if len(parts) == 0 {
		return "ninguna red válida (se niega todo)"
	}
	return strings.Join(parts, ", ")
}

var cidrCache struct {
	mu  sync.Mutex
	raw string
	set bool
	val NetworkAllowlist
}

// SuperadminAllowlist lee la variable (con caché por valor: el motor se arma
// en cada petición y la lista no se vuelve a interpretar si no cambió).
func SuperadminAllowlist() NetworkAllowlist {
	raw := os.Getenv(SuperadminAllowedCIDRsEnv)
	cidrCache.mu.Lock()
	defer cidrCache.mu.Unlock()
	if cidrCache.set && cidrCache.raw == raw {
		return cidrCache.val
	}
	v := ParseCIDRs(raw)
	if len(v.Invalid) > 0 {
		warnOnce("cidrs", fmt.Sprintf("[nexo] %s: entradas inválidas ignoradas: %s", SuperadminAllowedCIDRsEnv, strings.Join(v.Invalid, ", ")))
	}
	cidrCache.raw, cidrCache.set, cidrCache.val = raw, true, v
	return v
}
