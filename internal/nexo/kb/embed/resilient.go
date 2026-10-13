package embed

// Capa común a todos los adaptadores (RC-12): lotes, reintentos con espera
// exponencial y fluctuación, respeto de Retry-After, límites por minuto,
// tiempo máximo por llamada, validación de la respuesta y borrado de la
// clave en los errores. Las llamadas al proveedor se serializan (una a la
// vez por Embedder): el volumen es bajo y así los límites son exactos.

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
	"time"
	"unicode/utf8"
)

// adapter es lo que cada proveedor implementa: una llamada HTTP con un lote.
type adapter interface {
	maxBatch() int
	call(ctx context.Context, hc *http.Client, texts []string, kind Kind) ([][]float32, error)
}

type client struct {
	cfg Config
	a   adapter
	hc  *http.Client

	mu     sync.Mutex // serializa las llamadas
	window []usage    // llamadas del último minuto (límites RPM y TPM)
	sleep  func(context.Context, time.Duration) error
	now    func() time.Time
}

type usage struct {
	at     time.Time
	tokens int
}

func newClient(c Config, a adapter) *client {
	return &client{cfg: c, a: a, hc: &http.Client{}, sleep: sleepCtx, now: time.Now}
}

func (c *client) Provider() string { return c.cfg.Provider }
func (c *client) Model() string    { return c.cfg.Model }
func (c *client) Dims() int        { return c.cfg.Dims }

// Embed implementa Embedder.
func (c *client) Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	prefix := c.cfg.DocPrefix
	if kind == KindQuery {
		prefix = c.cfg.QueryPrefix
	}
	in := make([]string, len(texts))
	for i, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			t = "(vacío)" // los proveedores rechazan textos vacíos
		}
		in[i] = clip(prefix+t, c.cfg.MaxChars)
	}
	batch := c.cfg.Batch
	if m := c.a.maxBatch(); batch <= 0 || batch > m {
		batch = m
	}
	out := make([][]float32, 0, len(in))
	for start := 0; start < len(in); start += batch {
		end := min(start+batch, len(in))
		vecs, err := c.callWithRetry(ctx, in[start:end], kind)
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (c *client) callWithRetry(ctx context.Context, texts []string, kind Kind) ([][]float32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tokens := 0
	for _, t := range texts {
		tokens += len(t)/4 + 1
	}
	var last error
	for attempt := 0; attempt <= c.cfg.Retries; attempt++ {
		if err := c.waitQuota(ctx, tokens); err != nil {
			return nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
		vecs, err := c.a.call(cctx, c.hc, texts, kind)
		cancel()
		c.window = append(c.window, usage{at: c.now(), tokens: tokens})
		if err == nil {
			if err := c.check(vecs, len(texts)); err != nil {
				return nil, err
			}
			if c.cfg.Normalize {
				for _, v := range vecs {
					normalize(v)
				}
			}
			return vecs, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		err = c.redact(err)
		last = err
		if !retryable(err) {
			return nil, err
		}
		if attempt == c.cfg.Retries {
			break
		}
		if err := c.sleep(ctx, c.backoff(attempt, err)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: %s %s tras %d intentos: %v", ErrUnavailable, c.cfg.Provider, c.cfg.Model, c.cfg.Retries+1, last)
}

// check valida la cantidad y la dimensión de los vectores.
func (c *client) check(vecs [][]float32, n int) error {
	if len(vecs) != n {
		return fmt.Errorf("embed %s: se pidieron %d vectores y llegaron %d", c.cfg.Provider, n, len(vecs))
	}
	for _, v := range vecs {
		if len(v) != c.cfg.Dims {
			return fmt.Errorf("embed %s: el modelo %s devolvió %d dimensiones y se configuraron %d (%s)", c.cfg.Provider, c.cfg.Model, len(v), c.cfg.Dims, EnvDims)
		}
	}
	return nil
}

// backoff: 0,5 s, 1 s, 2 s ... hasta 30 s, con fluctuación; un Retry-After
// del proveedor manda (hasta 2 minutos).
func (c *client) backoff(attempt int, err error) time.Duration {
	var ae *APIError
	if errors.As(err, &ae) && ae.RetryAfter > 0 {
		return min(ae.RetryAfter, 2*time.Minute)
	}
	d := 500 * time.Millisecond << min(attempt, 6)
	d = min(d, 30*time.Second)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// waitQuota espera hasta que la llamada quepa en los límites del minuto.
func (c *client) waitQuota(ctx context.Context, tokens int) error {
	if c.cfg.RPM <= 0 && c.cfg.TPM <= 0 {
		return nil
	}
	for {
		now := c.now()
		keep := c.window[:0]
		used := 0
		for _, u := range c.window {
			if now.Sub(u.at) < time.Minute {
				keep = append(keep, u)
				used += u.tokens
			}
		}
		c.window = keep
		okRPM := c.cfg.RPM <= 0 || len(c.window) < c.cfg.RPM
		okTPM := c.cfg.TPM <= 0 || used+tokens <= c.cfg.TPM || len(c.window) == 0
		if okRPM && okTPM {
			return nil
		}
		wait := time.Minute - now.Sub(c.window[0].at) + 10*time.Millisecond
		if err := c.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// retryable: error transitorio del proveedor o de red (no el del contexto
// del llamador, que ya se comprobó).
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
		return &cp
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

// parseRetryAfter lee el encabezado Retry-After en segundos.
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
		return time.Until(t)
	}
	return 0
}

// normalize deja el vector con norma 1 (similitud de coseno = producto
// interno; Gemini por debajo de 3072 dimensiones no lo entrega unitario).
func normalize(v []float32) {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
}
