// Package server contiene i gestori HTTP del relay: upload con comandi immediati e download in streaming.
package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"bashtransfer/internal/relay"
	"bashtransfer/internal/slug"
)

// Options configura il gestore HTTP.
type Options struct {
	// PublicURL, se non vuoto, è la base dei comandi generati (D12).
	PublicURL string
	// Authorize, se non nil, decide se un PUT è autorizzato (D9, collegato in 2.1).
	Authorize func(*http.Request) bool
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
	seg, ok := singleSegment(r.URL.Path)
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

// singleSegment accetta "/" (segmento vuoto) e "/<seg>"; altri path sono 404.
func singleSegment(p string) (string, bool) {
	if p == "/" {
		return "", true
	}
	seg := strings.TrimPrefix(p, "/")
	if seg == "" || strings.Contains(seg, "/") {
		return "", false
	}
	return seg, true
}

// nameFromSegment usa il segmento del path come nome file; default "download" (D5).
// La sanitizzazione completa è in 2.2.
func nameFromSegment(seg string) string {
	if seg == "" {
		return "download"
	}
	return seg
}

// baseURL costruisce la base dei comandi (D12).
func (h *handler) baseURL(r *http.Request) string {
	if h.opts.PublicURL != "" {
		return strings.TrimSuffix(h.opts.PublicURL, "/")
	}
	scheme := "http"
	switch xf := r.Header.Get("X-Forwarded-Proto"); {
	case xf == "http" || xf == "https":
		scheme = xf
	case r.TLS != nil:
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *handler) usage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "bashtransfer: relay di file senza salvataggio su disco.\n"+
		"Invio:     curl -T FILE %s\n"+
		"Ricezione: esegui il comando curl o wget ricevuto dal mittente.\n", h.baseURL(r))
}

// upload apre la sessione, scrive subito i comandi e legge il body solo quando il ricevente si aggancia.
func (h *handler) upload(w http.ResponseWriter, r *http.Request, seg string) {
	if h.opts.Authorize != nil && !h.opts.Authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rc := http.NewResponseController(w)
	if err := rc.EnableFullDuplex(); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	name := nameFromSegment(seg)
	s, err := h.reg.Create(name, r.ContentLength)
	if err != nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	ok := false
	defer func() {
		if !ok {
			s.Abort(relay.ErrSenderGone)
		}
		h.reg.Finish(s)
	}()

	// curl invia "Expect: 100-continue" per upload grandi. Se la risposta finale arriva prima
	// del 100 e prima della lettura del body, Go chiude il body (ErrBodyReadAfterClose).
	// Il 100 viene quindi inviato esplicitamente, prima dei comandi.
	if expectsContinue(r) {
		w.WriteHeader(http.StatusContinue)
	}

	base := h.baseURL(r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "curl -o \"%s\" \"%s/%s\"\nwget -O \"%s\" \"%s/%s\"\n", name, base, s.Slug, name, base, s.Slug)
	_ = rc.Flush()

	select {
	case rcv := <-s.Attached():
		rr := rcv.(*responseReceiver)
		rr.start(name, s.Length)
		n, err := h.copier.Copy(rr, r.Body)
		if err == nil && s.Length >= 0 && n != s.Length {
			err = relay.ErrLengthMismatch
		}
		if err != nil {
			s.Abort(err)
			panic(http.ErrAbortHandler)
		}
		ok = true
	case <-s.Expired():
		panic(http.ErrAbortHandler)
	case <-r.Context().Done():
	}
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
	rcv := newResponseReceiver(w)
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
	if s.Err() != nil {
		if rcv.started {
			// Interrompe la risposta senza chunk finale: il client vede un errore (D8).
			panic(http.ErrAbortHandler)
		}
		http.Error(w, "transfer aborted", http.StatusServiceUnavailable)
	}
}
