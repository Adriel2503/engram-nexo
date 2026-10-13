package store

import "context"

// PassageTexts devuelve, por id de sección, el pasaje que ve el reordenador
// (NX-10 R3): ruta de títulos de la carpeta, título del documento, ruta de la
// sección y cuerpo, la misma composición que se vectoriza (VectorText, RC-03).
// Solo lee las secciones cuyos ids se pasan: quien llama ya los autorizó con
// el filtro común de la búsqueda (permisos y tiempo), no se amplía nada.
func (s *Store) PassageTexts(ctx context.Context, sectionIDs []string) (map[string]string, error) {
	if len(sectionIDs) == 0 {
		return map[string]string{}, nil
	}
	return s.vectorTexts(ctx, sectionIDs)
}
