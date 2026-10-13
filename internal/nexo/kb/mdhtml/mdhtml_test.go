package mdhtml

import (
	"strings"
	"testing"
)

func TestRenderBlocks(t *testing.T) {
	src := "# Título\n\nUn párrafo con **negrita**, *cursiva* y `código <x>`.\n\n" +
		"## Lista\n\n- uno\n- dos\n  - anidado\n- tres\n\n1. primero\n2. segundo\n\n" +
		"> una cita\n\n```go\nfunc main() { fmt.Println(\"<hola>\") }\n```\n\n" +
		"| Col A | Col B |\n|---|:---:|\n| a1 | b1 |\n| a2 | `b2` |\n\n---\n\n[enlace](https://example.com/a_b_c) y [interno](/dashboard/kb)\n"
	out := Render(src)
	for _, want := range []string{
		"<h1>Título</h1>", "<strong>negrita</strong>", "<em>cursiva</em>", "<code>código &lt;x&gt;</code>",
		"<h2>Lista</h2>", "<ul>", "<li>uno", "<li>anidado", "<ol>", "<li>primero",
		"<blockquote>", "una cita", `<pre><code class="language-go">`, "fmt.Println(&#34;&lt;hola&gt;&#34;)",
		"<table>", "<th>Col A</th>", `<th style="text-align:center">Col B</th>`, "<td>a1</td>", "<code>b2</code>",
		"<hr/>", `<a href="https://example.com/a_b_c" rel="noopener noreferrer">enlace</a>`, `<a href="/dashboard/kb"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q en:\n%s", want, out)
		}
	}
	if strings.Contains(out, "```") || strings.Contains(out, "## ") {
		t.Errorf("quedó Markdown crudo:\n%s", out)
	}
}

func TestRenderEscapesHTMLAndBlocksUnsafeLinks(t *testing.T) {
	cases := []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"[clic](javascript:alert(1))",
		"[clic](JaVaScRiPt:alert(1))",
		"[clic](java\tscript:alert(1))",
		"[clic](data:text/html;base64,PHNjcmlwdD4=)",
		"[clic](vbscript:msgbox)",
		"![x](javascript:alert(1))",
		"[x](https://ok.example/\" onmouseover=\"alert(1))",
		"<javascript:alert(1)>",
		"| a | b |\n|---|---|\n| <iframe src=x> | [y](javascript:alert(2)) |",
		"```\n</code></pre><script>alert(3)</script>\n```",
		"`</code><script>alert(4)</script>`",
		"**<b onclick=alert(5)>x</b>**",
		"<a href=\"javascript:alert(6)\">x</a>",
	}
	for _, c := range cases {
		out := Render(c)
		low := strings.ToLower(out)
		for _, bad := range []string{"<script", "<img", "<iframe", "<b ", "href=\"javascript", "href=\"data:", "href=\"vbscript", "href=\"java", "<a href=\"javascript"} {
			if strings.Contains(low, bad) {
				t.Errorf("entrada %q produjo HTML inseguro (%s):\n%s", c, bad, out)
			}
		}
		if strings.Contains(low, "onmouseover=\"") {
			t.Errorf("atributo inyectado en %q:\n%s", c, out)
		}
	}
	// El texto peligroso se ve como texto.
	if out := Render("<script>alert(1)</script>"); !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("la etiqueta no se escapó como texto: %s", out)
	}
	if out := Render("[clic](javascript:alert(1))"); strings.Contains(out, "<a ") || !strings.Contains(out, "clic") {
		t.Errorf("enlace inseguro: %s", out)
	}
}

func TestSafeURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://x.example":     true,
		"http://x.example":      true,
		"mailto:a@b.example":    true,
		"/dashboard/kb":         true,
		"#ancla":                true,
		"otra/pagina.md":        true,
		"../arriba.md?x=1:2":    true,
		"javascript:alert(1)":   false,
		" javascript:alert(1)":  false,
		"java\nscript:alert(1)": false,
		"data:text/html,x":      false,
		"//evil.example":        false,
		"":                      false,
		"&#106;avascript:x":     false,
	} {
		if got := SafeURL(u); got != want {
			t.Errorf("SafeURL(%q) = %v, want %v", u, got, want)
		}
	}
}
