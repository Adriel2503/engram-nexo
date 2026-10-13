package dashboard

// Nexo IA · SG-10 — datos de la página de cuentas de servicio del panel
// (nexo_service_accounts.templ). Los arma cloudserver
// (nexo_service_accounts_dashboard.go) desde la misma API que la CLI; aquí
// solo hay texto ya formateado, sin tipos de Nexo.

// NexoSAPage es la página /dashboard/admin/service-accounts.
type NexoSAPage struct {
	CSRF      string
	Notice    string
	Error     string
	CanManage bool // platform.users.manage: alta, deshabilitar y habilitar
	CanTokens bool // platform.tokens.manage: emitir, rotar y revocar
	ShowAll   bool
	Accounts  []NexoSARow
	Selected  *NexoSADetail
	// NewToken es el token recién emitido: se muestra esta sola vez.
	NewToken     string
	NewTokenNote string
}

// NexoSARow es una cuenta en el listado.
type NexoSARow struct {
	Slug        string
	ID          string
	Description string
	Owner       string
	Enabled     bool
	Roles       string
	// Usage es el último uso en texto ("usada hoy", "sin uso desde hace 200
	// días", "nunca usada (creada hace 10 días)").
	Usage string
	// Stale: sin uso en NEXO_SA_STALE_DAYS días (solo aviso; no se
	// deshabilita).
	Stale bool
}

// NexoSADetail es la cuenta elegida con sus tokens.
type NexoSADetail struct {
	NexoSARow
	CreatedBy string
	CreatedAt string
	// StaleNote explica la marca "Sin uso" (solo si Stale).
	StaleNote string
	Tokens    []NexoSATokenRow
}

// NexoSATokenRow es un token de la cuenta (sin secreto).
type NexoSATokenRow struct {
	ID       string
	Name     string
	Scope    string
	Expires  string
	State    string
	Active   bool
	LastUsed string
	LastSeen string
}

func nexoSAState(enabled bool) string {
	if enabled {
		return "Habilitada"
	}
	return "Deshabilitada"
}

func nexoSAChip(enabled bool) string {
	if enabled {
		return "nx-chip nx-chip-ok"
	}
	return "nx-chip nx-chip-ret"
}

func nexoSAListURL(all bool) string {
	if all {
		return "/dashboard/admin/service-accounts?all=1"
	}
	return "/dashboard/admin/service-accounts"
}

func nexoSAURL(slug string) string {
	return "/dashboard/admin/service-accounts?sa=" + slug
}

func nexoSAAction(slug, op string) string {
	return "/dashboard/admin/service-accounts/" + slug + "/" + op
}

// nexoSAToggleOp es la acción del botón de estado.
func nexoSAToggleOp(enabled bool) string {
	if enabled {
		return "disable"
	}
	return "enable"
}
