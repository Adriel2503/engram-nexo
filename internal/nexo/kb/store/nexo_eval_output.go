package store

import "github.com/Gentleman-Programming/engram/v3/internal/nexo/redact"

// NX-07: los espacios de la evaluación se autorizan antes de redactar la salida.
func nexoRedactEvalRun(r EvalRun) EvalRun {
	r.nexoSpaces = append([]string{}, r.Spaces...)
	r.Spaces = append([]string{}, r.Spaces...)
	redact.Struct(&r)
	return r
}

// NexoAuthorizationSpaces devuelve una copia de los espacios originales.
func (r EvalRun) NexoAuthorizationSpaces() []string {
	if r.nexoSpaces != nil {
		return append([]string{}, r.nexoSpaces...)
	}
	return append([]string{}, r.Spaces...)
}
