// Package service contiene los casos de uso de los espacios de conocimiento
// (NX-10, design §6): espacios sobre el motor de roles compartido
// (internal/nexo/authz), publicación por lotes, retiro y reemplazo, lectura,
// búsqueda y eventos. Cada escritura de contenido ocurre en una transacción
// por documento con su fila en nexo_kb_changes y su evento en nexo_kb_events,
// con el actor tomado del token (NX-01).
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/rerank"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// Variables de entorno de límites (design §8).
const (
	EnvMaxDocBytes = "NEXO_KB_MAX_DOC_BYTES" // por defecto 2 MB
	EnvMaxBatch    = "NEXO_KB_MAX_BATCH"     // por defecto 200
)

// Limits son los límites del servicio.
type Limits struct {
	MaxDocBytes int64
	MaxBatch    int
	Convert     ingest.Limits
}

// LimitsFromEnv lee los límites (valores inválidos o vacíos: por defecto).
func LimitsFromEnv() Limits {
	l := Limits{MaxDocBytes: 2 << 20, MaxBatch: 200}
	if n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(EnvMaxDocBytes)), 10, 64); err == nil && n > 0 {
		l.MaxDocBytes = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvMaxBatch))); err == nil && n > 0 {
		l.MaxBatch = n
	}
	l.Convert = ingest.Limits{MaxInputBytes: l.MaxDocBytes, MaxOutputBytes: int(l.MaxDocBytes)}
	return l
}

// Service agrupa los casos de uso.
type Service struct {
	db       *sql.DB
	st       *store.Store
	roles    *authz.Service
	eng      *authz.Engine
	reg      *ingest.Registry
	searcher *search.Searcher
	blobs    kb.BlobStore
	limits   Limits
	sections ingest.SectionerOptions
	upload   UploadLimits // NX-10 2b
	r1       R1Options
	r1Set    *R1Options
	// NX-10 R2 (r2.go)
	emb    embed.Embedder
	embCfg embed.Config
	embSet bool
	r2     R2Options
	r2Set  bool
	hybrid *search.Hybrid
	vec    *vectorState
	// NX-10 R3 (r3.go)
	rr    rerank.Reranker
	rrCfg rerank.Config
	rrSet bool
	r3    search.RerankOptions
	r3Set bool
}

// WithUploadLimits fija los límites de subida (NX-10 2b).
func WithUploadLimits(l UploadLimits) Option { return func(s *Service) { s.upload = l } }

// WithR1 fija las opciones de R1 (en lugar de las del entorno).
func WithR1(o R1Options) Option {
	return func(s *Service) { s.r1Set = &o }
}

func logf(format string, args ...any) { log.Printf(format, args...) }

// Option configura el servicio.
type Option func(*Service)

// WithRetriever reemplaza el motor de búsqueda (por defecto pgfts). Los
// filtros comunes de tiempo y permisos se aplican igual (search.Searcher).
func WithRetriever(r search.Retriever) Option {
	return func(s *Service) { s.searcher = search.NewSearcher(r) }
}

// WithBlobStore enchufa un almacén de originales (por defecto no guarda nada).
func WithBlobStore(b kb.BlobStore) Option { return func(s *Service) { s.blobs = b } }

// WithLimits fija los límites.
func WithLimits(l Limits) Option { return func(s *Service) { s.limits = l } }

// WithRegistry reemplaza el registro de conversores.
func WithRegistry(r *ingest.Registry) Option { return func(s *Service) { s.reg = r } }

// WithSectionerOptions fija el tamaño objetivo de las secciones.
func WithSectionerOptions(o ingest.SectionerOptions) Option {
	return func(s *Service) { s.sections = o }
}

// New crea el servicio sobre la base de Engram Cloud y el servicio de roles.
func New(db *sql.DB, roles *authz.Service, opts ...Option) *Service {
	st := store.New(db)
	s := &Service{db: db, st: st, roles: roles, eng: roles.Engine(), reg: DefaultRegistry(),
		blobs: kb.NoopBlobStore{}, limits: LimitsFromEnv(), sections: ingest.DefaultSectionerOptions, upload: UploadLimitsFromEnv()}
	s.searcher = search.NewSearcher(search.NewPGFTS(st))

	for _, o := range opts {
		o(s)
	}
	s.setupR2()         // NX-10 R2: motor híbrido si hay proveedor de embeddings (r2.go)
	s.setupR3()         // NX-10 R3: reordenamiento si hay proveedor (r3.go)
	if s.r1Set != nil { // NX-10 2b: opciones de R1 (r1.go)
		s.ApplyR1(*s.r1Set)
	} else {
		applyR1FromEnv(s)
	}
	if s.limits.Convert.MaxBinaryInputBytes == 0 {
		s.limits.Convert.MaxBinaryInputBytes = s.upload.MaxFileBytes
	}
	return s
}

// Store devuelve el almacén (para pruebas y herramientas).
func (s *Service) Store() *store.Store { return s.st }

// Registry devuelve el registro de conversores.
func (s *Service) Registry() *ingest.Registry { return s.reg }

// ─── Autorización ────────────────────────────────────────────────────────────

func actorOf(id authz.Identity) store.Actor {
	return store.Actor{PrincipalID: id.PrincipalID, TokenID: id.TokenID, Label: id.Label, Verified: id.Verified,
		ClientIP: id.ClientIP, UserAgent: id.UserAgent, Claim: id.ClientClaim}
}

// authorize comprueba la capacidad en el espacio. Denegado: evento
// authz.denied (en nexo_kb_events y en nexo_authz_events) y error 404 si el
// espacio no es visible (R-4) o 403 si lo es. Permitido: devuelve el espacio
// (creando su fila de nexo_kb_spaces si el ámbito se creó por la API de roles).
func (s *Service) authorize(ctx context.Context, id authz.Identity, slug string, c authz.Capability, details map[string]any) (domain.Space, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !domain.ValidSlug(slug) {
		return domain.Space{}, domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "space %q not found", slug)
	}
	sc := authz.SpaceScope(slug)
	d, err := s.eng.Can(ctx, id, sc, c)
	if err != nil {
		return domain.Space{}, err
	}
	if !d.Allowed {
		s.recordDenied(ctx, id, slug, c, d, details)
		if d.Hidden {
			return domain.Space{}, domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "space %q not found", slug)
		}
		if d.Reason == authz.ReasonArchived {
			return domain.Space{}, domain.Errorf(domain.KindForbidden, domain.CodeArchived, "space %q is archived", slug)
		}
		return domain.Space{}, domain.Errorf(domain.KindForbidden, domain.CodeForbidden, "%s is required in space %q (role %q, %s)", c, slug, roleName(d.Role), d.Reason)
	}
	sp, err := s.loadSpace(ctx, slug, id.PrincipalID)
	if err != nil {
		return domain.Space{}, err
	}
	sp.Role = string(d.Role)
	sp.Capabilities = capsOf(authz.Capabilities(authz.ScopeSpace, d.Role, id.TokenScope))
	return sp, nil
}

func roleName(r authz.Role) string {
	if r == authz.RoleNone {
		return "ninguno"
	}
	return string(r)
}

func capsOf(cs []authz.Capability) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

func (s *Service) loadSpace(ctx context.Context, slug, actorID string) (domain.Space, error) {
	sp, found, err := store.GetSpace(ctx, s.db, slug, false)
	if err != nil {
		return domain.Space{}, err
	}
	if found {
		return sp, nil
	}
	if _, err := store.EnsureSpace(ctx, s.db, slug, slug, "", nil, actorID); err != nil {
		return domain.Space{}, err
	}
	sp, found, err = store.GetSpace(ctx, s.db, slug, false)
	if err != nil {
		return domain.Space{}, err
	}
	if !found {
		return domain.Space{}, domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "space %q not found", slug)
	}
	return sp, nil
}

// recordDenied deja la denegación en ambos registros (mejor esfuerzo).
func (s *Service) recordDenied(ctx context.Context, id authz.Identity, slug string, c authz.Capability, d authz.Decision, details map[string]any) {
	s.eng.RecordDenied(ctx, id, authz.SpaceScope(slug), c, d, details)
	meta := map[string]any{"capability": string(c), "reason": d.Reason}
	if d.Hidden {
		meta["hidden"] = true
	}
	if d.Role != authz.RoleNone {
		meta["role"] = string(d.Role)
	}
	for k, v := range details {
		meta[k] = v
	}
	spaceID := ""
	if sp, found, err := store.GetSpace(ctx, s.db, slug, false); err == nil && found {
		spaceID = sp.ID
	}
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), denyRecordTimeout)
	defer cancel()
	_ = store.InsertEvent(recCtx, s.db, store.EventWrite{SpaceID: spaceID, SpaceSlug: slug, Action: domain.ActionAuthzDenied,
		TargetKind: "space", TargetID: slug, Actor: actorOf(id), Details: meta})
}

func (s *Service) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// mapAuthzError traduce errores del servicio de roles a errores de dominio.
func mapAuthzError(err error) error {
	var denied *authz.DeniedError
	var rule *authz.RuleError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &denied):
		if denied.Decision.Hidden {
			return domain.Errorf(domain.KindNotFound, domain.CodeNotFound, "space %s not found", denied.Scope.Key)
		}
		return domain.Errorf(domain.KindForbidden, domain.CodeForbidden, "%s is required (%s)", denied.Capability, denied.Decision.Reason)
	case errors.As(err, &rule):
		switch rule.Code {
		case authz.CodeInvalid, authz.CodeServiceAccountRole, authz.CodePrincipalDisabled:
			return domain.Errorf(domain.KindInvalid, rule.Code, "%s", rule.Message)
		case authz.CodeNotFound:
			return domain.Errorf(domain.KindNotFound, rule.Code, "%s", rule.Message)
		case authz.CodeSelfPromotion:
			return domain.Errorf(domain.KindForbidden, rule.Code, "%s", rule.Message)
		case authz.CodeScopeExists:
			return domain.Errorf(domain.KindConflict, domain.CodeSpaceExists, "%s", rule.Message)
		}
		return domain.Errorf(domain.KindConflict, rule.Code, "%s", rule.Message)
	}
	return err
}

func validateCategories(cats []string) ([]string, error) {
	out := make([]string, 0, len(cats))
	seen := map[string]bool{}
	for _, c := range cats {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		if !domain.ValidCategory(c) {
			return nil, domain.Errorf(domain.KindInvalid, domain.CodeCategory, "invalid category %q (lowercase letters, digits, _ and -)", c)
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, domain.Errorf(domain.KindInvalid, domain.CodeCategory, "at least one category is required")
	}
	return out, nil
}

func wrapf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(args, err)...)
}
