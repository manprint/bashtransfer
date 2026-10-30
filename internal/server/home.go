package server

import (
	_ "embed"
	"html"
	"net/http"
	"strings"
)

// homeHTML è la pagina di cortesia per i browser. %%BASE%% è la base pubblica dei comandi.
//
//go:embed home.html
var homeHTML string

// wantsHTML riporta se il client è un browser. curl, wget e aria2c inviano Accept */* o nessun
// Accept e ricevono il testo delle istruzioni.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// serveHome risponde con la pagina di cortesia. Gli script e gli stili sono inline: la CSP
// vieta ogni altra risorsa esterna.
func serveHome(w http.ResponseWriter, base string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(strings.ReplaceAll(homeHTML, "%%BASE%%", html.EscapeString(base))))
}
