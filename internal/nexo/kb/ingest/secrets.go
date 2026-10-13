package ingest

import (
	"regexp"
	"strings"
)

// SecretFinding es un patrón evidente de secreto (RN-K4).
type SecretFinding struct {
	Kind string `json:"kind"`
	Line int    `json:"line"`
}

type secretPattern struct {
	kind string
	re   *regexp.Regexp
}

// Solo patrones de alta confianza: un lineamiento puede hablar de
// contraseñas sin contener una. Los marcadores de ejemplo (<...>, ${...},
// ***, xxx) en cadenas de conexión no cuentan.
var secretPatterns = []secretPattern{
	{"private_key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED |PGP )?PRIVATE KEY(?: BLOCK)?-----`)},
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github_token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{60,}\b`)},
	{"slack_token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`)},
	{"openai_or_anthropic_key", regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{32,}\b`)},
	{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"engram_token", regexp.MustCompile(`\begc_[A-Za-z0-9_-]{24,}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{"connection_string_password", regexp.MustCompile(`\b[a-z][a-z0-9+.-]{1,20}://[^\s:/@]+:([^\s@/]{3,})@[^\s/]+`)},
}

// DetectSecrets devuelve los secretos evidentes encontrados (sin el valor).
func DetectSecrets(text string) []SecretFinding {
	var out []SecretFinding
	for i, line := range strings.Split(text, "\n") {
		for _, p := range secretPatterns {
			m := p.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if p.kind == "connection_string_password" && isPlaceholder(m[1]) {
				continue
			}
			out = append(out, SecretFinding{Kind: p.kind, Line: i + 1})
		}
	}
	return out
}

func isPlaceholder(pw string) bool {
	p := strings.ToLower(pw)
	if strings.ContainsAny(p, "<>{}$*") || strings.HasPrefix(p, "xxx") || strings.HasPrefix(p, "%") {
		return true
	}
	switch p {
	case "password", "contrasena", "contraseña", "pass", "secret", "changeme", "usuario", "clave", "tu-clave", "pwd":
		return true
	}
	return false
}
