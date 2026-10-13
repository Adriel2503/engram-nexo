// Package kb agrupa el módulo de espacios de conocimiento de Nexo IA (NX-10).
//
// Subpaquetes (docs/nexo/conocimiento/design.md §3):
//
//	domain   tipos y errores
//	ingest   conversores a Markdown con procedencia y Sectioner
//	store    migraciones y SQL de las tablas nexo_kb_*
//	search   interfaz Retriever, filtros comunes obligatorios y pgfts
//	service  casos de uso (espacios, publicación, retiro, búsqueda, lectura)
//	httpapi  rutas /nexo/kb/v1
//	eval     medición de la búsqueda (Recall@5, MRR)
//	cli      comando `engram nexo kb ...`
//
// Este archivo define el punto de extensión para guardar los archivos
// originales (decisión D-K11: por ahora no se guardan).
package kb

import (
	"context"
	"errors"
)

// ErrBlobNotStored indica que el almacén no guarda originales (NoopBlobStore)
// o que la referencia no existe.
var ErrBlobNotStored = errors.New("kb: original file is not stored")

// BlobRef identifica un original guardado. Es lo que iría en
// nexo_kb_versions.source_blob_ref (hoy NULL).
type BlobRef string

// BlobStore guarda los archivos originales de una publicación para poder
// reconvertir y reindexar desde ellos sin migrar contenido (design §5.4 y
// §6.1, D-K11). Las implementaciones deben ser direccionables por contenido:
// Put con el mismo sha256 devuelve la misma referencia (idempotente).
type BlobStore interface {
	// Put guarda data cuyo sha256 hexadecimal es sha256Hex y devuelve su
	// referencia. Devuelve ("", nil) si el almacén no guarda originales.
	Put(ctx context.Context, sha256Hex, mime string, data []byte) (BlobRef, error)
	// Get devuelve el original y su sha256, o ErrBlobNotStored.
	Get(ctx context.Context, ref BlobRef) (data []byte, sha256Hex string, err error)
	// Delete borra el original (solo por retención explícita, nunca por la API
	// de contenido).
	Delete(ctx context.Context, ref BlobRef) error
}

// NoopBlobStore es la implementación por defecto: no guarda nada. Put
// devuelve una referencia vacía, así source_blob_ref queda NULL y solo se
// conservan source_sha256, source_mime y source_size_bytes.
type NoopBlobStore struct{}

// Put implementa BlobStore.
func (NoopBlobStore) Put(context.Context, string, string, []byte) (BlobRef, error) { return "", nil }

// Get implementa BlobStore.
func (NoopBlobStore) Get(context.Context, BlobRef) ([]byte, string, error) {
	return nil, "", ErrBlobNotStored
}

// Delete implementa BlobStore.
func (NoopBlobStore) Delete(context.Context, BlobRef) error { return nil }
