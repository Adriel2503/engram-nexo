package domain

// NX-07: la redacción solo limpia la copia de salida. Una clave que decide pertenencia,
// paginación o escritura (doc_key) conserva aquí su valor original. El campo es privado:
// ni la serialización JSON ni redact.Struct lo recorren.

// KeySource guarda el valor original de una clave antes de redactar.
type KeySource struct{ value *string }

// Remember conserva el valor original (solo la primera vez).
func (s *KeySource) Remember(original string) {
	if s.value == nil {
		s.value = &original
	}
}

// Original devuelve el valor guardado; sin él, el actual (registro nunca redactado).
func (s KeySource) Original(current string) string {
	if s.value != nil {
		return *s.value
	}
	return current
}

// NexoDocKey devuelve la clave original del documento para decisiones; DocKey es solo salida.
func (d Document) NexoDocKey() string { return d.KeySource.Original(d.DocKey) }
