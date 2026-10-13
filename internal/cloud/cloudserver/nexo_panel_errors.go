package cloudserver

// Nexo IA · NX-10 (panel, OP-06) — errores de texto plano del panel en
// español, sin editar los handlers de Engram. Los handlers de /dashboard/
// responden fallos con http.Error (texto plano, en inglés y a veces con el
// error de la base pegado). Este middleware, con la marca de Nexo activa,
// retiene el cuerpo de esas respuestas (4xx y 5xx, hasta 8 KB), lo pasa por
// dashboard.NexoErrorText y lo reemplaza si lo conoce; un error interno solo
// deja su detalle en el log del servidor. Todo lo demás (HTML, JSON, la API,
// los archivos estáticos y los mensajes que no conoce) pasa sin cambios.

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud/dashboard"
)

const nexoPanelErrorMaxBody = 8 << 10

func nexoPanelErrorText(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		inPanel := p == "/dashboard" || strings.HasPrefix(p, "/dashboard/")
		if !dashboard.NexoBrand() || !inPanel || strings.HasPrefix(p, "/dashboard/static/") {
			next.ServeHTTP(w, r)
			return
		}
		pw := &panelErrorWriter{ResponseWriter: w, r: r}
		next.ServeHTTP(pw, r)
		pw.finish()
	})
}

type panelErrorWriter struct {
	http.ResponseWriter
	r       *http.Request
	status  int
	started bool // la cabecera ya salió al cliente
	capture bool // se retiene el cuerpo de un error de texto plano
	buf     bytes.Buffer
}

func (w *panelErrorWriter) WriteHeader(code int) {
	if w.started || w.capture {
		return
	}
	if code >= 400 && strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		w.capture, w.status = true, code
		return
	}
	w.started = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *panelErrorWriter) Write(b []byte) (int, error) {
	if !w.started && !w.capture {
		w.WriteHeader(http.StatusOK)
	}
	if !w.capture {
		return w.ResponseWriter.Write(b)
	}
	if w.buf.Len()+len(b) > nexoPanelErrorMaxBody { // demasiado grande: no es un mensaje de error
		w.release()
		return w.ResponseWriter.Write(b)
	}
	return w.buf.Write(b)
}

// release deja salir lo retenido tal cual.
func (w *panelErrorWriter) release() {
	w.capture, w.started = false, true
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(w.buf.Bytes())
	w.buf.Reset()
}

func (w *panelErrorWriter) finish() {
	if !w.capture {
		return
	}
	text := strings.TrimSpace(w.buf.String())
	es, ok := dashboard.NexoErrorText(w.status, text)
	if !ok {
		w.release()
		return
	}
	if w.status >= 500 {
		nexoLogInternal("dashboard "+w.r.Method+" "+w.r.URL.Path, errors.New(text))
	}
	w.capture, w.started = false, true
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write([]byte(es + "\n"))
}

// Flush y Unwrap mantienen las capacidades del ResponseWriter original.
func (w *panelErrorWriter) Flush() {
	if w.capture {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *panelErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
