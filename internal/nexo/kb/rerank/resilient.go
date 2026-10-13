package rerank

// Capa común a todos los adaptadores (RC-29): lotes, llamadas en paralelo con
// tope, reintentos con espera exponencial y fluctuación, respeto de
// Retry-After, límite por minuto, tiempo máximo por llamada, validación de la
// respuesta y borrado de la clave en los errores. Mismo diseño que
// embed/resilient.go; aquí los lotes de una consulta pueden ir en paralelo
// (la latencia de la búsqueda importa más que en el indexador).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// batchResult es lo que devuelve un adaptador para un lote: un puntaje en
// [0, 1] por pasaje, en el mismo orden, y los tokens que informó el
// proveedor (0 si no los informa).
type batchResult struct {
	scores       []float64
	promptTokens int
	outputTokens int
}

// adapter es lo que cada proveedor implementa: una llamada HTTP con un lote.
type adapter interface {
	call(ctx context.Context, hc *http.Client, query string, texts []string) (batchResult, error)
}

// maxQueryChars acota la pregunta enviada (la búsqueda ya la limita a 500).
const maxQueryChars = 1000

type client struct {
	cfg Config
	a   adapter
	hc  *http.Client

	mu     sync.Mutex // protege window
	window []time.Time
	sleep  func(context.Context, time.Duration) error
	now    func() time.Time

	calls, retries, malformed, failed, promptTok, outTok atomic.Int64
}

func newClient(c Config, a adapter) *client {
	return &client{cfg: c, a: a, hc: &http.Client{}, sleep: sleepCtx, now: time.Now}
}

// Name implementa Reranker.
func (c *client) Name() string { return c.cfg.ID() }

// Model implementa Reranker.
func (c *client) Model() string { return c.cfg.Model }

// Stats implementa StatsReporter.
func (c *client) Stats() Stats {
	return Stats{Calls: c.calls.Load(), Retries: c.retries.Load(), Malformed: c.malformed.Load(), FailedBatch: c.failed.Load(),
		PromptTokens: c.promptTok.Load(), OutputTokens: c.outTok.Load()}
}

// Rerank implementa Reranker.
func (c *client) Rerank(ctx context.Context, query string, passages []Passage) ([]Score, error) {
	out := make([]Score, len(passages))
	if len(passages) == 0 {
		return out, nil
	}
	query = clip(strings.TrimSpace(query), maxQueryChars)
	texts := make([]string, len(passages))
	for i, p := range passages {
		t := strings.TrimSpace(p.Text)
		if t == "" {
			t = "(vacío)"
		}
		texts[i] = clip(t, c.cfg.MaxChars)
	}
	batch := max(c.cfg.Batch, 1)
	var (
		wg   sync.WaitGroup
		emu  sync.Mutex
		errs []error
	)
	sem := make(chan struct{}, max(c.cfg.Concurrency, 1))
	for start := 0; start < len(texts); start += batch {
		end := min(start+batch, len(texts))
		if ctx.Err() != nil {
			emu.Lock()
			errs = append(errs, ctx.Err())
			emu.Unlock()
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if rec := recover(); rec != nil { // un adaptador nunca tira la búsqueda
					c.failed.Add(1)
					emu.Lock()
					errs = append(errs, fmt.Errorf("rerank %s: pánico en el adaptador: %v", c.Name(), rec))
					emu.Unlock()
				}
			}()
			res, err := c.callWithRetry(ctx, query, texts[start:end])
			if err != nil {
				c.failed.Add(1)
				emu.Lock()
				errs = append(errs, err)
				emu.Unlock()
				return
			}
			for i, v := range res {
				out[start+i] = Score{Value: v, OK: true}
			}
		}(start, end)
	}
	wg.Wait()
	if len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	return out, nil
}

func (c *client) callWithRetry(ctx context.Context, query string, texts []string) ([]float64, error) {
	var last error
	malformedLeft := 1 // una segunda oportunidad ante una respuesta no válida
	for attempt := 0; ; {
		if err := c.waitQuota(ctx); err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
		res, err := c.a.call(cctx, c.hc, query, texts)
		cancel()
		c.calls.Add(1)
		c.promptTok.Add(int64(res.promptTokens))
		c.outTok.Add(int64(res.outputTokens))
		if err == nil {
			err = c.check(res.scores, len(texts))
		}
		if err == nil {
			return res.scores, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		err = c.redact(err)
		last = err
		if errors.Is(err, ErrMalformed) {
			c.malformed.Add(1)
			if malformedLeft > 0 {
				malformedLeft--
				c.retries.Add(1)
				continue
			}
			return nil, err
		}
		if !retryable(err) {
			return nil, err
		}
		if attempt >= c.cfg.Retries {
			break
		}
		c.retries.Add(1)
		delay := c.backoff(attempt, err)
		attempt++
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: %s %s tras %d intentos: %v", ErrUnavailable, c.Name(), c.cfg.Model, c.cfg.Retries+1, last)
}

// check valida la cantidad y el rango de los puntajes.
func (c *client) check(scores []float64, n int) error {
	if len(scores) != n {
		return fmt.Errorf("%w: se pidieron %d puntajes y llegaron %d", ErrMalformed, n, len(scores))
	}
	for _, v := range scores {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: puntaje no numérico", ErrMalformed)
		}
	}
	return nil
}

// backoff: 0,5 s, 1 s, 2 s ... hasta 10 s, con fluctuación; un Retry-After
// del proveedor manda (hasta 2 minutos).
func (c *client) backoff(attempt int, err error) time.Duration {
	var ae *APIError
	if errors.As(err, &ae) && ae.RetryAfter > 0 {
		return min(ae.RetryAfter, 2*time.Minute)
	}
	d := 500 * time.Millisecond << min(attempt, 5)
	d = min(d, 10*time.Second)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// waitQuota espera hasta que la llamada quepa en el límite por minuto.
func (c *client) waitQuota(ctx context.Context) error {
	if c.cfg.RPM <= 0 {
		return nil
	}
	for {
		c.mu.Lock()
		now := c.now()
		keep := c.window[:0]
		for _, at := range c.window {
			if now.Sub(at) < time.Minute {
				keep = append(keep, at)
			}
		}
		c.window = keep
		if len(c.window) < c.cfg.RPM {
			c.window = append(c.window, now)
			c.mu.Unlock()
			return nil
		}
		wait := time.Minute - now.Sub(c.window[0]) + 10*time.Millisecond
		c.mu.Unlock()
		if err := c.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// retryable: error transitorio del proveedor o de red (no el del contexto del
// llamador, que ya se comprobó).
func retryable(err error) bool {
	if IsTransient(err) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset")
}

// redact borra la clave de cualquier mensaje (por si un proveedor o un error
// de red la repitiera).
func (c *client) redact(err error) error {
	k := c.cfg.APIKey
	if err == nil || len(k) < 6 || !strings.Contains(err.Error(), k) {
		return err
	}
	var ae *APIError
	if errors.As(err, &ae) {
		cp := *ae
		cp.Message = strings.ReplaceAll(cp.Message, k, "[clave]")
		if strings.Contains(cp.Error(), k) {
			return errors.New(strings.ReplaceAll(cp.Error(), k, "[clave]"))
		}
		return &cp
	}
	if errors.Is(err, ErrMalformed) {
		return fmt.Errorf("%w: %s", ErrMalformed, strings.ReplaceAll(err.Error(), k, "[clave]"))
	}
	return errors.New(strings.ReplaceAll(err.Error(), k, "[clave]"))
}

func clip(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// shortBody acota el texto de un error del proveedor.
func shortBody(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 300 {
		s = clip(s, 300) + "..."
	}
	return s
}

// parseRetryAfter lee el encabezado Retry-After en segundos o en fecha HTTP.
func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	var secs int
	if _, err := fmt.Sscanf(h, "%d", &secs); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}
