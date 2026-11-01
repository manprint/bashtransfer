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

// HTML pages for browsers: the home (courtesy page), the download page of a link and the
// error messages. layout.html holds the styles, header and shared scripts; each page defines the
// content block. html/template escapes the base, the file name and the messages.
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

// pageData holds the page data: each page uses only its own fields.
type pageData struct {
	Title   string
	NoIndex bool
	// home
	Base    string
	HTTPS   bool
	Receive []command // receiver commands, with LINK in place of the link
	// download
	Name, Size string
	Commands   []command
	// Away: the sender is not connected but the link stays in grace (BASHT_SENDER_GRACE).
	Away bool
	// message
	Heading, Text string
}

// wantsHTML reports whether the client is a browser. curl, wget and aria2c send Accept */* or no
// Accept and receive the text of the instructions or the file.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// previewAgents are (lowercase) fragments of the User-Agent of programs that open a link to show
// its preview or check it: chat, social, mail, Office. Some do not ask for text/html: they still
// receive the download page, so they do not consume the link. None appears in the User-Agent of
// curl, wget, aria2c or busybox wget.
var previewAgents = []string{
	"bot", "spider", "crawl", "preview", "facebookexternalhit", "whatsapp", "embedly", "iframely",
	"slack", "skype", "discord", "telegram", "mattermost", "ms-office", "microsoft office", "outlook",
}

// isPreviewAgent reports whether the request comes from a preview program (previewAgents).
func isPreviewAgent(r *http.Request) bool {
	ua := strings.ToLower(r.UserAgent())
	for _, a := range previewAgents {
		if strings.Contains(ua, a) {
			return true
		}
	}
	return false
}

// pageCSP forbids any external resource: scripts, styles and the icon are inline. The two %s are the
// destinations allowed for forms (the download page button) and for script requests (the home
// upload): 'self' where needed, otherwise 'none'.
const pageCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action %s; connect-src %s; frame-ancestors 'none'"

// servePage responds with page name and status status. The page is generated before the headers:
// a template error becomes a 500 instead of a truncated page.
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

// serveHome responds with the courtesy page; base is the public base of the commands.
func serveHome(w http.ResponseWriter, base string) {
	servePage(w, http.StatusOK, "home", "'none'", "'self'", pageData{
		Title:   "Bashtransfer",
		Base:    base,
		HTTPS:   strings.HasPrefix(base, "https://"),
		Receive: receiverCommands("LINK"),
	})
}

// landing responds to a browser, or to a preview program, with the download page of the slug: name,
// size and the Download button, a POST form on the same URL. It never claims the slug: opening the
// link, reloading it or pasting it into a chat does not consume it. A link already used, expired or
// unknown responds like the download (410, 404) with the page for the reason.
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

// Message pages that do not match a rejection reason (reason*): link claimed by a download in
// progress, transfer interrupted by the sender before it started.
const (
	pageClaimed = "claimed"
	pageAborted = "aborted"
)

// messages hold the title and text of the error pages for browsers, per reason. For a reason that
// is absent, the page shows the HTTP status and the message as text.
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

// writeError responds with status: to browsers with the page of the reason reason, to other clients
// with msg as text (http.Error).
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

// humanSize renders n for the download page: "1.1 GiB (1,152,499,340 bytes)". -1 is the unknown
// length of an upload from a pipe.
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

// groupDigits separates thousands with commas: 1152499340 becomes "1,152,499,340".
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
