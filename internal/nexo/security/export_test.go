package security

// Accesores solo para las pruebas de paquete externo (security_test).

// NexoExportChainColumns devuelve las columnas de la cadena que la consulta de exportación ya quita de la fila.
func NexoExportChainColumns() []string { return append([]string(nil), nexoExportChainColumns...) }

// NexoExportColumns lista las columnas declaradas de una tabla.
func NexoExportColumns(table string) []string {
	out := make([]string, 0, len(nexoExportSchemas[table]))
	for c := range nexoExportSchemas[table] {
		out = append(out, c)
	}
	return out
}
