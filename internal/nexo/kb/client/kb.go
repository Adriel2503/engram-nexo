package client

// KB: consulta del conocimiento para el asistente y la CLI (D-01, D-02).
// Primero el servidor; si no responde, la copia local, marcando la respuesta
// como copia local con su fecha y antigüedad (RF-42, HU-28). Cuando el
// servidor vuelve a responder, la copia se actualiza sola en segundo plano.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/domain"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/search"
)

// Variables de entorno del cliente.
const (
	// EnvTimeout: segundos de espera del servidor antes de responder desde la
	// copia local (por defecto 8).
	EnvTimeout = "NEXO_KB_TIMEOUT_SECONDS"
	// EnvSyncMinutes: cada cuántos minutos se actualiza la copia mientras el
	// MCP corre (por defecto 10, mínimo 1).
	EnvSyncMinutes = "NEXO_KB_SYNC_MINUTES"
)

// Fuentes de una respuesta.
const (
	SourceServer = "server"
	SourceLocal  = "local_copy"
)

// Estados para el asistente (HU-07, HU-08).
const (
	StatusOfficial = "Oficial"
	StatusRetired  = "Retirado"
)

// NoStandardMessage es el aviso literal de HU-06.
const NoStandardMessage = "No existe un estándar oficial publicado sobre este tema."

// ErrOffline: la operación necesita el servidor y no respondió.
var ErrOffline = errors.New("sin conexión con el servidor de Nexo")

// ErrNotConfigured: falta el servidor o el token.
var ErrNotConfigured = errors.New("Nexo no está configurado en esta PC: falta el servidor (ENGRAM_CLOUD_SERVER) o el token (ENGRAM_CLOUD_TOKEN)")

// KB reúne el cliente HTTP, la copia local y su sincronización.
type KB struct {
	API       *API // nil si no hay servidor y token
	Local     *Local
	Syncer    *Syncer
	SyncEvery time.Duration
	now       func() time.Time

	mu        sync.Mutex
	syncing   bool
	lastTry   time.Time
	lastSync  *SyncReport
	lastError string
	offline   bool
}

// OpenKB abre la copia local por defecto y el cliente con la configuración
// de Engram (cloud.json o variables). Sin servidor o token, solo la copia.
func OpenKB(dataDir string) (*KB, error) {
	st, err := LoadSettings(dataDir, "")
	if err != nil {
		return nil, err
	}
	local, err := OpenLocal(DefaultLocalPath())
	if err != nil {
		return nil, err
	}
	var api *API
	if st.Server != "" && st.Token != "" {
		api = NewAPI(st, time.Duration(envInt(EnvTimeout, 8))*time.Second)
		api.UserAgent = UserAgent + " (mcp)"
	}
	return NewKB(api, local), nil
}

// NewKB arma la KB con piezas ya creadas.
func NewKB(api *API, local *Local) *KB {
	k := &KB{API: api, Local: local, SyncEvery: time.Duration(max(1, envInt(EnvSyncMinutes, 10))) * time.Minute, now: time.Now}
	if api != nil {
		k.Syncer = NewSyncer(api, local)
	}
	return k
}

// Close cierra la copia.
func (k *KB) Close() error { return k.Local.Close() }

// ─── Sincronización ──────────────────────────────────────────────────────────

// SyncNow sincroniza y espera el resultado.
func (k *KB) SyncNow(ctx context.Context) (SyncReport, error) {
	if k.API == nil {
		return SyncReport{Status: k.Local.Status(ctx)}, ErrNotConfigured
	}
	k.mu.Lock()
	k.lastTry = k.now()
	k.mu.Unlock()
	rep, err := k.Syncer.Sync(ctx)
	k.mu.Lock()
	defer k.mu.Unlock()
	if err != nil {
		k.lastError = err.Error()
		k.offline = Unreachable(err)
		return rep, err
	}
	k.lastSync, k.lastError, k.offline = &rep, "", false
	return rep, nil
}

// syncAsync dispara una sincronización en segundo plano si no hay otra en
// curso y la copia está vieja (o se pide forzar). Nunca bloquea.
func (k *KB) syncAsync(force bool) {
	if k.API == nil {
		return
	}
	k.mu.Lock()
	if k.syncing || (!force && k.now().Sub(k.lastTry) < k.SyncEvery) {
		k.mu.Unlock()
		return
	}
	k.syncing = true
	k.mu.Unlock()
	go func() {
		defer func() {
			k.mu.Lock()
			k.syncing = false
			k.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if _, err := k.SyncNow(ctx); err != nil {
			log.Printf("[nexo] kb: no se pudo actualizar la copia local: %v", err)
		}
	}()
}

// Start sincroniza al iniciar y luego cada SyncEvery mientras ctx viva
// (design §6.4: al iniciar el MCP y como máximo cada 10 minutos).
func (k *KB) Start(ctx context.Context) {
	if k.API == nil {
		return
	}
	go func() {
		k.syncAsync(true)
		t := time.NewTicker(k.SyncEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				k.syncAsync(true)
			}
		}
	}()
}

// online marca que el servidor respondió: si venía sin conexión o la copia
// está vieja, se actualiza en segundo plano (CA-K10: al reconectar se
// actualiza sola).
func (k *KB) online() {
	k.mu.Lock()
	wasOffline := k.offline
	k.offline = false
	k.mu.Unlock()
	st := k.Local.Status(context.Background())
	stale := st.SyncedAt == nil || k.now().Sub(*st.SyncedAt) > k.SyncEvery
	if wasOffline || stale {
		k.syncAsync(wasOffline)
	}
}

func (k *KB) markOffline() {
	k.mu.Lock()
	k.offline = true
	k.mu.Unlock()
}

// CopyInfo describe la copia local en una respuesta.
type CopyInfo struct {
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	Age      string     `json:"age,omitempty"`
	Path     string     `json:"path"`
}

func (k *KB) copyInfo(ctx context.Context) (*CopyInfo, Status) {
	st := k.Local.Status(ctx)
	return &CopyInfo{SyncedAt: st.SyncedAt, Age: st.Age, Path: st.Path}, st
}

func localNotice(st Status, why string) string {
	if st.SyncedAt == nil {
		return why + " La copia local está vacía (nunca se sincronizó): no hay datos para responder sin conexión."
	}
	return fmt.Sprintf("%s Respuesta desde la copia local del %s (%s): no se pudo verificar con el servidor.", why,
		st.SyncedAt.Local().Format("2006-01-02 15:04"), st.Age)
}

// shouldFallback: sin servidor configurado o sin respuesta del servidor.
func (k *KB) shouldFallback(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotConfigured) || Unreachable(err) {
		k.markOffline()
		return true
	}
	return false
}

// ─── Búsqueda ────────────────────────────────────────────────────────────────

// SearchParams es una búsqueda (RF-20, RC-26).
type SearchParams struct {
	Query     string
	Spaces    []string
	Category  string
	Tag       string
	At        *time.Time
	KnownAt   *time.Time
	Mode      string
	Limit     int
	LocalOnly bool
	// LocalReason reemplaza, con LocalOnly, el motivo del aviso de la copia
	// local (por ejemplo, la CLI cuando el servidor no respondió).
	LocalReason string
}

// Hit es un resultado citable (RN-03, RF-21).
type Hit struct {
	Status       string     `json:"status"`
	Citation     string     `json:"citation"`
	Link         string     `json:"link,omitempty"`
	SourceURL    string     `json:"source_url,omitempty"`
	Space        string     `json:"space"`
	DocKey       string     `json:"doc_key"`
	Title        string     `json:"title"`
	HeadingPath  []string   `json:"heading_path,omitempty"`
	Anchor       string     `json:"anchor"`
	Version      int        `json:"version"`
	Category     string     `json:"category,omitempty"`
	Owner        string     `json:"owner,omitempty"`
	ValidFrom    time.Time  `json:"valid_from"`
	ValidUntil   *time.Time `json:"valid_until,omitempty"`
	SupersededBy string     `json:"superseded_by,omitempty"`
	Quote        string     `json:"quote,omitempty"`
	Match        string     `json:"match,omitempty"`
}

// SearchResult es la respuesta de nexo_search.
type SearchResult struct {
	Source    string            `json:"source"`
	Notice    string            `json:"notice,omitempty"`
	Copy      *CopyInfo         `json:"copy,omitempty"`
	At        *time.Time        `json:"at,omitempty"`
	KnownAt   *time.Time        `json:"known_at,omitempty"`
	NoResults bool              `json:"no_results"`
	Message   string            `json:"message,omitempty"`
	Results   []Hit             `json:"results"`
	Relevance *search.Relevance `json:"relevance,omitempty"`
	Guidance  string            `json:"guidance"`
	// NX-10 R2: modo usado por el servidor (fts, exact o hybrid) y estado de
	// la búsqueda por significado.
	Mode   string             `json:"mode,omitempty"`
	Vector *search.VectorInfo `json:"vector,omitempty"`
}

const searchGuidance = "Cita cada resultado usado con su 'citation' y su 'link' (y 'source_url' si existe) e indica su 'status'. " +
	"Si no_results es true, responde literalmente: \"No existe un estándar oficial publicado sobre este tema.\" y marca cualquier recomendación propia como tal. " +
	"Si source es local_copy, avisa que la respuesta viene de la copia local (con su fecha) y que no pudo verificarse con el servidor." +
	" Si hay un notice del servidor (por ejemplo, respuesta solo por texto), tenlo en cuenta: una pregunta formulada con otras palabras puede no encontrarse."

// serverHit es un resultado de GET /search.
type serverHit struct {
	Space          string                `json:"space"`
	DocKey         string                `json:"doc_key"`
	Title          string                `json:"title"`
	Category       string                `json:"category"`
	Owner          string                `json:"owner"`
	Version        int                   `json:"version"`
	Anchor         string                `json:"anchor"`
	HeadingPath    []string              `json:"heading_path"`
	Excerpt        string                `json:"excerpt"`
	SourceLocation domain.SourceLocation `json:"source_location"`
	ValidFrom      time.Time             `json:"valid_from"`
	ValidUntil     *time.Time            `json:"valid_until"`
	DocStatus      string                `json:"document_status"`
	SupersededBy   string                `json:"superseded_by"`
	Match          string                `json:"match"`
	Citation       string                `json:"citation"`
	Link           string                `json:"link"`
	SourceURL      string                `json:"source_url"`
}

type serverSearch struct {
	Results   []serverHit       `json:"results"`
	NoResults bool              `json:"no_results"`
	Relevance *search.Relevance `json:"relevance"`
	// NX-10 R2: modo efectivo y aviso del servidor (respuesta solo por texto).
	Mode   string             `json:"mode"`
	Notice string             `json:"notice"`
	Vector *search.VectorInfo `json:"vector"`
}

// hitStatus describe el estado de un resultado hoy y, con at, en la fecha.
func hitStatus(docStatus, supersededBy string, validFrom time.Time, validUntil *time.Time, at *time.Time, now time.Time) string {
	today := StatusOfficial
	if docStatus == domain.DocRetired {
		today = StatusRetired
		if supersededBy != "" {
			today = "Reemplazado por " + supersededBy
		}
	} else if validFrom.After(now) || (validUntil != nil && !validUntil.After(now)) {
		today = "No rige hoy"
	}
	if at == nil || today == StatusOfficial {
		return today
	}
	if today == "No rige hoy" {
		return "Vigente en la fecha consultada; hoy no rige"
	}
	return "Vigente en la fecha consultada; hoy no rige (" + today + ")"
}

func quoteOf(loc domain.SourceLocation, body, excerpt string) string {
	if q, ok := loc["quote"].(string); ok && q != "" {
		return q
	}
	if body != "" {
		return search.Quote(body)
	}
	return strings.Join(strings.Fields(excerpt), " ")
}

// Search busca en el servidor o, si no responde, en la copia local.
func (k *KB) Search(ctx context.Context, p SearchParams) (SearchResult, error) {
	if strings.TrimSpace(p.Query) == "" {
		return SearchResult{}, errors.New("falta la consulta")
	}
	if p.Limit <= 0 {
		p.Limit = 5
	}
	if !p.LocalOnly {
		res, err := k.searchServer(ctx, p)
		if err == nil {
			k.online()
			return res, nil
		}
		if !k.shouldFallback(err) {
			return SearchResult{}, err
		}
		why := "El servidor de Nexo no respondió."
		if errors.Is(err, ErrNotConfigured) {
			why = "Nexo no tiene servidor configurado en esta PC."
		}
		return k.searchLocal(ctx, p, why)
	}
	why := "Consulta solo a la copia local."
	if p.LocalReason != "" {
		why = p.LocalReason
	}
	return k.searchLocal(ctx, p, why)
}

func (k *KB) searchServer(ctx context.Context, p SearchParams) (SearchResult, error) {
	if k.API == nil {
		return SearchResult{}, ErrNotConfigured
	}
	v := url.Values{}
	v.Set("q", p.Query)
	if len(p.Spaces) > 0 {
		v.Set("spaces", strings.Join(p.Spaces, ","))
	}
	for name, val := range map[string]string{"category": p.Category, "tag": p.Tag, "mode": p.Mode} {
		if val != "" {
			v.Set(name, val)
		}
	}
	if p.At != nil {
		v.Set("at", p.At.UTC().Format(time.RFC3339Nano))
	}
	if p.KnownAt != nil {
		v.Set("known_at", p.KnownAt.UTC().Format(time.RFC3339Nano))
	}
	v.Set("limit", strconv.Itoa(p.Limit))
	var sr serverSearch
	if err := k.API.Do(ctx, "GET", "/nexo/kb/v1/search?"+v.Encode(), nil, &sr); err != nil {
		return SearchResult{}, err
	}
	now := k.now()
	res := SearchResult{Source: SourceServer, At: p.At, KnownAt: p.KnownAt, NoResults: sr.NoResults, Relevance: sr.Relevance, Results: []Hit{}, Guidance: searchGuidance,
		Mode: sr.Mode, Notice: sr.Notice, Vector: sr.Vector}
	for _, h := range sr.Results {
		res.Results = append(res.Results, Hit{Status: hitStatus(h.DocStatus, h.SupersededBy, h.ValidFrom, h.ValidUntil, p.At, now), Citation: h.Citation,
			Link: h.Link, SourceURL: h.SourceURL, Space: h.Space, DocKey: h.DocKey, Title: h.Title, HeadingPath: h.HeadingPath, Anchor: h.Anchor,
			Version: h.Version, Category: h.Category, Owner: h.Owner, ValidFrom: h.ValidFrom, ValidUntil: h.ValidUntil, SupersededBy: h.SupersededBy,
			Quote: quoteOf(h.SourceLocation, "", h.Excerpt), Match: h.Match})
	}
	if res.NoResults || len(res.Results) == 0 {
		res.NoResults, res.Message = true, NoStandardMessage
	}
	return res, nil
}

// linkBase es la URL pública para los enlaces de la copia local.
func (k *KB) linkBase(ctx context.Context) string {
	if u := strings.TrimRight(k.Local.PublicURL(ctx), "/"); u != "" {
		return u
	}
	if k.API != nil {
		return k.API.Server
	}
	return strings.TrimRight(k.Local.meta(ctx, "server"), "/")
}

func (k *KB) localHit(base string, h LocalHit, at *time.Time) Hit {
	sh := search.Hit{Space: h.Space, Title: h.Title, DocKey: h.DocKey, HeadingPath: h.HeadingPath, Anchor: h.Anchor, Version: h.Version,
		ValidFrom: h.ValidFrom, ValidUntil: h.ValidUntil, SourceLocation: h.SourceLocation}
	return Hit{Status: hitStatus(h.Status, h.SupersededBy, h.ValidFrom, h.ValidUntil, at, k.now()), Citation: search.Citation(sh) + " [copia local]",
		Link: base + search.SectionPath(h.Space, h.DocKey, h.Anchor, h.Version), SourceURL: h.SourceURL, Space: h.Space, DocKey: h.DocKey, Title: h.Title,
		HeadingPath: h.HeadingPath, Anchor: h.Anchor, Version: h.Version, Category: h.Category, Owner: h.Owner, ValidFrom: h.ValidFrom,
		ValidUntil: h.ValidUntil, SupersededBy: h.SupersededBy, Quote: search.Quote(h.Body), Match: h.Match}
}

func (k *KB) searchLocal(ctx context.Context, p SearchParams, why string) (SearchResult, error) {
	info, st := k.copyInfo(ctx)
	res := SearchResult{Source: SourceLocal, Copy: info, At: p.At, KnownAt: p.KnownAt, Results: []Hit{}, Guidance: searchGuidance, Notice: localNotice(st, why)}
	if st.Empty {
		// Sin datos locales no se puede afirmar que no exista un estándar.
		res.Message = "No se pudo consultar el conocimiento: el servidor no respondió y no hay copia local. No afirmes que no existe un estándar; avisa que la consulta falló."
		return res, nil
	}
	if p.KnownAt != nil {
		res.Message = "La copia local no guarda el historial de publicación: la consulta con known_at necesita el servidor. Avisa que no se pudo responder sin conexión."
		return res, nil
	}
	lr, err := k.Local.Search(ctx, LocalQuery{Text: p.Query, Spaces: p.Spaces, Category: p.Category, Tag: p.Tag, At: p.At, Limit: p.Limit, Mode: p.Mode})
	if err != nil {
		return SearchResult{}, err
	}
	base := k.linkBase(ctx)
	for _, h := range lr.Hits {
		res.Results = append(res.Results, k.localHit(base, h, p.At))
	}
	res.Relevance = lr.Relevance
	if len(res.Results) == 0 {
		res.NoResults, res.Message = true, NoStandardMessage+" (según la copia local)"
	}
	return res, nil
}

// ─── Lectura ─────────────────────────────────────────────────────────────────

// GetParams pide un documento o una sección (RF-22).
type GetParams struct {
	Space     string
	DocKey    string
	Anchor    string
	Version   int
	LocalOnly bool
}

// GetSection es una sección de un documento leído.
type GetSection struct {
	Anchor      string   `json:"anchor"`
	HeadingPath []string `json:"heading_path,omitempty"`
	Citation    string   `json:"citation,omitempty"`
	Link        string   `json:"link,omitempty"`
}

// GetResult es la respuesta de nexo_get.
type GetResult struct {
	Source       string       `json:"source"`
	Notice       string       `json:"notice,omitempty"`
	Copy         *CopyInfo    `json:"copy,omitempty"`
	Space        string       `json:"space"`
	DocKey       string       `json:"doc_key"`
	Title        string       `json:"title"`
	Category     string       `json:"category,omitempty"`
	Owner        string       `json:"owner,omitempty"`
	Status       string       `json:"status"`
	Version      int          `json:"version"`
	ValidFrom    *time.Time   `json:"valid_from,omitempty"`
	ValidUntil   *time.Time   `json:"valid_until,omitempty"`
	SupersededBy string       `json:"superseded_by,omitempty"`
	Anchor       string       `json:"anchor,omitempty"`
	Citation     string       `json:"citation"`
	Link         string       `json:"link,omitempty"`
	SourceURL    string       `json:"source_url,omitempty"`
	Content      string       `json:"content"`
	Sections     []GetSection `json:"sections,omitempty"`
	DocNotice    string       `json:"document_notice,omitempty"`
}

// DocPath es la ruta del panel de un documento en una versión (sin ancla).
func DocPath(space, docKey string, version int) string {
	v := url.Values{}
	v.Set("doc", docKey)
	if version > 0 {
		v.Set("v", strconv.Itoa(version))
	}
	return "/dashboard/kb/spaces/" + url.PathEscape(space) + "/doc?" + v.Encode()
}

func docCitation(space, title, docKey string, version int, from time.Time, until *time.Time) string {
	s := fmt.Sprintf("%s / %s (%s, v%d, vigente desde %s", space, title, docKey, version, from.Format("2006-01-02"))
	if until != nil {
		s += " hasta " + until.Format("2006-01-02")
	}
	return s + ")"
}

// Get lee del servidor o, si no responde, de la copia (solo lo vigente).
func (k *KB) Get(ctx context.Context, p GetParams) (GetResult, error) {
	p.Space, p.DocKey, p.Anchor = strings.TrimSpace(p.Space), strings.TrimSpace(p.DocKey), strings.TrimSpace(p.Anchor)
	if i := strings.Index(p.DocKey, "#"); i > 0 && p.Anchor == "" {
		p.DocKey, p.Anchor = p.DocKey[:i], p.DocKey[i+1:]
	}
	if p.Space == "" || p.DocKey == "" {
		return GetResult{}, errors.New("faltan space y doc_key")
	}
	if !p.LocalOnly {
		res, err := k.getServer(ctx, p)
		if err == nil {
			k.online()
			return res, nil
		}
		if !k.shouldFallback(err) {
			return GetResult{}, err
		}
		return k.getLocal(ctx, p, "El servidor de Nexo no respondió.")
	}
	return k.getLocal(ctx, p, "Consulta solo a la copia local.")
}

func (k *KB) getServer(ctx context.Context, p GetParams) (GetResult, error) {
	if k.API == nil {
		return GetResult{}, ErrNotConfigured
	}
	q := ""
	if p.Version > 0 {
		q = "?version=" + strconv.Itoa(p.Version)
	}
	docPath := "/nexo/kb/v1/spaces/" + url.PathEscape(p.Space) + "/documents/" + url.PathEscape(p.DocKey)
	now := k.now()
	if p.Anchor != "" {
		var sv struct {
			Space    string         `json:"space"`
			DocKey   string         `json:"doc_key"`
			Title    string         `json:"title"`
			Category string         `json:"category"`
			Owner    string         `json:"owner"`
			Version  int            `json:"version"`
			Section  domain.Section `json:"section"`
			Citation string         `json:"citation"`
			Status   string         `json:"document_status"`
			Notice   string         `json:"notice"`
			Link     string         `json:"link"`
			Source   string         `json:"source_url"`
		}
		if err := k.API.Do(ctx, "GET", docPath+"/sections/"+url.PathEscape(p.Anchor)+q, nil, &sv); err != nil {
			return GetResult{}, err
		}
		vf := sv.Section.ValidFrom
		return GetResult{Source: SourceServer, Space: sv.Space, DocKey: sv.DocKey, Title: sv.Title, Category: sv.Category, Owner: sv.Owner,
			Status: hitStatus(sv.Status, "", sv.Section.ValidFrom, sv.Section.ValidUntil, nil, now), Version: sv.Version, ValidFrom: &vf,
			ValidUntil: sv.Section.ValidUntil, Anchor: sv.Section.Anchor, Citation: sv.Citation, Link: sv.Link, SourceURL: sv.Source,
			Content: sv.Section.Body, DocNotice: sv.Notice}, nil
	}
	var dv struct {
		Document domain.Document `json:"document"`
		Version  domain.Version  `json:"version"`
		Sections []struct {
			Anchor      string   `json:"anchor"`
			HeadingPath []string `json:"heading_path"`
		} `json:"sections"`
		Notice string `json:"notice"`
	}
	if err := k.API.Do(ctx, "GET", docPath+q, nil, &dv); err != nil {
		return GetResult{}, err
	}
	base := k.API.Server
	if u := strings.TrimRight(k.Local.PublicURL(ctx), "/"); u != "" {
		base = u
	}
	vf := dv.Version.ValidFrom
	res := GetResult{Source: SourceServer, Space: dv.Document.Space, DocKey: dv.Document.DocKey, Title: dv.Version.Title, Category: dv.Version.Category,
		Owner: dv.Version.OwnerLabel, Status: hitStatus(dv.Document.Status, dv.Document.SupersededBy, dv.Version.ValidFrom, dv.Version.ValidUntil, nil, now),
		Version: dv.Version.Version, ValidFrom: &vf, ValidUntil: dv.Version.ValidUntil, SupersededBy: dv.Document.SupersededBy,
		Citation: docCitation(dv.Document.Space, dv.Version.Title, dv.Document.DocKey, dv.Version.Version, dv.Version.ValidFrom, dv.Version.ValidUntil),
		Link:     base + DocPath(dv.Document.Space, dv.Document.DocKey, dv.Version.Version), SourceURL: dv.Version.Origin.SourceURL,
		Content: dv.Version.ContentMD, DocNotice: dv.Notice}
	if dv.Version.Version != dv.Document.CurrentVersion && dv.Document.Status != domain.DocRetired {
		res.Status = "Versión histórica (la vigente es la " + strconv.Itoa(dv.Document.CurrentVersion) + ")"
	}
	for _, s := range dv.Sections {
		res.Sections = append(res.Sections, GetSection{Anchor: s.Anchor, HeadingPath: s.HeadingPath,
			Link: base + search.SectionPath(dv.Document.Space, dv.Document.DocKey, s.Anchor, dv.Version.Version)})
	}
	return res, nil
}

func (k *KB) getLocal(ctx context.Context, p GetParams, why string) (GetResult, error) {
	info, st := k.copyInfo(ctx)
	if st.Empty {
		return GetResult{}, fmt.Errorf("%s La copia local está vacía: no se puede leer sin conexión", why)
	}
	d, found, err := k.Local.Get(ctx, p.Space, p.DocKey, p.Anchor, nil)
	if err != nil {
		return GetResult{}, err
	}
	if !found {
		return GetResult{}, fmt.Errorf("%s La copia local no tiene %s/%s vigente hoy (o no es visible para ti)", why, p.Space, p.DocKey)
	}
	if p.Version > 0 && p.Version != d.Version {
		return GetResult{}, fmt.Errorf("%s Sin conexión solo está la versión vigente (v%d); la v%d necesita el servidor", why, d.Version, p.Version)
	}
	base := k.linkBase(ctx)
	first := d.Sections[0]
	res := GetResult{Source: SourceLocal, Copy: info, Notice: localNotice(st, why), Space: d.Space, DocKey: d.DocKey, Title: d.Title,
		Category: d.Category, Owner: d.Owner, Status: hitStatus(d.Status, d.SupersededBy, first.ValidFrom, first.ValidUntil, nil, k.now()),
		Version: d.Version, ValidFrom: &first.ValidFrom, ValidUntil: first.ValidUntil, SupersededBy: d.SupersededBy, SourceURL: first.SourceURL}
	if p.Anchor != "" {
		h := k.localHit(base, first, nil)
		res.Anchor, res.Citation, res.Link, res.Content = first.Anchor, h.Citation, h.Link, first.Body
		return res, nil
	}
	res.Citation = docCitation(d.Space, d.Title, d.DocKey, d.Version, first.ValidFrom, first.ValidUntil) + " [copia local]"
	res.Link = base + DocPath(d.Space, d.DocKey, d.Version)
	var b strings.Builder
	for i, s := range d.Sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s.Body)
		res.Sections = append(res.Sections, GetSection{Anchor: s.Anchor, HeadingPath: s.HeadingPath, Link: base + search.SectionPath(d.Space, d.DocKey, s.Anchor, d.Version)})
	}
	res.Content = b.String()
	return res, nil
}

// ─── Espacios, cambios, propuestas e identidad ───────────────────────────────

// SpacesResult es la respuesta de nexo_spaces.
type SpacesResult struct {
	Source string           `json:"source"`
	Notice string           `json:"notice,omitempty"`
	Copy   *CopyInfo        `json:"copy,omitempty"`
	Spaces []map[string]any `json:"spaces"`
}

// Spaces lista los espacios visibles con el rol efectivo (RF-05).
func (k *KB) Spaces(ctx context.Context) (SpacesResult, error) {
	var out struct {
		Items []domain.Space `json:"items"`
	}
	err := ErrNotConfigured
	if k.API != nil {
		err = k.API.Do(ctx, "GET", "/nexo/kb/v1/spaces", nil, &out)
	}
	if err == nil {
		k.online()
		res := SpacesResult{Source: SourceServer, Spaces: []map[string]any{}}
		for _, s := range out.Items {
			res.Spaces = append(res.Spaces, map[string]any{"slug": s.Slug, "name": s.Name, "description": s.Description, "role": s.Role,
				"visibility": s.Visibility, "status": s.Status, "capabilities": s.Capabilities})
		}
		return res, nil
	}
	if !k.shouldFallback(err) {
		return SpacesResult{}, err
	}
	info, st := k.copyInfo(ctx)
	sps, lerr := k.Local.Spaces(ctx)
	if lerr != nil {
		return SpacesResult{}, lerr
	}
	res := SpacesResult{Source: SourceLocal, Copy: info, Notice: localNotice(st, "El servidor de Nexo no respondió."), Spaces: []map[string]any{}}
	for _, s := range sps {
		res.Spaces = append(res.Spaces, map[string]any{"slug": s.Slug, "name": s.Name, "role": s.Role})
	}
	return res, nil
}

// serverOnly ejecuta una operación que necesita el servidor.
func (k *KB) serverOnly(ctx context.Context, method, path string, body, out any) error {
	if k.API == nil {
		return ErrNotConfigured
	}
	err := k.API.Do(ctx, method, path, body, out)
	if err == nil {
		k.online()
		return nil
	}
	if Unreachable(err) {
		k.markOffline()
		return fmt.Errorf("%w: la operación necesita el servidor y no se realizó (nada quedó guardado)", ErrOffline)
	}
	return err
}

// ChangesParams pide "qué cambió desde X" (RC-27, HU-38).
type ChangesParams struct {
	Since  time.Time
	Until  *time.Time
	Spaces []string
	Clock  string
	Limit  int
}

// Changes devuelve lo agregado, modificado y retirado entre dos fechas.
func (k *KB) Changes(ctx context.Context, p ChangesParams) (map[string]any, error) {
	v := url.Values{}
	v.Set("since", p.Since.UTC().Format(time.RFC3339Nano))
	if p.Until != nil {
		v.Set("until", p.Until.UTC().Format(time.RFC3339Nano))
	}
	if len(p.Spaces) > 0 {
		v.Set("spaces", strings.Join(p.Spaces, ","))
	}
	if p.Clock != "" {
		v.Set("clock", p.Clock)
	}
	if p.Limit > 0 {
		v.Set("limit", strconv.Itoa(p.Limit))
	}
	var out map[string]any
	if err := k.serverOnly(ctx, "GET", "/nexo/kb/v1/changes?"+v.Encode(), nil, &out); err != nil {
		return nil, err
	}
	out["source"] = SourceServer
	out["guidance"] = "Lista por espacio y documento las secciones agregadas (added), modificadas (modified) y retiradas (removed) con su fecha y su 'link'. Si no_changes es true, dilo."
	return out, nil
}

// ProposeParams es una propuesta (RF-30, KP-08).
type ProposeParams struct {
	Space       string     `json:"-"`
	DocKey      string     `json:"doc_key,omitempty"`
	BaseVersion int        `json:"base_version,omitempty"`
	Title       string     `json:"title"`
	Category    string     `json:"category,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Content     string     `json:"content"`
	Rationale   string     `json:"rationale,omitempty"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
	ValidUntil  *time.Time `json:"valid_until,omitempty"`
}

// Propose crea una propuesta (solo con servidor).
func (k *KB) Propose(ctx context.Context, p ProposeParams) (map[string]any, error) {
	if strings.TrimSpace(p.Space) == "" || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Content) == "" {
		return nil, errors.New("faltan space, title y content")
	}
	var out map[string]any
	if err := k.serverOnly(ctx, "POST", "/nexo/kb/v1/spaces/"+url.PathEscape(strings.TrimSpace(p.Space))+"/proposals", p, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Proposals lista propuestas: las propias y, para revisores, las del espacio.
func (k *KB) Proposals(ctx context.Context, space, status string, mine bool, limit int) (map[string]any, error) {
	v := url.Values{}
	if space != "" {
		v.Set("space", space)
	}
	if status != "" {
		v.Set("status", status)
	}
	if mine {
		v.Set("mine", "true")
	}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	var out map[string]any
	if err := k.serverOnly(ctx, "GET", "/nexo/kb/v1/proposals?"+v.Encode(), nil, &out); err != nil {
		return nil, err
	}
	// El contenido completo se pide aparte; aquí basta un resumen.
	if items, ok := out["items"].([]any); ok {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				if c, ok := m["content_md"].(string); ok && len(c) > 600 {
					m["content_md"] = c[:600] + "..."
				}
			}
		}
	}
	return out, nil
}

// ReviewParams es la revisión de una propuesta (RF-32).
type ReviewParams struct {
	ProposalID string
	Decision   string // approve | discard
	Note       string
	Force      bool
	ValidFrom  *time.Time
	ValidUntil *time.Time
}

// Review aprueba o descarta una propuesta.
func (k *KB) Review(ctx context.Context, p ReviewParams) (map[string]any, error) {
	id := strings.TrimSpace(p.ProposalID)
	if id == "" || strings.ContainsAny(id, "/:?#") {
		return nil, errors.New("proposal_id inválido")
	}
	body := map[string]any{"note": p.Note}
	var action string
	switch strings.ToLower(strings.TrimSpace(p.Decision)) {
	case "approve", "aprobar":
		action = "approve"
		body["force"] = p.Force
		if p.ValidFrom != nil {
			body["valid_from"] = p.ValidFrom.UTC()
		}
		if p.ValidUntil != nil {
			body["valid_until"] = p.ValidUntil.UTC()
		}
	case "discard", "descartar":
		action = "discard"
		if strings.TrimSpace(p.Note) == "" {
			return nil, errors.New("para descartar indica el motivo en note")
		}
		body["reason"] = p.Note
	default:
		return nil, errors.New("decision debe ser approve o discard")
	}
	var out map[string]any
	if err := k.serverOnly(ctx, "POST", "/nexo/kb/v1/proposals/"+url.PathEscape(id)+":"+action, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Whoami informa identidad, servidor y estado de la copia local.
func (k *KB) Whoami(ctx context.Context) map[string]any {
	out := map[string]any{"local_copy": k.Local.Status(ctx)}
	if k.API == nil {
		out["server"] = map[string]any{"configured": false, "reachable": false, "message": ErrNotConfigured.Error()}
		return out
	}
	srv := map[string]any{"configured": true, "url": k.API.Server}
	var me map[string]any
	err := k.API.Do(ctx, "GET", "/nexo/authz/v1/me", nil, &me)
	switch {
	case err == nil:
		k.online()
		srv["reachable"] = true
		out["identity"] = me
	case Unreachable(err):
		k.markOffline()
		srv["reachable"] = false
		srv["message"] = "el servidor no respondió: las consultas usan la copia local"
	default:
		srv["reachable"] = true
		srv["message"] = FriendlyError(err)
	}
	out["server"] = srv
	k.mu.Lock()
	if k.lastError != "" {
		out["last_sync_error"] = k.lastError
	}
	k.mu.Unlock()
	return out
}

// FriendlyError traduce un error a un mensaje comprensible (HU-27).
func FriendlyError(err error) string {
	if err == nil {
		return ""
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusUnauthorized:
			return "El token de Nexo no es válido o venció: pide uno vigente al administrador."
		case http.StatusForbidden:
			return "No tienes permiso para esta operación en ese espacio (" + ae.Msg + ")."
		case http.StatusNotFound:
			return "No existe o no es visible para ti (" + ae.Msg + ")."
		case http.StatusConflict:
			if ae.Code == domain.CodeVersionConflict {
				return "El documento cambió desde la versión que vio la propuesta: confirma con el usuario y reintenta con force=true (" + ae.Msg + ")."
			}
			return "Conflicto: " + ae.Msg
		case http.StatusUnprocessableEntity, http.StatusBadRequest:
			return "Datos inválidos: " + ae.Msg
		case http.StatusRequestEntityTooLarge:
			return "Contenido demasiado grande: " + ae.Msg
		case http.StatusTooManyRequests:
			return "Demasiadas peticiones: espera un momento y reintenta."
		}
		return fmt.Sprintf("El servidor respondió %d: %s", ae.Status, ae.Msg)
	}
	if errors.Is(err, ErrOffline) || Unreachable(err) {
		return "Sin conexión con el servidor de Nexo: " + err.Error()
	}
	return err.Error()
}

// ParseWhen interpreta una fecha (AAAA-MM-DD, en UTC) o un instante RFC 3339.
func ParseWhen(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return &t, nil
	}
	return nil, fmt.Errorf("fecha inválida %q: use AAAA-MM-DD o RFC 3339", s)
}

// MarshalIndent es json con sangría (respuestas legibles para el asistente).
func MarshalIndent(v any) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(raw)
}
