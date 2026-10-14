package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestParseRequireDBTLS(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "false": false, "0": false, "OFF": false, "true": true, "1": true, " Yes ": true} {
		got, err := ParseRequireDBTLS(raw)
		if err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", raw, got, err, want)
		}
	}
	if _, err := ParseRequireDBTLS("quizas"); err == nil || !strings.Contains(err.Error(), DBRequireTLSEnv) {
		t.Fatalf("valor inválido: %v", err)
	}
}

func TestInspectDSN(t *testing.T) {
	t.Setenv("PGSSLMODE", "")
	cases := []struct {
		name, dsn, mode         string
		local, verified, hasErr bool
	}{
		{"url verify-full remoto", "postgres://u:p@db.interno:5432/x?sslmode=verify-full&sslrootcert=", "verify-full", false, true, false},
		{"url disable localhost", "postgres://u:p@localhost:5432/x?sslmode=disable", "disable", true, false, false},
		{"url loopback", "postgres://u:p@127.0.0.1/x?sslmode=disable", "disable", true, false, false},
		{"url loopback v6", "postgres://u:p@[::1]:5432/x?sslmode=disable", "disable", true, false, false},
		{"sin sslmode = prefer", "postgres://u:p@db.interno/x", "prefer", false, false, false},
		{"require remoto no verifica", "postgres://u:p@10.0.0.5/x?sslmode=require", "require", false, false, false},
		{"clave=valor", "host=db.interno port=5432 user=u password='a b' dbname=x sslmode=verify-ca", "verify-ca", false, true, false},
		{"clave=valor socket", "host=/var/run/postgresql user=u dbname=x sslmode=disable", "disable", true, false, false},
		{"varios hosts, uno remoto", "postgres://u:p@localhost:5432,db.interno:5432/x?sslmode=disable", "disable", false, false, false},
		{"modo desconocido", "postgres://u:p@localhost/x?sslmode=casi", "", false, false, true},
		{"dsn roto", "postgres://u:p@%zz/x", "", false, false, true},
	}
	for _, c := range cases {
		got, err := InspectDSN(c.dsn)
		if (err != nil) != c.hasErr {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		if c.hasErr {
			if err != nil && strings.Contains(err.Error(), "p@") {
				t.Errorf("%s: el error no debe traer la contraseña: %v", c.name, err)
			}
			continue
		}
		if got.SSLMode != c.mode || got.Local != c.local || got.Verified != c.verified {
			t.Errorf("%s: %+v; want mode=%s local=%v verified=%v", c.name, got, c.mode, c.local, c.verified)
		}
	}
	t.Setenv("PGSSLMODE", "verify-full")
	if got, err := InspectDSN("postgres://u:p@db.interno/x"); err != nil || got.SSLMode != "verify-full" || !got.Verified {
		t.Errorf("PGSSLMODE: %+v, %v", got, err)
	}
}

// Regresión (revisión adversarial): la decisión sale de la configuración
// efectiva de pgx, no de una lectura propia de la cadena. Un sslmode repetido
// gana el último (como en pgx), un archivo de servicio tiene prioridad sobre
// PGSSLMODE y require con sslrootcert equivale a verify-ca.
func TestInspectDSNUsesEffectivePgxConfig(t *testing.T) {
	t.Setenv("PGSSLMODE", "")
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	dir := t.TempDir()
	_, _, caPEM := newTestCA(t, "CA de prueba")
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, dsn, mode string
		verified        bool
	}{
		{"repetido: gana el último (require)", "host=db.interno user=u dbname=x sslmode=verify-full sslmode=require", "require", false},
		{"repetido: gana el último (verify-full)", "host=db.interno user=u dbname=x sslmode=require sslmode=verify-full", "verify-full", true},
		{"allow", "host=db.interno user=u dbname=x sslmode=allow", "allow", false},
		{"prefer por defecto", "host=db.interno user=u dbname=x", "prefer", false},
		{"require con sslrootcert = verify-ca", "host=db.interno user=u dbname=x sslmode=require sslrootcert='" + caPath + "'", "verify-ca", true},
		{"varios hosts verify-full", "postgres://u:p@db1.interno:5432,db2.interno:5432/x?sslmode=verify-full", "verify-full", true},
	}
	for _, c := range cases {
		got, err := InspectDSN(c.dsn)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.SSLMode != c.mode || got.Verified != c.verified {
			t.Errorf("%s: %+v; want mode=%s verified=%v", c.name, got, c.mode, c.verified)
		}
		if err := got.Enforce("ENGRAM_DATABASE_URL", true); (err == nil) != c.verified {
			t.Errorf("%s: Enforce con REQUIRE_TLS = %v; want aceptar=%v", c.name, err, c.verified)
		}
	}

	// Archivo de servicio: en pgx tiene prioridad sobre PGSSLMODE.
	svc := filepath.Join(dir, "pg_service.conf")
	if err := os.WriteFile(svc, []byte("[nexo]\nhost=db.interno\nsslmode=require\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSERVICEFILE", svc)
	t.Setenv("PGSSLMODE", "verify-full")
	got, err := InspectDSN("service=nexo user=u dbname=x")
	if err != nil {
		t.Fatalf("servicio: %v", err)
	}
	if got.SSLMode != "require" || got.Verified {
		t.Errorf("servicio sobre PGSSLMODE: %+v; want require sin verificar", got)
	}
}

func TestDBTLSEnforceAndWarning(t *testing.T) {
	remote := DBTLS{SSLMode: "disable"}
	err := remote.Enforce("ENGRAM_DATABASE_URL", true)
	if err == nil || !strings.Contains(err.Error(), DBRequireTLSEnv) || !strings.Contains(err.Error(), "sslmode=disable") || !strings.Contains(err.Error(), "ENGRAM_DATABASE_URL") {
		t.Fatalf("el error debe decir por qué: %v", err)
	}
	if err := remote.Enforce("X", false); err != nil {
		t.Fatalf("sin exigir no falla: %v", err)
	}
	// Exigir vale también para una base local: es una opción explícita.
	if err := (DBTLS{SSLMode: "disable", Local: true}).Enforce("X", true); err == nil {
		t.Fatal("NEXO_DB_REQUIRE_TLS=true exige TLS verificado aun en una base local")
	}
	if err := (DBTLS{SSLMode: "verify-ca", Verified: true}).Enforce("X", true); err != nil {
		t.Fatalf("verify-ca cumple: %v", err)
	}
	if w := remote.Warning("ENGRAM_DATABASE_URL"); !strings.Contains(w, "ADVERTENCIA") || !strings.Contains(w, "verify-full") {
		t.Fatalf("aviso: %q", w)
	}
	if w := (DBTLS{SSLMode: "disable", Local: true}).Warning("X"); w != "" {
		t.Fatalf("base local sin aviso: %q", w)
	}
	if w := (DBTLS{SSLMode: "verify-full", Verified: true}).Warning("X"); w != "" {
		t.Fatalf("TLS verificado sin aviso: %q", w)
	}
}

// ─── verify-full contra un servidor TLS de prueba ────────────────────────────

type testPKI struct {
	dir     string
	caPEM   []byte
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	caPath  string
	otherCA string // ruta de otra CA que no firmó nada
	serial  int64
}

func newTestCA(t *testing.T, cn string) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func (p *testPKI) serverCert(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p.serial++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(100 + p.serial), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakePostgres acepta conexiones, contesta 'S' a la petición SSL y hace el
// apretón de manos TLS con cert. Devuelve cuando el cliente cierra. Nunca
// habla el protocolo de Postgres: basta para ver si el cliente acepta o
// rechaza el certificado.
func fakePostgres(t *testing.T, cert tls.Certificate) (addr string, handshakes chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	handshakes = make(chan error, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 8)
				if _, err := c.Read(buf); err != nil {
					handshakes <- err
					return
				}
				if _, err := c.Write([]byte{'S'}); err != nil {
					handshakes <- err
					return
				}
				srv := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				handshakes <- srv.Handshake()
			}()
		}
	}()
	return ln.Addr().String(), handshakes
}

func connectVerifyFull(t *testing.T, addr, host, mode, caPath string) error {
	t.Helper()
	_, port, _ := net.SplitHostPort(addr)
	dsn := fmt.Sprintf("postgres://u:secreto-de-prueba@%s:%s/x?sslmode=%s&sslrootcert=%s&connect_timeout=5", host, port, mode, caPath)
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	// El nombre del host se resuelve a 127.0.0.1: así se prueba el nombre del
	// certificado con la IP real del servidor de prueba.
	cfg.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	cfg.DialFunc = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if conn != nil {
		_ = conn.Close(ctx)
	}
	return err
}

// SG-05: verify-full acepta el certificado de la CA con el nombre correcto y
// rechaza un nombre equivocado o una CA distinta. El servidor de prueba solo
// completa el apretón de manos TLS; lo que sigue falla en el protocolo de
// Postgres, que no es lo que se mide.
func TestVerifyFullTLSAgainstTestServer(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, caPEM := newTestCA(t, "nexo-test-ca")
	pki := &testPKI{dir: dir, caCert: caCert, caKey: caKey, caPEM: caPEM, caPath: filepath.Join(dir, "ca.crt")}
	if err := os.WriteFile(pki.caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, otherPEM := newTestCA(t, "otra-ca")
	pki.otherCA = filepath.Join(dir, "otra-ca.crt")
	if err := os.WriteFile(pki.otherCA, otherPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	addr, hs := fakePostgres(t, pki.serverCert(t, "db.nexo.test"))

	// Nombre y CA correctos: el apretón TLS termina bien (el error posterior
	// es del protocolo, no del certificado).
	err := connectVerifyFull(t, addr, "db.nexo.test", "verify-full", pki.caPath)
	if err == nil {
		t.Fatal("el servidor de prueba no habla Postgres: se esperaba un error de protocolo")
	}
	if e := <-hs; e != nil {
		t.Fatalf("apretón TLS con nombre y CA correctos: %v", e)
	}
	if strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "x509") {
		t.Fatalf("el certificado correcto no debe rechazarse: %v", err)
	}

	// Nombre equivocado: verify-full rechaza.
	err = connectVerifyFull(t, addr, "otro.nexo.test", "verify-full", pki.caPath)
	if err == nil || !strings.Contains(err.Error(), "x509") {
		t.Fatalf("verify-full debe rechazar un certificado con otro nombre: %v", err)
	}
	<-hs

	// Otra CA: verify-full rechaza aunque el nombre coincida.
	err = connectVerifyFull(t, addr, "db.nexo.test", "verify-full", pki.otherCA)
	if err == nil || !strings.Contains(err.Error(), "x509") {
		t.Fatalf("verify-full debe rechazar un certificado de otra CA: %v", err)
	}
	<-hs

	// verify-ca no mira el nombre (acepta el nombre equivocado) pero sí la CA.
	err = connectVerifyFull(t, addr, "otro.nexo.test", "verify-ca", pki.caPath)
	if err != nil && strings.Contains(err.Error(), "x509") {
		t.Fatalf("verify-ca con la CA correcta no mira el nombre: %v", err)
	}
	<-hs
	err = connectVerifyFull(t, addr, "db.nexo.test", "verify-ca", pki.otherCA)
	if err == nil || !strings.Contains(err.Error(), "x509") {
		t.Fatalf("verify-ca debe rechazar una CA distinta: %v", err)
	}
	<-hs

	// Ningún error trae la contraseña.
	if strings.Contains(err.Error(), "secreto-de-prueba") {
		t.Fatalf("el error no debe traer la contraseña: %v", err)
	}
}
