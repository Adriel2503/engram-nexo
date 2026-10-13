package kbtest

// Embedder simulado y determinista para las pruebas sin red (NX-10 R2): un
// "modelo semántico" de juguete que agrupa palabras por concepto (un léxico
// pequeño español/inglés y raíces de 5 letras sin tildes) y proyecta los
// conceptos con hashing en 64 dimensiones. Dos textos que comparten
// conceptos quedan cerca aunque usen otras palabras ("password" y
// "contraseña", "retry" y "reintentos"). Sirve para probar la tubería
// (cola, indexador, fusión, umbral, filtros); la calidad real se mide con
// Gemini (design §20.6).

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/embed"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/ingest"
)

// FakeDims es la dimensión del embedder simulado.
const FakeDims = 64

// FakeEmbedder implementa embed.Embedder.
type FakeEmbedder struct {
	ModelName string
	// D es la dimensión (FakeDims por defecto; 3072 prueba halfvec).
	D int
	// Calls y Texts cuentan las llamadas y los textos vectorizados.
	Calls atomic.Int64
	Texts atomic.Int64

	mu sync.Mutex
	// Fail hace fallar todas las llamadas con ese error (proveedor caído).
	Fail error
	// Reject hace fallar (error permanente) los textos que contienen la
	// cadena.
	Reject string
}

// NewFakeEmbedder crea el embedder simulado.
func NewFakeEmbedder() *FakeEmbedder {
	return &FakeEmbedder{ModelName: "fake-semantic-v1", D: FakeDims}
}

// FakeConfig es la configuración que acompaña al embedder simulado.
func (f *FakeEmbedder) FakeConfig() embed.Config {
	return embed.Config{Provider: "fake", Model: f.ModelName, Dims: f.D, Batch: 100}
}

// SetFail cambia el error de todas las llamadas (nil = funciona).
func (f *FakeEmbedder) SetFail(err error) {
	f.mu.Lock()
	f.Fail = err
	f.mu.Unlock()
}

// SetReject rechaza los textos que contienen s.
func (f *FakeEmbedder) SetReject(s string) {
	f.mu.Lock()
	f.Reject = s
	f.mu.Unlock()
}

// Provider implementa embed.Embedder.
func (f *FakeEmbedder) Provider() string { return "fake" }

// Model implementa embed.Embedder.
func (f *FakeEmbedder) Model() string { return f.ModelName }

// Dims implementa embed.Embedder.
func (f *FakeEmbedder) Dims() int { return f.D }

// Embed implementa embed.Embedder.
func (f *FakeEmbedder) Embed(_ context.Context, texts []string, _ embed.Kind) ([][]float32, error) {
	f.Calls.Add(1)
	f.mu.Lock()
	fail, reject := f.Fail, f.Reject
	f.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if reject != "" && strings.Contains(t, reject) {
			return nil, &embed.APIError{Provider: "fake", Status: 400, Message: "texto rechazado"}
		}
		out[i] = FakeVector(t, f.D)
	}
	f.Texts.Add(int64(len(texts)))
	return out, nil
}

// ErrFakeDown simula un proveedor que no responde.
var ErrFakeDown = errors.Join(embed.ErrUnavailable, errors.New("simulado"))

var fakeWordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// fakeConcepts agrupa palabras equivalentes (español e inglés).
var fakeConcepts = map[string]string{
	"retry": "reintento", "retries": "reintento", "reintento": "reintento", "reintentos": "reintento", "reintentar": "reintento",
	"policy": "politica", "politica": "politica", "politicas": "politica",
	"external": "externo", "externo": "externo", "externos": "externo", "externas": "externo",
	"calls": "llamada", "call": "llamada", "llamada": "llamada", "llamadas": "llamada", "invocar": "llamada",
	"password": "secreto", "contrasena": "secreto", "contrasenas": "secreto", "clave": "secreto", "secreto": "secreto", "secretos": "secreto", "credenciales": "secreto", "token": "secreto", "tokens": "secreto", "sensibles": "secreto", "sensible": "secreto",
	"log": "log", "logs": "log", "registro": "log", "registrar": "log", "escribir": "log", "loguear": "log", "logging": "log", "bitacora": "log",
	"user": "usuario", "usuario": "usuario", "usuarios": "usuario",
	"microfrontend": "frontend", "microfrontends": "frontend", "frontend": "frontend",
	"microservicio": "backend", "microservicios": "backend", "backend": "backend", "servicio": "backend",
	"table": "tabla", "tables": "tabla", "tabla": "tabla", "tablas": "tabla",
}

var fakeStop = map[string]bool{"de": true, "la": true, "el": true, "en": true, "los": true, "las": true, "un": true, "una": true, "y": true, "o": true,
	"que": true, "se": true, "del": true, "al": true, "por": true, "para": true, "con": true, "the": true, "for": true, "of": true, "to": true, "a": true,
	"puedo": true, "como": true, "cual": true, "es": true, "lo": true}

// FakeVector es el vector determinista de un texto.
func FakeVector(text string, dims int) []float32 {
	v := make([]float64, dims)
	for _, w := range fakeWordRe.FindAllString(strings.ToLower(ingest.Fold(text)), -1) {
		if fakeStop[w] || len(w) < 2 {
			continue
		}
		c, ok := fakeConcepts[w]
		if !ok {
			c = w
			if len([]rune(c)) > 5 {
				c = string([]rune(c)[:5])
			}
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(c))
		x := h.Sum64()
		for k := 0; k < 3; k++ {
			idx := int((x >> (k * 16)) % uint64(dims))
			sign := 1.0
			if (x>>(48+k))&1 == 1 {
				sign = -1
			}
			v[idx] += sign
		}
	}
	var n float64
	for _, x := range v {
		n += x * x
	}
	out := make([]float32, dims)
	if n == 0 {
		out[0] = 1
		return out
	}
	n = math.Sqrt(n)
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out
}
