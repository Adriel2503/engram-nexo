package store

// NexoDocKey devuelve la clave original de la propuesta (NX-07); DocKey es solo la copia de salida.
func (p Proposal) NexoDocKey() string { return p.KeySource.Original(p.DocKey) }
