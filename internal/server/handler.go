// Package server contiene i gestori HTTP del relay: upload con comandi immediati e download in streaming.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bashtransfer/internal/relay"
	"bashtransfer/internal/slug"
)

// drainTimeout limita il tempo che Go dedica a svuotare il body non letto di un upload terminato
// male (401, 503, scadenza, ricevente caduto). Senza limite un mittente fermo terrebbe aperta la
// connessione all'infinito: l'handler è già finito ma net/http legge il body prima di chiudere.
const drainTimeout = 2 * time.Second

// abortLinger è l'attesa tra l'esito negativo scritto al mittente HTTP/1 e la chiusura della
// connessione: con il body non letto la chiusura invia un RST, che arrivato prima della lettura di
// curl cancellerebbe il messaggio. Variabile per i test.
var abortLinger = 500 * time.Millisecond

// defaultDisposition è il Content-Disposition di ogni risposta che non è un download.
const defaultDisposition = `inline; filename="bashtransfer.txt"`

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
	reasonGoneSender       = "gone_sender"
	reasonGoneExpired      = "gone_expired"
	reasonRangeUnsupported = "range_not_supported"
	reasonBadRequest       = "bad_request"
	reasonMediaType        = "unsupported_media_type"
)

// tokenHeader porta il token di una sessione prenotata (reserve) nel PUT del browser: un header e
// non la query, che i proxy registrano nei log con la riga della richiesta.
const tokenHeader = "X-Bashtransfer-Token"

// Ritmo dello stream di una prenotazione (reserve): avanzamento ogni progressInterval, una riga
// di keepalive ogni keepaliveTicks intervalli senza novità. Dietro Apache la chiusura della pagina
// arriva al server solo dopo alcune scritture fallite verso il browser: il keepalive frequente la
// fa arrivare in pochi secondi se l'annullamento esplicito (cancel) non parte.
const (
	progressInterval = 500 * time.Millisecond
	keepaliveTicks   = 2
)

// bindTimeout è il tempo entro cui il browser deve iniziare il PUT dopo la prenotazione: lo fa
// subito, e una prenotazione senza upload non deve occupare una sessione. Variabile per i test.
var bindTimeout = 30 * time.Second

// goneReasonOf è il motivo del rifiuto (log e pagina) di uno slug terminale: err è un GoneError di
// relay, o ErrClaimed (reasonGone, come un link usato).
func goneReasonOf(err error) string {
	var g *relay.GoneError
	if errors.As(err, &g) {
		switch g.Reason {
		case relay.GoneExpired:
			return reasonGoneExpired
		case relay.GoneSenderGone:
			return reasonGoneSender
		}
	}
	return reasonGone
}

// reject registra il rifiuto di una richiesta e risponde con status e msg (ai browser con la pagina
// del motivo, writeError). Il path non compare nel log: contiene slug o nome del file.
func (h *handler) reject(w http.ResponseWriter, r *http.Request, level slog.Level, status int, reason, msg string) {
	h.logReject(r, level, status, reason)
	writeError(w, r, status, reason, msg)
}

func (h *handler) logReject(r *http.Request, level slog.Level, status int, reason string) {
	h.opts.Logger.LogAttrs(r.Context(), level, "request_rejected",
		slog.String("method", r.Method), slog.Int("status", status), slog.String("reason", reason))
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Ogni risposta porta un nome di file. curl -J apre il file con O_EXCL (rifiuta di sovrascrivere)
	// solo se trova Content-Disposition; senza, ripiega sul nome nell'URL e lo tronca. Un mittente
	// che aggiunge -OJ a curl -T src URL/src scriverebbe così la risposta dentro la sorgente. Il
	// download lo sostituisce con il nome vero. curl recenti (8.18) ignorano l'header sulle
	// risposte 4xx/5xx: lì protegge solo le versioni meno recenti (README, "La sorgente non viene
	// mai alterata").
	w.Header().Set("Content-Disposition", defaultDisposition)
	// Il path ancora codificato: un %2F nel nome resta parte del segmento invece di diventare
	// un separatore (SanitizeName ne tiene poi l'ultima parte).
	seg, ok := singleSegment(r.URL.EscapedPath())
	if !ok {
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	}
	if seg != "" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		// La stessa URL dà la pagina di download o il file: le cache non devono confonderli, e lo
		// stato del link cambia a ogni richiesta.
		w.Header().Set("Vary", "Accept, User-Agent")
		w.Header().Set("Cache-Control", "no-store")
	}
	switch {
	case r.Method == http.MethodGet && seg == "":
		h.usage(w, r)
	case r.Method == http.MethodPost && seg == "":
		h.reserve(w, r)
	case r.Method == http.MethodGet && (wantsHTML(r) || isPreviewAgent(r)):
		// Un browser, o un'anteprima di chat e posta, vede la pagina di download: solo il pulsante
		// (POST) reclama il link, così aprirlo o incollarlo in una chat non lo consuma.
		h.landing(w, r, seg)
	case r.Method == http.MethodGet, r.Method == http.MethodPost && seg != "":
		h.download(w, r, seg)
	case r.Method == http.MethodPut:
		h.upload(w, r, seg)
	case r.Method == http.MethodDelete && seg != "" && r.Header.Get(tokenHeader) != "":
		h.cancel(w, r, seg)
	default:
		w.Header().Set("Allow", "GET, POST, PUT")
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
	// "/" non ha un nome di file: curl -OJ non ha nulla da sovrascrivere, e il browser mostra la pagina.
	w.Header().Del("Content-Disposition")
	w.Header().Add("Vary", "Accept")
	if wantsHTML(r) {
		serveHome(w, base)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "bashtransfer: file relay, nothing is written to disk.\n"+
		"Send:    curl %s -T FILE\n"+
		"Receive: open the sender's link in a browser, or run the curl, wget or aria2c command\n"+
		"         from the sender's table.\n", base)
}

// authorized riporta se la richiesta può aprire un invio; altrimenti risponde 401.
func (h *handler) authorized(w http.ResponseWriter, r *http.Request) bool {
	if h.opts.Authorize == nil || h.opts.Authorize(r) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="bashtransfer"`)
	h.reject(w, r, slog.LevelWarn, http.StatusUnauthorized, reasonUnauthorized, "unauthorized")
	return false
}

// rejectCreate risponde all'errore di Create o Reserve.
func (h *handler) rejectCreate(w http.ResponseWriter, r *http.Request, err error) {
	level, reason := slog.LevelWarn, reasonCapacity
	if !errors.Is(err, relay.ErrCapacity) {
		level, reason = slog.LevelError, reasonInternal
		h.opts.Logger.Error("session_create_failed", "err", err)
	}
	h.reject(w, r, level, http.StatusServiceUnavailable, reason, "service unavailable")
}

// reservation è la risposta di reserve: il link, i comandi della tabella e il token del PUT.
type reservation struct {
	Slug     string    `json:"slug"`
	URL      string    `json:"url"`
	Token    string    `json:"token"`
	Commands []command `json:"commands"`
}

// reserve prenota una sessione per un invio dal browser: POST / con JSON {"name": ..., "size": ...}.
// Il browser invia poi il body con PUT /<slug> e il token (upload). Solo JSON: un altro sito non può
// inviarlo senza un preflight CORS, che il relay non autorizza.
//
// La risposta è uno stream NDJSON aperto per tutto il trasferimento: la prenotazione (link, comandi,
// token), {"event":"attached"} quando il ricevente si aggancia, {"event":"progress","bytes":N} con i
// byte consegnati al ricevente e infine l'esito, come nella risposta del PUT. L'esito non può
// viaggiare solo sul PUT: su HTTP/1 il browser legge la risposta solo dopo aver inviato tutto il
// body, e quella di un trasferimento interrotto si perde nel reset della connessione. La chiusura
// dello stream (pagina chiusa, Cancel) termina la sessione: dietro il body non letto del PUT il
// server non vedrebbe andare via il mittente.
func (h *handler) reserve(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		h.reject(w, r, slog.LevelDebug, http.StatusUnsupportedMediaType, reasonMediaType, "unsupported media type")
		return
	}
	base, ok := h.baseURL(r)
	if !ok {
		h.reject(w, r, slog.LevelInfo, http.StatusBadRequest, reasonInvalidHost, "invalid host")
		return
	}
	var req struct {
		Name string `json:"name"`
		Size *int64 `json:"size"`
	}
	// Il body va letto fino in fondo: solo dopo net/http rileva la chiusura della connessione e
	// annulla il contesto della richiesta.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || json.Unmarshal(body, &req) != nil || req.Size == nil || *req.Size < 0 {
		h.reject(w, r, slog.LevelDebug, http.StatusBadRequest, reasonBadRequest, "bad request")
		return
	}
	s, err := h.reg.Reserve(SanitizeName(req.Name), *req.Size, bindTimeout)
	if err != nil {
		h.rejectCreate(w, r, err)
		return
	}
	h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "upload_reserved",
		slog.String("id", s.ID), slog.Int64("length", s.Length))
	u := base + "/" + s.Slug
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	send := func(v any) bool { return enc.Encode(v) == nil && rc.Flush() == nil }
	if !send(reservation{
		Slug: s.Slug, URL: u, Token: s.Token, Commands: append(receiverCommands(u), command{"direct", u}),
	}) {
		h.reg.Cancel(s)
		return
	}

	type event struct {
		Event string `json:"event"`
		Bytes *int64 `json:"bytes,omitempty"`
	}
	tick := time.NewTicker(progressInterval)
	defer tick.Stop()
	started := s.Started()
	attached := false
	var last int64 = -1
	for ticks := 0; ; {
		select {
		case <-s.Finished():
			res := s.Result()
			if res == "" {
				// Sessione finita senza un esito del PUT: scaduta prima del body, chiusa per bindTimeout
				// o mittente caduto.
				select {
				case <-s.Expired():
					res = webOutcome(outcomeExpired, 0, time.Since(s.Created))
				default:
					res = webOutcome(failReason(s.Err()), s.Delivered(), 0)
				}
			}
			_, _ = io.WriteString(w, strings.TrimLeft(res, "\n"))
			_ = rc.Flush()
			return
		case <-started:
			started, attached = nil, true
			if !send(event{Event: "attached"}) {
				h.reg.Cancel(s)
				return
			}
		case <-tick.C:
			// L'avanzamento quando cambia; senza novità una riga ogni keepaliveTicks: una scrittura
			// fallita rivela il mittente caduto anche se la chiusura non arriva al server (proxy).
			ticks++
			ok := true
			switch n := s.Delivered(); {
			case attached && (n != last || ticks%keepaliveTicks == 0):
				last = n
				ok = send(event{Event: "progress", Bytes: &n})
			case !attached && ticks%keepaliveTicks == 0:
				ok = send(event{Event: "waiting"})
			}
			if !ok {
				h.reg.Cancel(s)
				return
			}
		case <-r.Context().Done():
			h.reg.Cancel(s)
			return
		}
	}
}

// cancel annulla un invio dal browser: DELETE /<slug> con il token della prenotazione. La pagina lo
// invia al Cancel e alla chiusura (fetch keepalive): la chiusura dello stream della prenotazione
// basta solo senza proxy, perché Apache la inoltra al server con ritardo. Prima del ricevente il
// link diventa 410; durante il download il ricevente vede il trasferimento interrotto.
func (h *handler) cancel(w http.ResponseWriter, r *http.Request, seg string) {
	s, err := h.reg.Reserved(seg, r.Header.Get(tokenHeader))
	if err != nil {
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	}
	h.reg.Cancel(s)
	h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "upload_cancelled", slog.String("id", s.ID))
	w.WriteHeader(http.StatusNoContent)
}

// upload apre la sessione, scrive subito i comandi e legge il body solo quando il ricevente si aggancia.
// Con tokenHeader è il body di una sessione prenotata dal browser (reserve): seg è lo slug, il token
// sostituisce le credenziali, e la risposta contiene solo l'esito finale in JSON (webOutcome).
func (h *handler) upload(w http.ResponseWriter, r *http.Request, seg string) {
	rc := http.NewResponseController(w)
	ok := false
	drain := drainTimeout
	defer func() {
		if !ok {
			_ = rc.SetReadDeadline(time.Now().Add(drain))
		}
	}()
	token := r.Header.Get(tokenHeader)
	web := token != ""
	if !web && !h.authorized(w, r) {
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
	var s *relay.Session
	if web {
		var err error
		if s, err = h.reg.Bind(seg, token); err != nil {
			h.reject(w, r, slog.LevelInfo, http.StatusNotFound, reasonNotFound, "404 page not found")
			return
		}
	} else {
		var err error
		if s, err = h.reg.Create(SanitizeName(seg), r.ContentLength); err != nil {
			h.rejectCreate(w, r, err)
			return
		}
	}
	name := s.Name
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

	// La lunghezza annunciata alla prenotazione è quella che il ricevente vedrà in Content-Length.
	if web && r.ContentLength >= 0 && r.ContentLength != s.Length {
		s.Abort(relay.ErrLengthMismatch)
		s.SetResult(webOutcome(failLengthMismatch, 0, 0))
		h.reject(w, r, slog.LevelInfo, http.StatusBadRequest, reasonBadRequest, "length mismatch")
		return
	}

	// curl invia "Expect: 100-continue" per upload grandi. Se la risposta finale arriva prima
	// del 100 e prima della lettura del body, Go chiude il body (ErrBodyReadAfterClose).
	// Il 100 viene quindi inviato esplicitamente, prima dei comandi.
	// Dietro un proxy h2c (Apache) l'header Expect non arriva: il proxy lo toglie e curl, vista la
	// risposta finale senza 100, non invia il body. Su HTTP/2 il 100 va quindi sempre inviato.
	if expectsContinue(r) || (r.ProtoMajor == 2 && r.ContentLength != 0) {
		w.WriteHeader(http.StatusContinue)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// La connessione del mittente non viene riusata: Stop può lasciarvi una scadenza di lettura
	// passata, e un upload resta comunque l'unica richiesta di curl -T.
	// Solo HTTP/1: in HTTP/2 l'header diventa un GOAWAY che blocca i proxy h2c (Apache) con il body in attesa.
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "close")
	}
	if !web {
		io.WriteString(w, commands(base+"/"+s.Slug))
		_ = rc.Flush()
	}

	// Con il body non letto net/http non vede la caduta del mittente: la connessione viene
	// controllata a intervalli finché il ricevente non si aggancia.
	conn := connFrom(r.Context())
	var peerCheck <-chan time.Time
	if conn != nil && peerCheckSupported {
		t := time.NewTicker(peerCheckInterval)
		defer t.Stop()
		peerCheck = t.C
	}

	// Solo per il browser: per curl un'interruzione prima della copia (ricevente caduto appena
	// agganciato) passa dalla copia, che scrive al mittente il motivo.
	var cancelled <-chan struct{}
	if web {
		cancelled = s.Aborted()
	}

	var heartbeat <-chan time.Time
	if r.ProtoMajor == 2 {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		heartbeat = t.C
	}

	for {
		select {
		case rcv := <-s.Attached():
			rr := rcv.(*responseReceiver)
			tr.attached = time.Now()
			h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "receiver_attached",
				slog.String("id", s.ID), seconds("wait_s", tr.attached.Sub(s.Created)))
			rr.start(name, s.Length)
			s.Start()
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
				if web {
					res := webOutcome(failReason(s.Err()), n, time.Since(tr.attached))
					s.SetResult(res)
					writeWebOutcome(w, res)
					return
				}
				abortSender(w, r, failedMessage(s.Err(), n, s.Length, h.opts.IdleTimeout))
				return
			}
			tr.outcome = outcomeOK
			ok = true
			msg := doneMessage(n, time.Since(tr.attached))
			if web {
				msg = webOutcome(outcomeOK, n, time.Since(tr.attached))
				s.SetResult(msg)
			}
			io.WriteString(w, msg)
			_ = rc.Flush()
			return
		case <-s.Expired():
			tr.outcome = outcomeExpired
			// Prima dell'Abort differito con ErrSenderGone: il log deve dire scadenza, non mittente caduto.
			s.Abort(relay.ErrExpired)
			drain = 0
			if web {
				res := webOutcome(outcomeExpired, 0, time.Since(s.Created))
				s.SetResult(res)
				writeWebOutcome(w, res)
				return
			}
			abortSender(w, r, fmt.Sprintf("\nERRORE: download NON avvenuto: nessun ricevente si è collegato "+
				"in %s, il link è stato chiuso.\n", roundDuration(time.Since(s.Created))))
			return
		case <-cancelled:
			// Il mittente nel browser ha chiuso la pagina o annullato (Cancel dallo stream della
			// prenotazione): il body non letto non va più atteso. L'esito lo scrive lo stream.
			drain = 0
			return
		case <-r.Context().Done():
			return
		case <-heartbeat:
			if _, err := io.WriteString(w, "\r"); err != nil {
				drain = 0
				panic(http.ErrAbortHandler)
			}
			if err := rc.Flush(); err != nil {
				drain = 0
				panic(http.ErrAbortHandler)
			}
		case <-peerCheck:
			if peerClosed(conn) {
				// outcome resta sender_gone; il defer registra ErrSenderGone e libera lo slug.
				drain = 0
				panic(http.ErrAbortHandler)
			}
		}
	}
}

// doneMessage è l'ultima riga per il mittente a trasferimento riuscito: tutti i byte sono stati
// scritti verso il ricevente. Il salvataggio su disco lo verifica il programma del ricevente.
func doneMessage(n int64, d time.Duration) string {
	return fmt.Sprintf("\nOK: download completato, %s consegnati al ricevente in %s.\n", sizeText(n), roundDuration(d))
}

// Motivi di un relay interrotto (failReason), nell'esito per il browser.
const (
	failReceiverGone   = "receiver_gone"
	failIdle           = "idle"
	failLengthMismatch = "length_mismatch"
	failSenderGone     = "sender_gone"
)

// failReason classifica il primo errore della sessione di un relay interrotto.
func failReason(err error) string {
	switch {
	case errors.Is(err, relay.ErrReceiverGone):
		return failReceiverGone
	case errors.Is(err, relay.ErrIdle):
		return failIdle
	case errors.Is(err, relay.ErrLengthMismatch):
		return failLengthMismatch
	}
	return failSenderGone
}

// failedMessage è l'ultima riga per il mittente quando il relay si interrompe dopo n byte. err è
// il primo errore della sessione.
func failedMessage(err error, n, length int64, idle time.Duration) string {
	var why string
	switch failReason(err) {
	case failReceiverGone:
		why = "il ricevente si è disconnesso o ha smesso di ricevere"
	case failIdle:
		why = "nessun dato dal mittente per " + idle.String()
	case failLengthMismatch:
		why = "il mittente ha inviato meno byte di quelli dichiarati"
	default:
		why = "connessione con il mittente interrotta"
	}
	done := strconv.FormatInt(n, 10)
	if length >= 0 {
		done += " di " + strconv.FormatInt(length, 10)
	}
	return fmt.Sprintf("\nERRORE: download NON completato dopo %s byte: %s.\n", done, why)
}

// webOutcome è l'ultima riga della risposta a un invio dal browser: l'esito in JSON, che la pagina
// mostra nella sua lingua. outcome è outcomeOK, outcomeExpired o un motivo di failReason; seconds è
// la durata del relay (dell'attesa per outcomeExpired).
func webOutcome(outcome string, n int64, d time.Duration) string {
	b, _ := json.Marshal(struct {
		Outcome string  `json:"outcome"`
		Bytes   int64   `json:"bytes"`
		Seconds float64 `json:"seconds"`
	}{outcome, n, math.Round(d.Seconds()*10) / 10})
	return "\n" + string(b) + "\n"
}

// abortSender scrive msg al mittente e chiude la risposta. Su HTTP/1 la interrompe senza chunk
// finale, così curl esce con errore: chiude prima il lato in scrittura (curl legge il messaggio e
// poi la fine del flusso) e attende abortLinger prima del RST. Su HTTP/2 lo stream termina
// regolarmente: un proxy h2c (Apache) che riceve un reset aggiunge al messaggio una sua pagina 502
// e chiude comunque la risposta al client senza errore.
func abortSender(w http.ResponseWriter, r *http.Request, msg string) {
	rc := http.NewResponseController(w)
	_, err := io.WriteString(w, msg)
	if err == nil {
		err = rc.Flush()
	}
	if r.ProtoMajor != 1 && err == nil {
		return
	}
	if err == nil {
		if cw, ok := connFrom(r.Context()).(interface{ CloseWrite() error }); ok && cw.CloseWrite() == nil {
			time.Sleep(abortLinger)
		}
	}
	panic(http.ErrAbortHandler)
}

// writeWebOutcome scrive l'esito di un upload dal browser e lascia terminare la risposta
// regolarmente anche su HTTP/1: per XMLHttpRequest una risposta troncata è solo un errore di rete,
// senza l'esito. Con il body non letto net/http chiude poi la connessione in scrittura e attende
// prima del reset, come abortSender.
func writeWebOutcome(w http.ResponseWriter, msg string) {
	_, _ = io.WriteString(w, msg)
	_ = http.NewResponseController(w).Flush()
}

// sizeText rende n come "3000000 byte (2.9 MiB)".
func sizeText(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " byte"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%d byte (%.1f %ciB)", n, float64(n)/float64(div), "KMGTPE"[exp])
}

// roundDuration arrotonda d ai millisecondi sotto il secondo, ai decimi di secondo sopra.
func roundDuration(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(100 * time.Millisecond)
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
	n, err := h.copier.Copy(deliveredCounter{rr, s}, body)
	close(copied)
	<-watched
	return n, err
}

// deliveredCounter conta nella sessione i byte scritti al ricevente, per l'avanzamento che lo stream
// della prenotazione mostra al mittente nel browser.
type deliveredCounter struct {
	relay.Receiver
	s *relay.Session
}

func (c deliveredCounter) Write(p []byte) (int, error) {
	n, err := c.Receiver.Write(p)
	c.s.AddDelivered(int64(n))
	return n, err
}

// command è un comando di download: lo strumento e la riga da eseguire.
type command struct {
	Label string `json:"label"`
	Line  string `json:"line"`
}

// receiverCommands restituisce i comandi di download per il link u, uno per strumento (curl, wget,
// aria2c): la tabella del mittente e le pagine HTML li prendono da qui. aria2c usa una sola
// connessione: il relay è un flusso unico e non serve intervalli di byte.
// Invariante: la sorgente non viene mai alterata. Se il ricevente lavora nella cartella del
// mittente, il file omonimo è proprio quello in invio, e nessun comando, né alcuna sua variante con
// meno flag, deve sovrascriverlo (scripts/invariant.py lo verifica). Per questo i comandi non
// contengono il nome: un nome esplicito (curl -o, wget -O) tronca il file prima di ogni controllo,
// e busybox wget ignora -nc. Il nome arriva da Content-Disposition: curl -OJ rifiuta di
// sovrascrivere, wget --content-disposition e aria2c salvano con un altro nome (.1). Senza -J o
// --content-disposition il nome è lo slug, senza -O curl scrive sullo stdout. -f evita di salvare
// la pagina di errore; --allow-overwrite=false prevale su un aria2.conf che chiede di sovrascrivere.
func receiverCommands(u string) []command {
	return []command{
		{"curl", fmt.Sprintf("curl -f -OJ \"%s\"", u)},
		{"wget", fmt.Sprintf("wget --content-disposition \"%s\"", u)},
		{"aria2c", fmt.Sprintf("aria2c -x1 -s1 --allow-overwrite=false \"%s\"", u)},
	}
}

// commands restituisce la tabella ASCII stampata al mittente: una riga per strumento con il
// comando di download (receiverCommands) e la riga direct con il solo link, senza virgolette, da
// incollare nel browser: apre la pagina di download, e il file parte solo dal suo pulsante.
func commands(u string) string {
	cmds := append(receiverCommands(u), command{"direct", u})
	const (
		title = "bashtransfer: comandi per il ricevente"
		note  = "Un solo ricevente per link. Lascia aperto il terminale fino alla fine."
	)
	// Due colonne (strumento, comando) larghe quanto il contenuto più lungo; titolo e nota occupano
	// tutta la larghezza. Se sono più larghi dei comandi, la colonna dei comandi si allarga.
	lw, cw := 0, 0
	for _, c := range cmds {
		lw = max(lw, len(c.Label))
		cw = max(cw, utf8.RuneCountInString(c.Line))
	}
	inner := max(lw+3+cw, utf8.RuneCountInString(title), utf8.RuneCountInString(note))
	cw = inner - lw - 3
	pad := func(s string, w int) string {
		return s + strings.Repeat(" ", w-utf8.RuneCountInString(s))
	}
	full := "+" + strings.Repeat("-", inner+2) + "+\n"
	split := "+" + strings.Repeat("-", lw+2) + "+" + strings.Repeat("-", cw+2) + "+\n"
	var b strings.Builder
	b.WriteString(full)
	b.WriteString("| " + pad(title, inner) + " |\n")
	b.WriteString(split)
	for _, c := range cmds {
		b.WriteString("| " + pad(c.Label, lw) + " | " + pad(c.Line, cw) + " |\n")
	}
	b.WriteString(split)
	b.WriteString("| " + pad(note, inner) + " |\n")
	b.WriteString(full)
	return b.String()
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
	case errors.Is(err, relay.ErrClaimed):
		// Nel log resta gone; il browser (pulsante premuto due volte) vede che il download è partito.
		h.logReject(r, slog.LevelInfo, http.StatusGone, reasonGone)
		writeError(w, r, http.StatusGone, pageClaimed, "gone")
		return
	case errors.Is(err, relay.ErrGone):
		h.reject(w, r, slog.LevelInfo, http.StatusGone, goneReasonOf(err), "gone")
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
		writeError(w, r, http.StatusServiceUnavailable, pageAborted, "transfer aborted")
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
	switch _, err := h.reg.Peek(id); {
	case errors.Is(err, relay.ErrNotFound):
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
	case err != nil:
		h.reject(w, r, slog.LevelInfo, http.StatusGone, goneReasonOf(err), "gone")
	default:
		w.Header().Set("Accept-Ranges", "none")
		h.reject(w, r, slog.LevelInfo, http.StatusBadRequest, reasonRangeUnsupported,
			"range requests not supported: download from the first byte, without resume")
	}
}
