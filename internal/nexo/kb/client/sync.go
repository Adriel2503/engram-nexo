package client

// Sincronización de la copia local con GET /nexo/kb/v1/sync (D-01).
//
// Ventana de solape (riesgo D-K2b-17 de la tanda 1): el feed numera los
// cambios con un BIGSERIAL; un cambio puede recibir su seq antes que otro y
// confirmarse después. Si el cliente avanzara su cursor al último seq leído,
// podría saltarse ese cambio para siempre. Por eso el cliente guarda puntos
// de control (seq, hora de la sincronización) y cada sincronización relee
// desde el seq del punto más nuevo que sea anterior en al menos la ventana
// (NEXO_KB_SYNC_OVERLAP_SECONDS, 120 s por defecto) a la última
// sincronización: cualquier cambio que siguiera en vuelo en una
// sincronización anterior se vuelve a leer. Aplicar un documento es
// idempotente (se reemplaza por su estado final), así que releer no duplica.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// EnvSyncOverlap fija la ventana de solape en segundos.
const EnvSyncOverlap = "NEXO_KB_SYNC_OVERLAP_SECONDS"

// maxCheckpoints acota los puntos de control guardados.
const maxCheckpoints = 30

// defaultOverlapSeqs es el solape en cantidad de cambios cuando todavía no hay un
// punto de control anterior a la ventana de tiempo.
const defaultOverlapSeqs = 1000

type checkpoint struct {
	Seq int64     `json:"seq"`
	At  time.Time `json:"at"`
}

// SyncReport resume una sincronización.
type SyncReport struct {
	Since         int64    `json:"since"`
	Seq           int64    `json:"seq"`
	Pages         int      `json:"pages"`
	Upserts       int      `json:"upserts"`
	Removes       int      `json:"removes"`
	SpacesRemoved []string `json:"spaces_removed,omitempty"`
	SpacesAdded   []string `json:"spaces_added,omitempty"`
	FullResync    bool     `json:"full_resync"`
	Reason        string   `json:"reason,omitempty"`
	Duration      string   `json:"duration"`
	Status        Status   `json:"status"`
}

// Syncer sincroniza una copia local con el servidor.
type Syncer struct {
	API     *API
	Local   *Local
	Overlap time.Duration
	// OverlapSeqs: cambios que se releen si no hay un punto de control anterior
	// a la ventana (por defecto 1000).
	OverlapSeqs int64
	// PageSize es el límite por página del feed (máximo del servidor: 500).
	PageSize int
	now      func() time.Time
}

// NewSyncer crea el sincronizador.
func NewSyncer(api *API, local *Local) *Syncer {
	return &Syncer{API: api, Local: local, Overlap: time.Duration(envInt(EnvSyncOverlap, 120)) * time.Second, PageSize: 200, OverlapSeqs: defaultOverlapSeqs, now: time.Now}
}

// fingerprint identifica servidor + token sin guardar el token (solo un hash
// truncado): si cambia la persona o el servidor, la copia se reconstruye.
func fingerprint(server, token string) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(server, "/") + "\n" + token))
	return hex.EncodeToString(sum[:8])
}

func (s *Syncer) checkpoints(ctx context.Context) []checkpoint {
	var cps []checkpoint
	_ = json.Unmarshal([]byte(s.Local.meta(ctx, "checkpoints")), &cps)
	return cps
}

// sinceFrom elige el cursor: el seq del punto de control más nuevo cuya hora
// sea anterior a la última sincronización menos la ventana de solape.
func sinceFrom(cps []checkpoint, overlap time.Duration, overlapSeqs int64) int64 {
	if len(cps) == 0 {
		return 0
	}
	last := cps[len(cps)-1]
	since, found := int64(0), false
	for _, c := range cps {
		if !c.At.After(last.At.Add(-overlap)) {
			since, found = c.Seq, true
		}
	}
	if !found {
		// Sin un punto anterior a la ventana (segunda sincronización): se
		// releen los últimos overlapSeqs cambios en vez de todo el feed.
		since = max(0, last.Seq-overlapSeqs)
	}
	return since
}

// Sync trae los cambios y los aplica. Ante un error deja la copia como
// estaba (cada página se aplica en su transacción) y lo anota.
func (s *Syncer) Sync(ctx context.Context) (SyncReport, error) {
	started := s.now().UTC()
	rep := SyncReport{}
	err := s.sync(ctx, &rep, started)
	rep.Duration = s.now().Sub(started).Round(time.Millisecond).String()
	if err != nil {
		_ = setMeta(ctx, s.Local.db, "last_error", truncate(err.Error(), 300))
		_ = setMeta(ctx, s.Local.db, "last_error_at", s.now().UTC().Format(time.RFC3339Nano))
	}
	rep.Status = s.Local.Status(ctx)
	return rep, err
}

func (s *Syncer) sync(ctx context.Context, rep *SyncReport, started time.Time) error {
	fp := fingerprint(s.API.Server, s.API.Token)
	if prev := s.Local.meta(ctx, "fingerprint"); prev != fp {
		// Otra persona u otro servidor: la copia anterior no vale.
		if prev != "" {
			rep.Reason = "cambió el usuario o el servidor: copia reconstruida"
		}
		if err := s.Local.Wipe(ctx); err != nil {
			return err
		}
		if err := setMeta(ctx, s.Local.db, "fingerprint", fp); err != nil {
			return err
		}
		if err := setMeta(ctx, s.Local.db, "server", s.API.Server); err != nil {
			return err
		}
	}
	cps := s.checkpoints(ctx)
	since := sinceFrom(cps, s.Overlap, s.OverlapSeqs)
	rep.Since = since
	known, err := s.Local.spaceSlugs(ctx)
	if err != nil {
		return err
	}
	full := since == 0
	restarted, resynced := false, false
	limit := s.PageSize
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	for page := 0; ; page++ {
		if page > 100000 {
			return fmt.Errorf("feed sin fin (más de 100000 páginas)")
		}
		var p SyncPage
		v := url.Values{}
		v.Set("since", strconv.FormatInt(since, 10))
		v.Set("limit", strconv.Itoa(limit))
		if err := s.API.Do(ctx, "GET", "/nexo/kb/v1/sync?"+v.Encode(), nil, &p); err != nil {
			return err
		}
		rep.Pages++
		if p.ResyncRequired {
			if resynced {
				return fmt.Errorf("el servidor pide reconstruir la copia dos veces seguidas")
			}
			resynced, full, since = true, true, 0
			rep.Reason = "el servidor pidió reconstruir la copia"
			if err := s.Local.Wipe(ctx, "fingerprint", "server"); err != nil {
				return err
			}
			cps = nil
			continue
		}
		if page == 0 && !restarted && len(known) > 0 {
			for _, sp := range p.VisibleSpaces {
				if !known[sp.Slug] {
					rep.SpacesAdded = append(rep.SpacesAdded, sp.Slug)
				}
			}
			// Un espacio que se vuelve visible trae documentos con seq
			// anteriores al cursor: se relee el feed completo una vez.
			if len(rep.SpacesAdded) > 0 && !full {
				restarted, full, since = true, true, 0
				rep.Reason = "nuevos espacios visibles: " + strings.Join(rep.SpacesAdded, ", ")
				page = -1
				rep.Pages = 0
				continue
			}
		}
		st, err := s.Local.Apply(ctx, p)
		if err != nil {
			return err
		}
		rep.Upserts += st.Upserts
		rep.Removes += st.Removes
		rep.SpacesRemoved = append(rep.SpacesRemoved, st.SpacesRemoved...)
		since = p.Seq
		if !p.HasMore {
			break
		}
	}
	rep.Seq, rep.FullResync = since, full
	cps = append(cps, checkpoint{Seq: since, At: started})
	if len(cps) > maxCheckpoints {
		cps = cps[len(cps)-maxCheckpoints:]
	}
	raw, _ := json.Marshal(cps)
	for k, v := range map[string]string{"checkpoints": string(raw), "synced_at": s.now().UTC().Format(time.RFC3339Nano), "last_error": "", "last_error_at": ""} {
		if err := setMeta(ctx, s.Local.db, k, v); err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
