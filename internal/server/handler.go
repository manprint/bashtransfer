// Package server contiene i gestori HTTP del relay: upload con comandi immediati e download in streaming.
package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bashtransfer/internal/relay"
	"bashtransfer/internal/slug"
)

// drainTimeout limita il tempo che Go dedica a svuotare il body non letto di un upload terminato
// male (401, 503, scadenza, ricevente caduto). Senza limite un mittente fermo terrebbe aperta la
// connessione all'infinito: l'handler è già finito ma net/http legge il body prima di chiudere.
const drainTimeout = 2 * time.Second

// Options configura il gestore HTTP.
type Options struct {
	// PublicURL, se non vuoto, è la base dei comandi generati (D12).
	PublicURL string
	// Authorize, se non nil, decide se un PUT è autorizzato (D9).
	Authorize func(*http.Request) bool
	// IdleTimeout, se > 0, interrompe il relay se il mittente è fermo così a lungo (D15)
	// o se una singola scrittura verso il ricevente non termina entro questo tempo.
	IdleTimeout time.Duration
	// Logger, se non nil, riceve l'esito di ogni trasferimento. Non vi compaiono slug né nomi.
	Logger *slog.Logger
}

type handler struct {
	reg    *relay.Registry
	copier *relay.Copier
	opts   Options
}

// New restituisce il gestore HTTP del relay.
func New(reg *relay.Registry, copier *relay.Copier, opts Options) http.Handler {
	return &handler{reg: reg, copier: copier, opts: opts}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Il path ancora codificato: un %2F nel nome resta parte del segmento invece di diventare
	// un separatore (SanitizeName ne tiene poi l'ultima parte).
	seg, ok := singleSegment(r.URL.EscapedPath())
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if seg == "" {
			h.usage(w, r)
			return
		}
		h.download(w, r, seg)
	case http.MethodPut:
		h.upload(w, r, seg)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// singleSegment accetta "/" (segmento vuoto) e "/<seg>" con p ancora codificato; altri path sono
// 404. Il segmento restituito è decodificato.
func singleSegment(p string) (string, bool) {
	if p == "/" {
		return "", true
	}
	seg := strings.TrimPrefix(p, "/")
	if seg == "" || strings.Contains(seg, "/") {
		return "", false
	}
	seg, err := url.PathUnescape(seg)
	if err != nil {
		return "", false
	}
	return seg, true
}

// unsafeHostChars sono i caratteri che la shell interpreta tra virgolette doppie, più spazi e
// fine riga. net/http accetta `$`, `!` e parentesi in Host: senza questo controllo un Host come
// `x$(cmd)` finirebbe nella riga di comando stampata.
const unsafeHostChars = "\"`$\\ !\t\r\n"

// baseURL costruisce la base dei comandi (D12). ok è false se Host è vuoto o contiene caratteri
// che la shell interpreterebbe nella riga stampata.
func (h *handler) baseURL(r *http.Request) (base string, ok bool) {
	if h.opts.PublicURL != "" {
		return strings.TrimSuffix(h.opts.PublicURL, "/"), true
	}
	if r.Host == "" || strings.ContainsAny(r.Host, unsafeHostChars) {
		return "", false
	}
	scheme := "http"
	switch xf := r.Header.Get("X-Forwarded-Proto"); {
	case xf == "http" || xf == "https":
		scheme = xf
	case r.TLS != nil:
		scheme = "https"
	}
	return scheme + "://" + r.Host, true
}

func (h *handler) usage(w http.ResponseWriter, r *http.Request) {
	base, ok := h.baseURL(r)
	if !ok {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "bashtransfer: relay di file senza salvataggio su disco.\n"+
		"Invio:     curl -T FILE %s\n"+
		"Ricezione: esegui il comando curl o wget ricevuto dal mittente.\n", base)
}

// upload apre la sessione, scrive subito i comandi e legge il body solo quando il ricevente si aggancia.
func (h *handler) upload(w http.ResponseWriter, r *http.Request, seg string) {
	rc := http.NewResponseController(w)
	ok := false
	drain := drainTimeout
	defer func() {
		if !ok {
			_ = rc.SetReadDeadline(time.Now().Add(drain))
		}
	}()
	if h.opts.Authorize != nil && !h.opts.Authorize(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="bashtransfer"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	base, baseOK := h.baseURL(r)
	if !baseOK {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	if err := rc.EnableFullDuplex(); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	name := SanitizeName(seg)
	s, err := h.reg.Create(name, r.ContentLength)
	if err != nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	start := time.Now()
	var sent int64
	outcome := "sender_gone"
	defer func() {
		if !ok {
			s.Abort(relay.ErrSenderGone)
		}
		h.reg.Finish(s)
		err := s.Err()
		if ok {
			// Copia completa: un ErrReceiverGone registrato dopo l'ultimo byte (il client chiude
			// appena ha letto Content-Length byte) non è un errore del trasferimento.
			err = nil
		}
		h.logTransfer(outcome, sent, time.Since(start), err)
	}()

	// curl invia "Expect: 100-continue" per upload grandi. Se la risposta finale arriva prima
	// del 100 e prima della lettura del body, Go chiude il body (ErrBodyReadAfterClose).
	// Il 100 viene quindi inviato esplicitamente, prima dei comandi.
	if expectsContinue(r) {
		w.WriteHeader(http.StatusContinue)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "curl -o \"%s\" \"%s/%s\"\nwget -O \"%s\" \"%s/%s\"\n", name, base, s.Slug, name, base, s.Slug)
	_ = rc.Flush()

	// Con il body non letto net/http non vede la caduta del mittente: la connessione viene
	// controllata a intervalli finché il ricevente non si aggancia.
	conn := connFrom(r.Context())
	var peerCheck <-chan time.Time
	if conn != nil && peerCheckSupported {
		t := time.NewTicker(peerCheckInterval)
		defer t.Stop()
		peerCheck = t.C
	}

	for {
		select {
		case rcv := <-s.Attached():
			rr := rcv.(*responseReceiver)
			rr.start(name, s.Length)
			var body io.Reader = r.Body
			if h.opts.IdleTimeout > 0 {
				body = relay.NewIdleReader(r.Body, h.opts.IdleTimeout, rc.SetReadDeadline)
			}
			n, err := h.copier.Copy(rr, body)
			sent = n
			if err == nil && s.Length >= 0 && n != s.Length {
				err = relay.ErrLengthMismatch
			}
			if err != nil {
				outcome = "aborted"
				s.Abort(err)
				drain = 0
				panic(http.ErrAbortHandler)
			}
			outcome = "ok"
			ok = true
			return
		case <-s.Expired():
			outcome = "expired"
			// Prima dell'Abort differito con ErrSenderGone: il log deve dire scadenza, non mittente caduto.
			s.Abort(relay.ErrExpired)
			drain = 0
			panic(http.ErrAbortHandler)
		case <-r.Context().Done():
			return
		case <-peerCheck:
			if peerClosed(conn) {
				// outcome resta sender_gone; il defer registra ErrSenderGone e libera lo slug.
				drain = 0
				panic(http.ErrAbortHandler)
			}
		}
	}
}

func (h *handler) logTransfer(outcome string, bytes int64, d time.Duration, err error) {
	if h.opts.Logger == nil {
		return
	}
	attrs := []any{"outcome", outcome, "bytes", bytes, "duration", d.Round(time.Millisecond)}
	if err != nil {
		attrs = append(attrs, "err", err)
		h.opts.Logger.Warn("trasferimento", attrs...)
		return
	}
	h.opts.Logger.Info("trasferimento", attrs...)
}

// expectsContinue riporta se il client ha chiesto 100 Continue con un body non vuoto.
func expectsContinue(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Expect"), "100-continue") && r.ContentLength != 0
}

// download reclama lo slug e attende la fine del trasferimento; il relay lo scrive il mittente.
func (h *handler) download(w http.ResponseWriter, r *http.Request, id string) {
	if !slug.Valid(id) {
		http.NotFound(w, r)
		return
	}
	rcv := newResponseReceiver(w, h.opts.IdleTimeout)
	s, err := h.reg.Claim(id, rcv)
	switch {
	case errors.Is(err, relay.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, relay.ErrClaimed), errors.Is(err, relay.ErrGone):
		http.Error(w, "gone", http.StatusGone)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	finished := s.Finished()
	ctxDone := r.Context().Done()
	for waiting := true; waiting; {
		select {
		case <-finished:
			waiting = false
		case <-ctxDone:
			s.Abort(relay.ErrReceiverGone)
			ctxDone = nil
		}
	}
	rcv.clearWriteDeadline()
	if s.Err() != nil {
		if rcv.started {
			// Interrompe la risposta senza chunk finale: il client vede un errore (D8).
			panic(http.ErrAbortHandler)
		}
		http.Error(w, "transfer aborted", http.StatusServiceUnavailable)
	}
}
