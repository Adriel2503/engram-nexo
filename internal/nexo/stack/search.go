package stack

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"

	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/secrets"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/stack/state"
)

type SearchOptions struct {
	Embedder, Model, Dims, URL, KeyFile, Deployment, Auth, Reranker, RerankModel, RerankURL, RerankKeyFile, RerankDeployment, RerankAuth, R1 string
	Example                                                                                                                                  bool
	key, rerankKey                                                                                                                           string
}

func searchFlags(f *flag.FlagSet, s *SearchOptions) {
	f.StringVar(&s.Embedder, "embedder", "off", "proveedor")
	f.StringVar(&s.Model, "embed-model", "", "modelo")
	f.StringVar(&s.Dims, "embed-dims", "", "dimensiones")
	f.StringVar(&s.URL, "embed-url", "", "URL")
	f.StringVar(&s.KeyFile, "embed-key-file", "", "archivo de clave")
	f.StringVar(&s.Deployment, "embed-deployment", "", "despliegue Azure")
	f.StringVar(&s.Auth, "embed-auth", "", "autenticación")
	f.StringVar(&s.Reranker, "rerank", "off", "reordenador")
	f.StringVar(&s.RerankModel, "rerank-model", "", "modelo")
	f.StringVar(&s.RerankURL, "rerank-url", "", "URL")
	f.StringVar(&s.RerankKeyFile, "rerank-key-file", "", "archivo de clave")
	f.StringVar(&s.RerankDeployment, "rerank-deployment", "", "despliegue Azure")
	f.StringVar(&s.RerankAuth, "rerank-auth", "", "autenticación")
	f.StringVar(&s.R1, "r1", "all", "mejoras")
}
func (a *App) environment(ctx context.Context, s *state.State, o Options) (map[string]string, error) {
	m := map[string]string{}
	for _, k := range []string{"POSTGRES_PASSWORD", "NEXO_OWNER_PASSWORD", "NEXO_APP_PASSWORD", "ENGRAM_CLOUD_TOKEN", "ENGRAM_CLOUD_TOKEN_PEPPER", "ENGRAM_JWT_SECRET"} {
		v, e := secrets.Hex(32)
		if e != nil {
			return nil, e
		}
		m[k] = v
	}
	m["ENGRAM_CLOUD_HOST"] = s.Bind
	m["ENGRAM_PORT"] = strconv.Itoa(s.Port)
	m["ENGRAM_CLOUD_ALLOWED_PROJECTS"] = "nexo-transversal"
	m["ENGRAM_TIMEZONE"] = "America/Lima"
	m["ENGRAM_DATA_DIR"] = s.Dir + "/engram-data"
	m["NEXO_HOME"] = s.Dir + "/nexo-home"
	m["NEXO_KB_PUBLIC_URL"] = s.ServerURL
	m["NEXO_SUPERADMIN_ALLOWED_CIDRS"] = "127.0.0.1/32,::1/128"
	db := o.DatabaseURL
	if db == "" {
		db = os.Getenv("NEXO_STACK_DATABASE_URL")
	}
	if s.ExternalDB {
		u, e := url.Parse(db)
		if e != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Host == "" {
			return nil, fmt.Errorf("--database-url debe apuntar a un Postgres dedicado")
		}
		m["ENGRAM_DATABASE_URL"] = db
		if owner := os.Getenv("NEXO_STACK_MIGRATION_DATABASE_URL"); owner != "" {
			m["NEXO_MIGRATION_DATABASE_URL"] = owner
		}
	} else {
		host := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.PGPort))
		if s.Mode == "prod" {
			host = "postgres:5432"
		}
		if s.Mode == "demo" {
			m["ENGRAM_DATABASE_URL"] = "postgres://postgres:" + m["POSTGRES_PASSWORD"] + "@" + host + "/engram_cloud?sslmode=disable"
		} else {
			m["ENGRAM_DATABASE_URL"] = "postgres://nexo_app:" + m["NEXO_APP_PASSWORD"] + "@" + host + "/engram_cloud?sslmode=disable"
			m["NEXO_MIGRATION_DATABASE_URL"] = "postgres://nexo_owner:" + m["NEXO_OWNER_PASSWORD"] + "@" + host + "/engram_cloud?sslmode=disable"
		}
	}
	if e := a.searchConfig(ctx, s, &o.Search, m); e != nil {
		return nil, e
	}
	return m, nil
}
