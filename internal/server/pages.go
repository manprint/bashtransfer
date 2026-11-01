package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bashtransfer/internal/relay"
	"bashtransfer/internal/slug"
)

// Pagine HTML per i browser: la home (pagina di cortesia), la pagina di download di un link e i
// messaggi di errore. layout.html contiene stili, intestazione e script comuni; ogni pagina
// definisce il blocco content. html/template applica l'escape a base, nome del file e messaggi.
//
//go:embed pages/*.html
var pageFS embed.FS

var pages = parsePages("home", "download", "message")

func parsePages(names ...string) map[string]*template.Template {
	layout := template.Must(template.ParseFS(pageFS, "pages/layout.html"))
	m := make(map[string]*template.Template, len(names))
	for _, n := range names {
		m[n] = template.Must(template.Must(layout.Clone()).ParseFS(pageFS, "pages/"+n+".html"))
	}
	return m
}

// pageData sono i dati delle pagine: ognuna usa solo i suoi campi.
type pageData struct {
	Title   string
	NoIndex bool
	// home
	Base    string
	HTTPS   bool
	Receive []command // comandi del ricevente, con LINK al posto del link
	// download
	Name, Size string
	Commands   []command
	// Away: il mittente non è connesso ma il link resta in tolleranza (BASHT_SENDER_GRACE).
	Away bool
	// message
	Heading, Text string
}

// wantsHTML riporta se il client è un browser. curl, wget e aria2c inviano Accept */* o nessun
// Accept e ricevono il testo delle istruzioni o il file.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// previewAgents sono frammenti (minuscoli) dello User-Agent dei programmi che aprono un link per
// mostrarne l'anteprima o controllarlo: chat, social, posta, Office. Alcuni non chiedono text/html:
// ricevono comunque la pagina di download, così non consumano il link. Nessuno compare negli
// User-Agent di curl, wget, aria2c e busybox wget.
var previewAgents = []string{
	"bot", "spider", "crawl", "preview", "facebookexternalhit", "whatsapp", "embedly", "iframely",
	"slack", "skype", "discord", "telegram", "mattermost", "ms-office", "microsoft office", "outlook",
}

// isPreviewAgent riporta se la richiesta viene da un programma di anteprima (previewAgents).
func isPreviewAgent(r *http.Request) bool {
	ua := strings.ToLower(r.UserAgent())
	for _, a := range previewAgents {
		if strings.Contains(ua, a) {
			return true
		}
	}
	return false
}

// pageCSP vieta ogni risorsa esterna: script, stili e icona sono inline. I due %s sono le
// destinazioni ammesse per i form (il pulsante della pagina di download) e per le richieste degli
// script (l'upload della home): 'self' dove servono, altrimenti 'none'.
const pageCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action %s; connect-src %s; frame-ancestors 'none'"

// servePage risponde con la pagina name e lo stato status. La pagina è generata prima degli header:
// un errore del template diventa un 500 invece di una pagina troncata.
func servePage(w http.ResponseWriter, status int, name, formAction, connect string, d pageData) {
	var b bytes.Buffer
	if err := pages[name].ExecuteTemplate(&b, "layout", d); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", fmt.Sprintf(pageCSP, formAction, connect))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if d.NoIndex {
		h.Set("X-Robots-Tag", "noindex, nofollow")
	}
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

// serveHome risponde con la pagina di cortesia; base è la base pubblica dei comandi.
func serveHome(w http.ResponseWriter, base string) {
	servePage(w, http.StatusOK, "home", "'none'", "'self'", pageData{
		Title:   "Bashtransfer",
		Base:    base,
		HTTPS:   strings.HasPrefix(base, "https://"),
		Receive: receiverCommands("LINK"),
	})
}

// landing risponde a un browser, o a un programma di anteprima, con la pagina di download dello
// slug: nome, dimensione e il pulsante Download, un form POST sulla stessa URL. Non reclama mai lo
// slug: aprire il link, ricaricarlo o incollarlo in una chat non lo consuma. Un link già usato,
// scaduto o sconosciuto risponde come il download (410, 404) con la pagina del motivo.
func (h *handler) landing(w http.ResponseWriter, r *http.Request, id string) {
	if !slug.Valid(id) {
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	}
	s, err := h.reg.Peek(id)
	switch {
	case errors.Is(err, relay.ErrNotFound):
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	case errors.Is(err, relay.ErrClaimed):
		h.logReject(r, slog.LevelInfo, http.StatusGone, reasonGone)
		writeError(w, r, http.StatusGone, pageClaimed, "gone")
		return
	case err != nil:
		h.reject(w, r, slog.LevelInfo, http.StatusGone, goneReasonOf(err), "gone")
		return
	}
	d := pageData{Title: "Download · Bashtransfer", NoIndex: true, Name: s.Name, Size: humanSize(s.Length)}
	d.Away = s.Away()
	if base, ok := h.baseURL(r); ok {
		d.Commands = receiverCommands(base + "/" + id)
	}
	servePage(w, http.StatusOK, "download", "'self'", "'self'", d)
}

func (h *handler) linkStatus(w http.ResponseWriter, r *http.Request, id string) {
	state, message, code := "ready", "", http.StatusOK
	var ready *bool
	var readyTTL int64
	var away, claimed, started bool
	var err error
	if r.Header.Get("X-Bashtransfer-Readiness") == "1" {
		var peer relay.PeerStatus
		peer, err = h.reg.ObserveReceiver(id)
		ready, away, claimed, started = &peer.Ready, peer.Away, peer.Claimed, peer.Started
		readyTTL = int64(relay.ReceiverPresenceTimeout / time.Second)
	} else {
		var s *relay.Session
		s, err = h.reg.Peek(id)
		if err == nil {
			away = s.Away()
		}
	}
	switch {
	case !slug.Valid(id), errors.Is(err, relay.ErrNotFound):
		state, code = reasonNotFound, http.StatusNotFound
	case errors.Is(err, relay.ErrClaimed):
		state, code = pageClaimed, http.StatusGone
	case err != nil:
		state, code = goneReasonOf(err), http.StatusGone
	case claimed && started:
		state = "active"
	case claimed:
		state = "starting"
	case away:
		state = "away"
	}
	if m, ok := messages[state]; ok {
		message = m.heading + ". " + m.text
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		State    string `json:"state"`
		Message  string `json:"message,omitempty"`
		Ready    *bool  `json:"ready,omitempty"`
		ReadyTTL int64  `json:"ready_ttl,omitempty"`
	}{state, message, ready, readyTTL})
}

// Pagine dei messaggi che non corrispondono a un motivo di rifiuto (reason*): link reclamato da un
// download in corso, trasferimento interrotto dal mittente prima dell'inizio.
const (
	pageClaimed = "claimed"
	pageAborted = "aborted"
)

// messages sono titolo e testo delle pagine di errore per i browser, per motivo. Per un motivo
// assente la pagina mostra lo stato HTTP e il messaggio in testo.
var messages = map[string]struct{ heading, text string }{
	reasonNotFound: {"Link not found",
		"This link does not exist. Check that you copied the whole link; if you did, ask the sender to send the file again."},
	reasonGone: {"Link already used or expired",
		"Each link works for one download only and expires if nobody uses it in time. Ask the sender to send the file again."},
	reasonGoneSender: {"Sender stopped",
		"The sender closed the page, cancelled or lost the connection before this link was used. Ask the sender to keep the page open and send the file again."},
	reasonGoneExpired: {"Link expired",
		"Nobody started the download in time. Ask the sender to send the file again."},
	pageClaimed: {"Download already started",
		"This link is already being downloaded, perhaps from another tab or window: check your browser's downloads. Each link works once; if it was not you, ask the sender to send the file again."},
	pageAborted: {"Transfer interrupted",
		"The sender stopped before the download could start. Ask the sender to send the file again."},
	reasonRangeUnsupported: {"Resume not supported",
		"This link can only be downloaded from the beginning, in one go."},
}

// writeError risponde con status: ai browser con la pagina del motivo reason, agli altri client con
// msg in testo (http.Error).
func writeError(w http.ResponseWriter, r *http.Request, status int, reason, msg string) {
	if !wantsHTML(r) {
		http.Error(w, msg, status)
		return
	}
	m, ok := messages[reason]
	if !ok {
		m.heading, m.text = http.StatusText(status), msg
	}
	servePage(w, status, "message", "'none'", "'none'", pageData{
		Title: m.heading + " · Bashtransfer", NoIndex: true, Heading: m.heading, Text: m.text,
	})
}

// humanSize rende n per la pagina di download: "1.1 GiB (1,152,499,340 bytes)". -1 è la lunghezza
// ignota di un invio da pipe.
func humanSize(n int64) string {
	const unit = 1024
	switch {
	case n < 0:
		return "unknown (streamed by the sender)"
	case n == 1:
		return "1 byte"
	case n < unit:
		return strconv.FormatInt(n, 10) + " bytes"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB (%s bytes)", float64(n)/float64(div), "KMGTPE"[exp], groupDigits(n))
}

// groupDigits separa le migliaia con la virgola: 1152499340 diventa "1,152,499,340".
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
