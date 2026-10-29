// Package server contiene i gestori HTTP del relay: upload con comandi immediati e download in streaming.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
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
	// Logger, se non nil, riceve gli eventi del relay (README, "Log"). Non vi compaiono mai slug,
	// nomi di file, path o credenziali: una sessione è identificata dal suo ID casuale.
	Logger *slog.Logger
}

type handler struct {
	reg    *relay.Registry
	copier *relay.Copier
	opts   Options
}

// New restituisce il gestore HTTP del relay.
func New(reg *relay.Registry, copier *relay.Copier, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &handler{reg: reg, copier: copier, opts: opts}
}

// Motivi dei rifiuti registrati nei log (campo reason di request_rejected).
const (
	reasonUnauthorized     = "unauthorized"
	reasonInvalidHost      = "invalid_host"
	reasonCapacity         = "capacity"
	reasonInternal         = "internal"
	reasonMethod           = "method_not_allowed"
	reasonNotFound         = "not_found"
	reasonGone             = "gone"
	reasonRangeUnsupported = "range_not_supported"
)

// reject registra il rifiuto di una richiesta e risponde con status e msg. Il path non compare
// nel log: contiene slug o nome del file.
func (h *handler) reject(w http.ResponseWriter, r *http.Request, level slog.Level, status int, reason, msg string) {
	h.opts.Logger.LogAttrs(r.Context(), level, "request_rejected",
		slog.String("method", r.Method), slog.Int("status", status), slog.String("reason", reason))
	http.Error(w, msg, status)
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Il path ancora codificato: un %2F nel nome resta parte del segmento invece di diventare
	// un separatore (SanitizeName ne tiene poi l'ultima parte).
	seg, ok := singleSegment(r.URL.EscapedPath())
	if !ok {
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
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
		h.reject(w, r, slog.LevelDebug, http.StatusMethodNotAllowed, reasonMethod, "method not allowed")
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
		h.reject(w, r, slog.LevelInfo, http.StatusBadRequest, reasonInvalidHost, "invalid host")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "bashtransfer: relay di file senza salvataggio su disco.\n"+
		"Invio:     curl -T FILE %s\n"+
		"Ricezione: esegui il comando curl, wget o aria2c ricevuto dal mittente.\n", base)
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
		h.reject(w, r, slog.LevelWarn, http.StatusUnauthorized, reasonUnauthorized, "unauthorized")
		return
	}
	base, baseOK := h.baseURL(r)
	if !baseOK {
		h.reject(w, r, slog.LevelInfo, http.StatusBadRequest, reasonInvalidHost, "invalid host")
		return
	}
	if err := rc.EnableFullDuplex(); err != nil {
		h.opts.Logger.Error("full_duplex_unavailable", "err", err)
		h.reject(w, r, slog.LevelError, http.StatusInternalServerError, reasonInternal, "internal error")
		return
	}
	name := SanitizeName(seg)
	s, err := h.reg.Create(name, r.ContentLength)
	if err != nil {
		level, reason := slog.LevelWarn, reasonCapacity
		if !errors.Is(err, relay.ErrCapacity) {
			level, reason = slog.LevelError, reasonInternal
			h.opts.Logger.Error("session_create_failed", "err", err)
		}
		h.reject(w, r, level, http.StatusServiceUnavailable, reason, "service unavailable")
		return
	}
	h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "upload_accepted",
		slog.String("id", s.ID), slog.Int64("length", s.Length))
	tr := transferLog{session: s}
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
		tr.log(h.opts.Logger, err)
	}()

	// curl invia "Expect: 100-continue" per upload grandi. Se la risposta finale arriva prima
	// del 100 e prima della lettura del body, Go chiude il body (ErrBodyReadAfterClose).
	// Il 100 viene quindi inviato esplicitamente, prima dei comandi.
	if expectsContinue(r) {
		w.WriteHeader(http.StatusContinue)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// La connessione del mittente non viene riusata: Stop può lasciarvi una scadenza di lettura
	// passata, e un upload resta comunque l'unica richiesta di curl -T.
	w.Header().Set("Connection", "close")
	io.WriteString(w, commands(name, base+"/"+s.Slug))
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
			tr.attached = time.Now()
			h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "receiver_attached",
				slog.String("id", s.ID), seconds("wait_s", tr.attached.Sub(s.Created)))
			rr.start(name, s.Length)
			body := relay.NewIdleReader(r.Body, h.opts.IdleTimeout, rc.SetReadDeadline)
			n, err := h.copyUntilAbort(s, rr, body)
			tr.bytes = n
			if err == nil && s.Length >= 0 && n != s.Length {
				err = relay.ErrLengthMismatch
			}
			if err != nil {
				tr.outcome = outcomeAborted
				s.Abort(err)
				drain = 0
				panic(http.ErrAbortHandler)
			}
			tr.outcome = outcomeOK
			ok = true
			return
		case <-s.Expired():
			tr.outcome = outcomeExpired
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

// copyUntilAbort copia il body verso il ricevente. Se la sessione viene interrotta durante la
// copia (ricevente caduto), una lettura del mittente in attesa di dati viene sbloccata subito
// invece di attendere il prossimo byte o BASHT_IDLE_TIMEOUT.
func (h *handler) copyUntilAbort(s *relay.Session, rr *responseReceiver, body *relay.IdleReader) (int64, error) {
	copied := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-s.Aborted():
			body.Stop()
		case <-copied:
		}
	}()
	n, err := h.copier.Copy(rr, body)
	close(copied)
	<-watched
	return n, err
}

// commands restituisce le righe di download stampate al mittente. aria2c usa una sola
// connessione: il relay è un flusso unico e non serve intervalli di byte.
func commands(name, u string) string {
	return fmt.Sprintf("curl -o \"%s\" \"%s\"\nwget -O \"%s\" \"%s\"\naria2c -x1 -s1 -o \"%s\" \"%s\"\n",
		name, u, name, u, name, u)
}

// Esiti di un trasferimento (campo outcome dell'evento transfer).
const (
	outcomeOK         = "ok"          // tutti i byte consegnati al ricevente
	outcomeAborted    = "aborted"     // relay iniziato e interrotto (mittente, ricevente, inattività)
	outcomeExpired    = "expired"     // nessun ricevente entro BASHT_WAIT_TIMEOUT (o arresto)
	outcomeSenderGone = "sender_gone" // il mittente è caduto prima che un ricevente si agganciasse
)

// transferLog raccoglie i dati dell'evento transfer, registrato una volta per sessione alla fine
// dell'upload.
type transferLog struct {
	session  *relay.Session
	outcome  string
	attached time.Time // zero se nessun ricevente si è agganciato
	bytes    int64
}

// log registra l'esito: Info per ok, scadenza e mittente caduto in attesa (comportamenti normali
// degli utenti), Warn per un relay interrotto a metà.
func (t *transferLog) log(l *slog.Logger, err error) {
	if t.outcome == "" {
		t.outcome = outcomeSenderGone
	}
	now := time.Now()
	s := t.session
	attrs := []slog.Attr{
		slog.String("id", s.ID),
		slog.String("outcome", t.outcome),
		slog.Int64("bytes", t.bytes),
	}
	if s.Length >= 0 {
		attrs = append(attrs, slog.Int64("length", s.Length))
	}
	if t.attached.IsZero() {
		attrs = append(attrs, seconds("wait_s", now.Sub(s.Created)))
	} else {
		d := now.Sub(t.attached)
		attrs = append(attrs, seconds("wait_s", t.attached.Sub(s.Created)), seconds("transfer_s", d))
		if d > 0 {
			attrs = append(attrs, slog.Float64("rate_mbs", math.Round(float64(t.bytes)/d.Seconds()/1e4)/100))
		}
	}
	level := slog.LevelInfo
	if err != nil {
		attrs = append(attrs, slog.String("err", err.Error()))
	}
	if t.outcome == outcomeAborted {
		level = slog.LevelWarn
	}
	l.LogAttrs(context.Background(), level, "transfer", attrs...)
}

// seconds rende una durata come secondi con tre decimali: leggibile in text e numerica in JSON
// (slog scriverebbe time.Duration in nanosecondi nel JSON).
func seconds(key string, d time.Duration) slog.Attr {
	return slog.Float64(key, math.Round(d.Seconds()*1000)/1000)
}

// expectsContinue riporta se il client ha chiesto 100 Continue con un body non vuoto.
func expectsContinue(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Expect"), "100-continue") && r.ContentLength != 0
}

// download reclama lo slug e attende la fine del trasferimento; il relay lo scrive il mittente.
func (h *handler) download(w http.ResponseWriter, r *http.Request, id string) {
	if !slug.Valid(id) {
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	}
	if !rangeFromStart(r.Header) {
		h.rejectRange(w, r, id)
		return
	}
	rcv := newResponseReceiver(w, h.opts.IdleTimeout)
	s, err := h.reg.Claim(id, rcv)
	switch {
	case errors.Is(err, relay.ErrNotFound):
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	case errors.Is(err, relay.ErrClaimed), errors.Is(err, relay.ErrGone):
		h.reject(w, r, slog.LevelInfo, http.StatusGone, reasonGone, "gone")
		return
	case err != nil:
		h.opts.Logger.Error("claim_failed", "err", err)
		h.reject(w, r, slog.LevelError, http.StatusInternalServerError, reasonInternal, "internal error")
		return
	}
	h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "download_claimed", slog.String("id", s.ID))

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

// rangeFromStart riporta se la richiesta si serve dal primo byte: nessun Range, un Range da
// ignorare (con If-Range, mai soddisfatto senza validatori; unità diversa da bytes; sintassi non
// valida) o intervalli che partono tutti da 0. In questi casi il relay risponde 200 con il body
// intero, come RFC 9110 consente.
func rangeFromStart(h http.Header) bool {
	v := h.Get("Range")
	if v == "" || h.Get("If-Range") != "" {
		return true
	}
	unit, set, ok := strings.Cut(v, "=")
	if !ok || !strings.EqualFold(strings.TrimSpace(unit), "bytes") {
		return true
	}
	fromStart := true
	specs := 0
	for _, spec := range strings.Split(set, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		specs++
		first, last, ok := strings.Cut(spec, "-")
		if !ok || (first == "" && last == "") {
			return true
		}
		if first == "" {
			// Intervallo suffisso (ultimi N byte): richiede la fine del flusso.
			if _, err := strconv.ParseUint(last, 10, 64); err != nil {
				return true
			}
			fromStart = false
			continue
		}
		n, err := strconv.ParseUint(first, 10, 64)
		if err != nil {
			return true
		}
		if last != "" {
			if _, err := strconv.ParseUint(last, 10, 64); err != nil {
				return true
			}
		}
		if n > 0 {
			fromStart = false
		}
	}
	return specs == 0 || fromStart
}

// rejectRange risponde a una richiesta di intervallo che non parte dal primo byte (ripresa con
// curl -C -, wget -c, connessioni aggiuntive di aria2) senza reclamare lo slug: il relay è un
// flusso unico, e un 200 con il body intero consumerebbe il trasferimento. Uno slug sconosciuto,
// concluso o già reclamato risponde come un GET normale. Non 416: curl -C - lo tratta come file
// già completo ed esce con successo lasciando il file parziale.
func (h *handler) rejectRange(w http.ResponseWriter, r *http.Request, id string) {
	switch err := h.reg.Peek(id); {
	case errors.Is(err, relay.ErrNotFound):
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
	case err != nil:
		h.reject(w, r, slog.LevelInfo, http.StatusGone, reasonGone, "gone")
	default:
		w.Header().Set("Accept-Ranges", "none")
		h.reject(w, r, slog.LevelInfo, http.StatusBadRequest, reasonRangeUnsupported,
			"range requests not supported: download from the first byte, without resume")
	}
}
