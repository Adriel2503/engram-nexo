package dashboard

// NX-07: nombres de alcance en la entrega de activación.
func nexoActivationScope(scope string) string {
	if scope == "read" {
		return "Solo lectura"
	}
	return "Lectura y escritura"
}
