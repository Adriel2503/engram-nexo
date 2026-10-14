package stack

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/render"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

func (a *App) production(ctx context.Context, s *state.State, o Options, m map[string]string) error {
	if o.Search.Example || o.OwnDir != "" {
		return errors.New("prod no admite semilla de ejemplo; publica el contenido con kb upload")
	}
	if s.ExternalDB && m["NEXO_MIGRATION_DATABASE_URL"] == "" {
		return errors.New("prod con base externa requiere NEXO_STACK_MIGRATION_DATABASE_URL y una cuenta de aplicación separada")
	}
	image := o.Image
	if image == "" {
		image = s.Image
	}
	if image == "" {
		tag := regexp.MustCompile("[^A-Za-z0-9_.-]").ReplaceAllString(a.Version, "-")
		image = s.Project + "-cloud:" + tag
	}
	if strings.HasPrefix(image, "-") || strings.ContainsAny(image, "\r\n\x00") {
		return errors.New("referencia de imagen inválida")
	}
	if o.Image == "" {
		_, exists := a.Runner(ctx, "docker", "image", "inspect", image)
		if exists != nil || o.Rebuild {
			source := o.BuildFrom
			if source == "" {
				source, _ = os.Getwd()
			}
			source, e := filepath.Abs(source)
			if e != nil {
				return e
			}
			if _, e = os.Stat(filepath.Join(source, "docker", "cloud", "Dockerfile")); e != nil {
				return errors.New("no hay código fuente: indica --image o --build-from con el repositorio")
			}
			_, _ = fmt.Fprintln(a.Out, "[..] Construyendo imagen de servidor desde el repositorio.")
			if _, e = a.Runner(ctx, "docker", "build", "-f", filepath.Join(source, "docker", "cloud", "Dockerfile"), "--build-arg", "VERSION="+a.Version, "-t", image, source); e != nil {
				return errors.New("no se pudo construir la imagen; revisa Docker y el acceso a las imágenes base")
			}
		}
	}
	s.Image = image
	m["CLOUD_IMAGE"] = image
	delete(m, "ENGRAM_DATA_DIR")
	delete(m, "NEXO_HOME")
	delete(m, "NEXO_SERVICE_ACCOUNTS_ENABLED") // La red es propia; solo el proxy de este proyecto es de confianza.
	h := crc32.ChecksumIEEE([]byte(s.Project)) % 4096
	subnet := fmt.Sprintf("172.%d.%d", 16+h/256, h%256)
	s.Subnet = subnet + ".0/24"
	s.ProxyIP = subnet + ".10"
	m["NEXO_TRUSTED_PROXIES"] = s.ProxyIP + "/32"
	m["NEXO_SUPERADMIN_ALLOWED_CIDRS"] = "127.0.0.1/32,::1/128"
	if e := os.MkdirAll(filepath.Join(s.Dir, "audit-export"), 0700); e != nil {
		return e
	}
	return render.Files(render.Data{State: *s, Env: m, TwoAccounts: true})
}
func (a *App) productionHealth(ctx context.Context, s state.State) error {
	path := filepath.Join(s.Dir, "ca.crt")
	if e := a.wait(ctx, func() bool {
		_, e := a.compose(ctx, s, "cp", "proxy:/data/caddy/pki/authorities/local/root.crt", path)
		return e == nil
	}, "CA interna de Caddy"); e != nil {
		return e
	}
	return a.trustCA(s)
}
func (a *App) trustCA(s state.State) error {
	if a.caDir == s.Dir {
		return nil
	}
	if _, ok := a.HTTP.Transport.(*http.Transport); a.HTTP.Transport != nil && !ok {
		return nil
	}
	pem, e := os.ReadFile(filepath.Join(s.Dir, "ca.crt"))
	if e != nil {
		return e
	}
	roots, e := x509.SystemCertPool()
	if e != nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return errors.New("certificado de Caddy inválido")
	}
	var tr *http.Transport
	if a.HTTP.Transport == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	} else if original, ok := a.HTTP.Transport.(*http.Transport); ok {
		tr = original.Clone()
	} else {
		return nil
	}
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	a.HTTP.Transport = tr
	a.caDir = s.Dir
	return nil
}
func (a *App) bootstrapProd(ctx context.Context, s state.State) error {
	p, e := render.ReadAccess(s.Dir)
	if e == nil && len(p) > 0 {
		return nil
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	raw, e := a.compose(ctx, s, "exec", "-T", "cloud", "engram", "cloud", "bootstrap", "admin", "--username", "superadmin", "--email", "superadmin@stack.local", "--issue-token", "nexo-stack")
	if e != nil {
		return errors.New("no se pudo crear el superadmin de prod; recupera un bootstrap interrumpido desde el contenedor")
	}
	tok := regexp.MustCompile("egc_[a-z0-9-]+_[0-9a-f]{8}_[A-Za-z0-9-]{20,}").FindString(string(raw))
	if tok == "" {
		return errors.New("el bootstrap no entregó un token válido; revisa la configuración sin imprimir sus secretos")
	}
	return render.AccessFile(render.Data{State: s, Access: []render.Access{{Username: "superadmin", Role: "superadmin", Token: tok}}})
}
