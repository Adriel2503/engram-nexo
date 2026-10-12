package cloudserver

// Nexo IA · NX-02 — registro de conexiones (lado servidor).
//
// nexoRecordActivity se llama al final del camino exitoso de los handlers de
// sincronización. Nunca devuelve error ni cambia la respuesta HTTP: si el
// registro falla, se loguea y la sincronización continúa (RF-16).

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

// nexoActivityRecorder se detecta por type assertion para no ampliar
// ChunkStore ni romper los fakes de los tests de Engram.
type nexoActivityRecorder interface {
	RecordActivity(ctx context.Context, event cloudstore.ActivityEvent) error
}

type nexoActivityPurger interface {
	PurgeActivityOlderThan(ctx context.Context, days int) (int64, error)
}

// nexoActivityTimeout acota la escritura del registro para no demorar la
// respuesta si Postgres está lento.
const nexoActivityTimeout = 2 * time.Second

// nexoRecordActivity registra una conexión exitosa. clientClaim es lo que
// declaró la PC (created_by), solo como referencia.
func (s *CloudServer) nexoRecordActivity(r *http.Request, op, project string, count int, clientClaim string) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[nexo] activity: panic recuperado (op=%s): %v", op, rec)
		}
	}()
	if s == nil || r == nil {
		return
	}
	recorder, ok := s.store.(nexoActivityRecorder)
	if !ok {
		return
	}
	event := cloudstore.ActivityEvent{
		Actor:     nexoResolveActor(r.Context(), clientClaim),
		Operation: op,
		Project:   project,
		ItemCount: count,
		LogDetail: nexoActivityLogsDetail(op, count),
	}
	if cc, ok := NexoClientContextFrom(r.Context()); ok {
		event.ClientIP = cc.IP
		event.UserAgent = cc.UserAgent
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), nexoActivityTimeout)
	defer cancel()
	if err := recorder.RecordActivity(ctx, event); err != nil {
		log.Printf("[nexo] activity: registro fallido (op=%s project=%q): %v", op, project, err)
	}
}

// nexoActivityLogsDetail decide si la operación agrega fila de detalle. Las
// descargas que no entregan datos y el manifiesto solo tocan la presencia
// (RF-11, CA-12).
func nexoActivityLogsDetail(op string, count int) bool {
	switch op {
	case cloudstore.NexoOpPullManifest:
		return false
	case cloudstore.NexoOpPullMutations:
		return count > 0
	default:
		return true
	}
}

// nexoChunkItemCount cuenta los elementos de un paquete ya validado.
func nexoChunkItemCount(chunk engramsync.ChunkData) int {
	return len(chunk.Sessions) + len(chunk.Observations) + len(chunk.Prompts) + len(chunk.Mutations)
}

// nexoChunkPayloadItemCount cuenta los elementos de un paquete crudo sin
// decodificar su contenido.
func nexoChunkPayloadItemCount(payload []byte) int {
	var counts struct {
		Sessions     []json.RawMessage `json:"sessions"`
		Observations []json.RawMessage `json:"observations"`
		Prompts      []json.RawMessage `json:"prompts"`
		Mutations    []json.RawMessage `json:"mutations"`
	}
	if err := json.Unmarshal(payload, &counts); err != nil {
		return 0
	}
	return len(counts.Sessions) + len(counts.Observations) + len(counts.Prompts) + len(counts.Mutations)
}

// nexoMutationsProject devuelve el proyecto de los cambios devueltos; si hay
// varios, la lista ordenada separada por comas (acotada).
func nexoMutationsProject(mutations []cloudstore.StoredMutation, fallback string) string {
	if len(mutations) == 0 {
		return fallback
	}
	seen := make(map[string]struct{})
	projects := make([]string, 0, 1)
	for _, m := range mutations {
		p := strings.TrimSpace(m.Project)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		projects = append(projects, p)
	}
	if len(projects) == 0 {
		return fallback
	}
	sort.Strings(projects)
	joined := strings.Join(projects, ",")
	if len(joined) > 512 {
		joined = joined[:512]
	}
	return joined
}

// ─── Conservación (RF-15, T-14) ──────────────────────────────────────────────

// NexoActivityRetentionEnv define los días que se conserva el detalle.
const NexoActivityRetentionEnv = "NEXO_ACTIVITY_RETENTION_DAYS"

const nexoDefaultActivityRetentionDays = 180

const nexoActivityPurgeInterval = 24 * time.Hour

// nexoActivityRetentionDays lee NEXO_ACTIVITY_RETENTION_DAYS (por defecto 180;
// 0 = no depurar; inválido = por defecto).
func nexoActivityRetentionDays() int {
	raw := strings.TrimSpace(os.Getenv(NexoActivityRetentionEnv))
	if raw == "" {
		return nexoDefaultActivityRetentionDays
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 {
		log.Printf("[nexo] %s=%q inválido; se usa %d", NexoActivityRetentionEnv, raw, nexoDefaultActivityRetentionDays)
		return nexoDefaultActivityRetentionDays
	}
	// NX-07 (RS-03): la función de depuración rechaza plazos menores al mínimo.
	if days > 0 && days < security.RetentionMinimumDays {
		log.Printf("[nexo] %s=%d es menor que el mínimo; se usa %d", NexoActivityRetentionEnv, days, security.RetentionMinimumDays)
		return security.RetentionMinimumDays
	}
	return days
}

// nexoPurgeActivityOnce ejecuta una depuración. Devuelve las filas borradas.
func (s *CloudServer) nexoPurgeActivityOnce(ctx context.Context, days int) int64 {
	purger, ok := s.store.(nexoActivityPurger)
	if !ok || days <= 0 {
		return 0
	}
	deleted, err := purger.PurgeActivityOlderThan(ctx, days)
	if err != nil {
		log.Printf("[nexo] activity: depuración fallida: %v", err)
		return 0
	}
	if deleted > 0 {
		log.Printf("[nexo] activity: %d filas con más de %d días depuradas", deleted, days)
	}
	return deleted
}

// nexoStartActivityRetention depura al iniciar y luego cada 24 h, en segundo
// plano, mientras viva el proceso.
func (s *CloudServer) nexoStartActivityRetention() {
	days := nexoActivityRetentionDays()
	if days <= 0 {
		return
	}
	if _, ok := s.store.(nexoActivityPurger); !ok {
		return
	}
	go func() {
		s.nexoPurgeActivityOnce(context.Background(), days)
		ticker := time.NewTicker(nexoActivityPurgeInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.nexoPurgeActivityOnce(context.Background(), days)
		}
	}()
}
