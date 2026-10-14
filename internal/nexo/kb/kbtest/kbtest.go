// Package kbtest arma entornos de prueba contra Postgres real
// (CLOUDSTORE_TEST_DSN) para el módulo de conocimiento: un esquema aislado
// por prueba migrado por cloudstore.New (las mismas migraciones del
// servidor), personas con usuario y token, identidades y el modo de dos
// cuentas de NX-07. Solo lo importan pruebas.
package kbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/authz"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/kb/service"
	"github.com/Gentleman-Programming/engram/v3/internal/nexo/security"
)

// Env es un entorno aislado.
type Env struct {
	T      testing.TB
	DSN    string // DSN con search_path del esquema (cuenta de pruebas o de aplicación)
	Schema string
	Admin  *sql.DB // superusuario de pruebas con search_path al esquema
	CS     *cloudstore.CloudStore
	DB     *sql.DB
	Roles  *authz.Service
	KB     *service.Service
	tokens map[string]string
	kinds  map[string]string
	names  map[string]string
}

var seq atomic.Int64

// BaseDSN devuelve CLOUDSTORE_TEST_DSN o salta la prueba.
func BaseDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}
	return dsn
}

// WithSearchPath agrega search_path (y opcionalmente usuario) a una DSN.
func WithSearchPath(t testing.TB, base, user, password, schema string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// NewSchema crea un esquema vacío y lo borra al terminar. prepare corre con
// el superusuario de pruebas antes de migrar (por ejemplo, para crear una
// configuración de búsqueda previa).
func NewSchema(t testing.TB, prefix string) (schema, dsn string, admin *sql.DB) {
	t.Helper()
	base := BaseDSN(t)
	schema = fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), seq.Add(1))
	adminDB, err := sql.Open("pgx", WithSearchPath(t, base, "", "", schema))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = adminDB.Close()
	})
	return schema, WithSearchPath(t, base, "", "", schema), adminDB
}

// New crea un entorno migrado con una sola cuenta.
func New(t testing.TB, opts ...service.Option) *Env {
	t.Helper()
	schema, dsn, admin := NewSchema(t, "nexo_kb")
	return Open(t, schema, dsn, admin, opts...)
}

// Open migra (cloudstore.New) y arma los servicios.
func Open(t testing.TB, schema, dsn string, admin *sql.DB, opts ...service.Option) *Env {
	t.Helper()
	cs, err := cloudstore.New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	db := cs.NexoDB()
	eng := authz.NewPostgres(db, authz.WithProjectNormalizer(cloudstore.NormalizeProjectGrant))
	roles := authz.NewService(db, eng)
	return &Env{T: t, DSN: dsn, Schema: schema, Admin: admin, CS: cs, DB: db, Roles: roles, KB: service.New(db, roles, opts...),
		tokens: map[string]string{}, kinds: map[string]string{}, names: map[string]string{}}
}

// TwoAccounts crea las cuentas dueña y de aplicación (NX-07) y un esquema de
// la dueña; devuelve el entorno abierto con la cuenta de aplicación y la DSN
// de la dueña. Los roles se borran al terminar.
func TwoAccounts(t testing.TB, opts ...service.Option) (*Env, string) {
	t.Helper()
	base := BaseDSN(t)
	suffix := fmt.Sprintf("%d_%d", time.Now().UnixNano(), seq.Add(1))
	schema, owner, app := "nexo_kb2_"+suffix, "nexo_kb_owner_"+suffix, "nexo_kb_app_"+suffix
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	password := hex.EncodeToString(b)
	admin, err := sql.Open("pgx", WithSearchPath(t, base, "", "", schema))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`CREATE ROLE ` + owner + ` LOGIN PASSWORD '` + password + `' NOSUPERUSER NOCREATEDB NOCREATEROLE`,
		`CREATE ROLE ` + app + ` LOGIN PASSWORD '` + password + `' NOSUPERUSER NOCREATEDB NOCREATEROLE`,
		`CREATE SCHEMA ` + schema + ` AUTHORIZATION ` + owner,
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("two accounts setup: %v", err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_, _ = admin.ExecContext(ctx, `DROP OWNED BY `+owner+`, `+app)
		_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+app)
		_, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS `+owner)
		_ = admin.Close()
	})
	ownerDSN := WithSearchPath(t, base, owner, password, schema)
	appDSN := WithSearchPath(t, base, app, password, schema)
	t.Setenv(security.MigrationDatabaseURLEnv, ownerDSN)
	return Open(t, schema, appDSN, admin, opts...), ownerDSN
}

// Ctx devuelve un contexto con caché de roles por petición.
func (e *Env) Ctx() context.Context { return authz.WithRequestCache(context.Background()) }

// Exec ejecuta SQL con la cuenta del entorno.
func (e *Env) Exec(q string, args ...any) {
	e.T.Helper()
	if _, err := e.DB.Exec(q, args...); err != nil {
		e.T.Fatalf("%s: %v", q, err)
	}
}

// Count devuelve un entero de una consulta.
func (e *Env) Count(q string, args ...any) int {
	e.T.Helper()
	var n int
	if err := e.DB.QueryRow(q, args...).Scan(&n); err != nil {
		e.T.Fatalf("%s: %v", q, err)
	}
	return n
}

// Principal crea una persona (usuario, correo y token) o una cuenta de
// servicio, y devuelve su id.
func (e *Env) Principal(name, kind, engramRole string) string {
	e.T.Helper()
	var id string
	if err := e.DB.QueryRow(`INSERT INTO cloud_principals (kind, display_name, role) VALUES ($1, $2, $3) RETURNING id::text`, kind, name, engramRole).Scan(&id); err != nil {
		e.T.Fatal(err)
	}
	if kind == authz.KindHuman {
		e.Exec(`INSERT INTO cloud_human_users (principal_id, username, email) VALUES ($1::bigint, $2, $3)`, id, name, name+"@rp.pe")
		if engramRole == "admin" {
			// Como el almacén de Engram: pide ser administrador y la asignación de
			// Nexo la deja authz (el primero, sin superadmin, queda superadmin).
			tx, err := e.DB.Begin()
			if err != nil {
				e.T.Fatal(err)
			}
			if _, err := authz.AssignOnCreateTx(context.Background(), tx, id, kind, engramRole, "test"); err != nil {
				_ = tx.Rollback()
				e.T.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				e.T.Fatal(err)
			}
		}
	}
	n := seq.Add(1)
	var tok string
	if err := e.DB.QueryRow(`INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name) VALUES ($1::bigint, $2, $3, $4) RETURNING id::text`,
		id, fmt.Sprintf("egc_kbtest_%d", n), fmt.Sprintf("hash-%s-%d", name, n), "laptop-"+name).Scan(&tok); err != nil {
		e.T.Fatal(err)
	}
	e.tokens[id], e.kinds[id], e.names[id] = tok, kind, name
	return id
}

// Token devuelve el id del token de un principal.
func (e *Env) Token(principalID string) string { return e.tokens[principalID] }

// ID arma la identidad de un principal como la arma el adaptador del servidor.
func (e *Env) ID(principalID string) authz.Identity {
	return authz.Identity{PrincipalID: principalID, TokenID: e.tokens[principalID], Kind: e.kinds[principalID], Label: e.names[principalID],
		Verified: true, Enabled: true, ClientIP: "10.20.30." + principalID, UserAgent: "nexo-kb-test/1.0", ClientClaim: "PC-" + e.names[principalID]}
}

// Team es el equipo de los criterios de aceptación: root (superadmin de
// arranque), adm (admin), aud (auditor), ana (responsable), luis (dev) y ci
// (cuenta de servicio).
type Team struct {
	Root, Adm, Aud, Ana, Luis, CI string
}

// NewTeam crea el equipo y los roles de plataforma.
func (e *Env) NewTeam() Team {
	e.T.Helper()
	tm := Team{
		Root: e.Principal("root", authz.KindHuman, "admin"),
		Adm:  e.Principal("adm", authz.KindHuman, "member"),
		Aud:  e.Principal("aud", authz.KindHuman, "member"),
		Ana:  e.Principal("ana", authz.KindHuman, "member"),
		Luis: e.Principal("luis", authz.KindHuman, "member"),
		CI:   e.Principal("ci-lineamientos", authz.KindServiceAccount, "member"),
	}
	ctx := e.Ctx()
	if _, err := e.Roles.SetPlatformRole(ctx, e.ID(tm.Root), tm.Adm, authz.RoleAdmin); err != nil {
		e.T.Fatalf("admin: %v", err)
	}
	if _, err := e.Roles.SetPlatformRole(e.Ctx(), e.ID(tm.Root), tm.Aud, authz.RoleAuditor); err != nil {
		e.T.Fatalf("auditor: %v", err)
	}
	return tm
}
