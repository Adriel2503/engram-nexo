// Package httpapi expone la API /nexo/kb/v1 de los espacios de conocimiento
// (NX-10, design §7). No conoce cloudserver: recibe un middleware que
// autentica (401) y deja la identidad en el contexto (authz.WithIdentity).
//
//	GET    /nexo/kb/v1/spaces                                        identidad (visibles con su rol)
//	POST   /nexo/kb/v1/spaces                                        platform.scope.create + assign_owner
//	GET    /nexo/kb/v1/spaces/{slug}                                 space.read
//	PATCH  /nexo/kb/v1/spaces/{slug}                                 space.configure
//	POST   /nexo/kb/v1/spaces/{slug}:archive | :restore              platform.scope.archive
//	GET    /nexo/kb/v1/spaces/{slug}/events                          space.audit.read o platform.audit.read
//	POST   /nexo/kb/v1/spaces/{slug}/documents:batch                 space.publish
//	GET    /nexo/kb/v1/spaces/{slug}/documents                       space.read
//	GET    /nexo/kb/v1/spaces/{slug}/documents/{doc_key}             space.read  (?version=)
//	GET    /nexo/kb/v1/spaces/{slug}/documents/{doc_key}/versions    space.read
//	GET    /nexo/kb/v1/spaces/{slug}/documents/{doc_key}/sections/{anchor}  space.read (?version=)
//	POST   /nexo/kb/v1/spaces/{slug}/documents/{doc_key}:retire      space.publish
//	POST   /nexo/kb/v1/spaces/{slug}/documents/{doc_key}:supersede   space.publish
//	GET    /nexo/kb/v1/search?q=&spaces=&category=&tag=&at=&known_at=&limit=   space.read (espacios visibles)
//	GET    /nexo/kb/v1/sections?space=&doc=&anchor=&version=         space.read
//
// doc_key puede contener "/": en la ruta va escapado (%2F). Errores
// {error_class, error_code, error}: 401 sin token válido, 403 sin capacidad
// (con evento authz.denied), 404 espacio no visible o inexistente, 409
// conflicto, 413 demasiado grande, 422 validación, 400 JSON o parámetros mal
// formados. Paginación por cursor opaco (máximo 200).
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/store"
)

// Prefix es la raíz versionada de la API (RNF-K8).
const Prefix = "/nexo/kb/v1"

// EnvMaxRequestBytes limita el cuerpo de una petición (por defecto 32 MB).
const EnvMaxRequestBytes = "NEXO_KB_MAX_REQUEST_BYTES"

// Middleware envuelve cada ruta: autentica y pone la identidad en el contexto.
type Middleware func(http.HandlerFunc) http.HandlerFunc

// Resolver devuelve el servicio (o un error si el almacén no está disponible).
type Resolver func(r *http.Request) (*service.Service, error)

type api struct {
	svc      Resolver
	maxBytes int64
}

// Register registra las rutas en el mux.
func Register(mux *http.ServeMux, svc Resolver, wrap Middleware) {
	a := &api{svc: svc, maxBytes: 32 << 20}
	if n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(EnvMaxRequestBytes)), 10, 64); err == nil && n > 0 {
		a.maxBytes = n
	}
	h := func(pattern string, fn handler) { mux.HandleFunc(pattern, wrap(a.route(fn))) }
	h("GET "+Prefix+"/spaces", a.listSpaces)
	h("POST "+Prefix+"/spaces", a.createSpace)
	h("GET "+Prefix+"/spaces/{slug}", a.getSpace)
	h("PATCH "+Prefix+"/spaces/{slug}", a.updateSpace)
	h("POST "+Prefix+"/spaces/{slugAction}", a.spaceAction)
	h("GET "+Prefix+"/spaces/{slug}/events", a.events)
	h("POST "+Prefix+"/spaces/{slug}/documents:batch", a.publishBatch)
	h("GET "+Prefix+"/spaces/{slug}/documents", a.listDocuments)
	h("GET "+Prefix+"/spaces/{slug}/documents/{docKey}", a.getDocument)
	h("GET "+Prefix+"/spaces/{slug}/documents/{docKey}/versions", a.listVersions)
	h("GET "+Prefix+"/spaces/{slug}/documents/{docKey}/sections/{anchor}", a.getSection)
	h("POST "+Prefix+"/spaces/{slug}/documents/{docKeyAction}", a.documentAction)
	h("GET "+Prefix+"/search", a.search)
	h("GET "+Prefix+"/sections", a.sectionByQuery)
	a.register2b(h) // entrega 2b (routes2b.go)
}

type handler func(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity)

// route: servicio disponible (500 si no), identidad administrada (403, R-15).
func (a *api) route(fn handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, err := a.svc(r)
		if err != nil || svc == nil {
			writeError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "nexo kb store is not configured")
			return
		}
		id, ok := authz.IdentityFrom(r.Context())
		if !ok || !id.Managed() {
			writeError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, constants.ReasonPolicyForbidden,
				"forbidden: knowledge spaces require a managed user token (shared token and insecure mode have no roles)")
			return
		}
		r = r.WithContext(authz.WithRequestCache(r.Context()))
		fn(w, r, svc, id)
	}
}

// ─── Respuestas ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, class, code, message string) {
	writeJSON(w, status, map[string]string{"error_class": class, "error_code": code, "error": message})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, constants.UpgradeErrorClassRepairable, domain.CodeInvalid, msg)
}

// writeErr traduce errores de dominio a HTTP.
func writeErr(w http.ResponseWriter, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		switch de.Kind {
		case domain.KindInvalid:
			writeError(w, http.StatusUnprocessableEntity, constants.UpgradeErrorClassRepairable, de.Code, de.Message)
		case domain.KindNotFound:
			writeError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, de.Code, de.Message)
		case domain.KindForbidden:
			writeError(w, http.StatusForbidden, constants.UpgradeErrorClassPolicy, de.Code, "forbidden: "+de.Message)
		case domain.KindConflict:
			writeError(w, http.StatusConflict, constants.UpgradeErrorClassPolicy, de.Code, de.Message)
		case domain.KindTooLarge:
			writeError(w, http.StatusRequestEntityTooLarge, constants.UpgradeErrorClassRepairable, de.Code, de.Message)
		default:
			writeError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, de.Message)
		}
		return
	}
	log.Printf("[nexo] kb: error interno: %v", err)
	writeError(w, http.StatusInternalServerError, constants.UpgradeErrorClassBlocked, constants.UpgradeErrorCodeInternal, "internal error")
}

// Page es la forma de los listados paginados (igual que NX-03).
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
	Limit      int     `json:"limit"`
}

func newPage[T any](items []T, next string, limit int) Page[T] {
	if items == nil {
		items = []T{}
	}
	p := Page[T]{Items: items, Limit: limit}
	if next != "" {
		p.NextCursor = &next
	}
	return p
}

// ─── Parámetros ──────────────────────────────────────────────────────────────

const (
	defaultLimit = 50
	maxLimit     = 200
)

func limitParam(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if n > maxLimit {
		n = maxLimit
	}
	return n, nil
}

// El cursor es opaco: base64 de un JSON con la última clave vista.
type cursor struct {
	Key string `json:"k,omitempty"`
	ID  int64  `json:"i,omitempty"`
	V   int    `json:"v,omitempty"`
}

func encodeCursor(c cursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	if strings.TrimSpace(s) == "" {
		return c, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return c, fmt.Errorf("invalid cursor")
	}
	return c, nil
}

func timeParam(r *http.Request, name string) (*time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	t, err := ingest.ParseDate(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return t, nil
}

func versionParam(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("version"))
	if raw == "" || raw == "current" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("version must be a positive integer or current")
	}
	return n, nil
}

func (a *api) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, a.maxBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, constants.UpgradeErrorClassRepairable, domain.CodeTooLarge,
				fmt.Sprintf("request body exceeds %d bytes; send smaller batches", a.maxBytes))
			return false
		}
		badRequest(w, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func splitAction(v string) (string, string) {
	i := strings.LastIndex(v, ":")
	if i < 0 {
		return v, ""
	}
	return v[:i], v[i+1:]
}

// ─── Espacios ────────────────────────────────────────────────────────────────

func (a *api) listSpaces(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	spaces, err := svc.ListSpaces(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": spaces})
}

func (a *api) createSpace(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	var in service.CreateSpaceInput
	if !a.decode(w, r, &in) {
		return
	}
	sp, err := svc.CreateSpace(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sp)
}

func (a *api) getSpace(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	sp, err := svc.GetSpace(r.Context(), id, r.PathValue("slug"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

func (a *api) updateSpace(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	var in service.UpdateSpaceInput
	if !a.decode(w, r, &in) {
		return
	}
	sp, err := svc.UpdateSpace(r.Context(), id, r.PathValue("slug"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

func (a *api) spaceAction(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	slug, action := splitAction(r.PathValue("slugAction"))
	var archived bool
	switch action {
	case "archive":
		archived = true
	case "restore":
	default:
		writeError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, domain.CodeNotFound, "unknown space action: use {slug}:archive or {slug}:restore")
		return
	}
	sp, err := svc.SetArchived(r.Context(), id, slug, archived)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

func (a *api) events(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	limit, err := limitParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	c, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	evs, err := svc.ListEvents(r.Context(), id, r.PathValue("slug"), c.ID, r.URL.Query().Get("action"), limit+1)
	if err != nil {
		writeErr(w, err)
		return
	}
	next := ""
	if len(evs) > limit {
		evs = evs[:limit]
		next = encodeCursor(cursor{ID: evs[len(evs)-1].ID})
	}
	writeJSON(w, http.StatusOK, newPage(evs, next, limit))
}

// ─── Documentos ──────────────────────────────────────────────────────────────

func (a *api) publishBatch(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	var req service.BatchRequest
	if !a.decode(w, r, &req) {
		return
	}
	if v := strings.TrimSpace(r.URL.Query().Get("dry_run")); v == "1" || v == "true" {
		req.DryRun = true
	}
	res, err := svc.PublishBatch(r.Context(), id, r.PathValue("slug"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *api) listDocuments(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	limit, err := limitParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	c, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	q := r.URL.Query()
	docs, err := svc.ListDocuments(r.Context(), id, r.PathValue("slug"), store.DocQuery{Status: q.Get("status"), Category: q.Get("category"), After: c.Key, Limit: limit + 1})
	if err != nil {
		writeErr(w, err)
		return
	}
	next := ""
	if len(docs) > limit {
		docs = docs[:limit]
		next = encodeCursor(cursor{Key: docs[len(docs)-1].DocKey})
	}
	writeJSON(w, http.StatusOK, newPage(docs, next, limit))
}

func (a *api) getDocument(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	v, err := versionParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	view, err := svc.GetDocument(r.Context(), id, r.PathValue("slug"), r.PathValue("docKey"), v)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *api) listVersions(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	limit, err := limitParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	c, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	vs, err := svc.ListVersions(r.Context(), id, r.PathValue("slug"), r.PathValue("docKey"), c.V, limit+1)
	if err != nil {
		writeErr(w, err)
		return
	}
	next := ""
	if len(vs) > limit {
		vs = vs[:limit]
		next = encodeCursor(cursor{V: vs[len(vs)-1].Version})
	}
	writeJSON(w, http.StatusOK, newPage(vs, next, limit))
}

func (a *api) getSection(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	v, err := versionParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	view, err := svc.GetSection(r.Context(), id, r.PathValue("slug"), r.PathValue("docKey"), r.PathValue("anchor"), v)
	if err != nil {
		writeErr(w, err)
		return
	}
	if base := BaseURL(r); base != "" && view.Link != "" { // RC-08 (tanda 2)
		view.Link = base + view.Link
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *api) sectionByQuery(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	q := r.URL.Query()
	space, doc, anchor := strings.TrimSpace(q.Get("space")), strings.TrimSpace(q.Get("doc")), strings.TrimSpace(q.Get("anchor"))
	if space == "" || doc == "" || anchor == "" {
		badRequest(w, "space, doc and anchor are required")
		return
	}
	v, err := versionParam(r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	view, err := svc.GetSection(r.Context(), id, space, doc, anchor, v)
	if err != nil {
		writeErr(w, err)
		return
	}
	if base := BaseURL(r); base != "" && view.Link != "" { // RC-08 (tanda 2)
		view.Link = base + view.Link
	}
	writeJSON(w, http.StatusOK, view)
}

type documentActionBody struct {
	By   string `json:"by"`
	Note string `json:"note"`
}

func (a *api) documentAction(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	key, action := splitAction(r.PathValue("docKeyAction"))
	var body documentActionBody
	if r.ContentLength != 0 && !a.decode(w, r, &body) {
		return
	}
	var doc domain.Document
	var err error
	switch action {
	case "retire":
		doc, err = svc.RetireDocument(r.Context(), id, r.PathValue("slug"), key, strings.TrimSpace(body.Note))
	case "supersede":
		doc, err = svc.SupersedeDocument(r.Context(), id, r.PathValue("slug"), key, body.By, strings.TrimSpace(body.Note))
	default:
		writeError(w, http.StatusNotFound, constants.UpgradeErrorClassRepairable, domain.CodeNotFound, "unknown document action: use {doc_key}:retire or {doc_key}:supersede")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// ─── Búsqueda ────────────────────────────────────────────────────────────────

func (a *api) search(w http.ResponseWriter, r *http.Request, svc *service.Service, id authz.Identity) {
	q := r.URL.Query()
	at, err := timeParam(r, "at")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	known, err := timeParam(r, "known_at")
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	limit := 0
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit <= 0 {
			badRequest(w, "limit must be a positive integer (max 50)")
			return
		}
	}
	var spaces []string
	for _, s := range q["spaces"] {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				spaces = append(spaces, part)
			}
		}
	}
	res, err := svc.Search(r.Context(), id, service.SearchRequest{Q: q.Get("q"), Spaces: spaces, Category: q.Get("category"), Tag: q.Get("tag"),
		At: at, KnownAt: known, Limit: limit, Mode: q.Get("mode"), Neighbors: isTrue(q.Get("neighbors"))})
	if err != nil {
		writeErr(w, err)
		return
	}
	if base := BaseURL(r); base != "" { // RC-08: enlace absoluto
		for i := range res.Hits {
			if res.Hits[i].Link != "" {
				res.Hits[i].Link = base + res.Hits[i].Link
			}
		}
	}
	writeJSON(w, http.StatusOK, res)
}
