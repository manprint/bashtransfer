// Package server contains the relay's HTTP handlers: upload with immediate commands and streaming download.
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

// drainTimeout limits the time Go spends draining the unread body of an upload that ended
// abnormally (401, 503, expiry, receiver gone). Without a limit, a stalled sender would keep the
// connection open forever: the handler has already finished, but net/http reads the body before closing.
const drainTimeout = 2 * time.Second

// abortLinger is the wait between writing the negative outcome to an HTTP/1 sender and closing the
// connection: with the body unread, closing sends an RST, which would cancel the message if it
// arrived before curl reads it. A variable for the tests.
var abortLinger = 500 * time.Millisecond

// defaultDisposition is the Content-Disposition of every response that is not a download.
const defaultDisposition = `inline; filename="bashtransfer.txt"`

// Options configures the HTTP handler.
type Options struct {
	// PublicURL, if not empty, is the base of the generated commands (D12).
	PublicURL string
	// Authorize, if not nil, decides whether a PUT is authorized (D9).
	Authorize func(*http.Request) bool
	// IdleTimeout, if > 0, aborts the relay if the sender is idle for this long (D15)
	// or if a single write to the receiver does not complete within this time.
	IdleTimeout time.Duration
	// Logger, if not nil, receives the relay events (README, "Log"). Slugs,
	// file names, paths or credentials never appear in it: a session is identified by its random ID.
	Logger *slog.Logger
}

type handler struct {
	reg    *relay.Registry
	copier *relay.Copier
	opts   Options
}

// New returns the HTTP handler of the relay.
func New(reg *relay.Registry, copier *relay.Copier, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	reg.OnGraceExpired(func(s *relay.Session) {
		opts.Logger.LogAttrs(context.Background(), slog.LevelInfo, "sender_grace_expired",
			slog.String("id", s.ID), seconds("wait_s", time.Since(s.Created)))
	})
	return &handler{reg: reg, copier: copier, opts: opts}
}

// Rejection reasons recorded in the logs (reason field of request_rejected).
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

// tokenHeader carries the token of a reserved session (reserve) in the browser's PUT: a header and
// not the query, which proxies record in logs with the request line.
const tokenHeader = "X-Bashtransfer-Token"

// Pace of a reservation's stream (reserve): progress every progressInterval, one keepalive line every
// keepaliveTicks intervals without news. Behind Apache the page closing reaches the server only after
// several failed writes to the browser: frequent keepalives make it arrive within seconds if the explicit
// cancellation (cancel) does not go out.
const (
	progressInterval = 500 * time.Millisecond
	keepaliveTicks   = 2
)

// bindTimeout is the time within which the browser must start the PUT after the reservation: it does so
// immediately, and a reservation without an upload must not occupy a session. A variable for the tests.
var bindTimeout = 30 * time.Second

// goneReasonOf is the reason for rejecting (log and page) a terminal slug: err is a relay GoneError,
// or ErrClaimed (reasonGone, like an already-used link).
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

// reject logs the rejection of a request and responds with status and msg (to browsers with the reason
// page, writeError). The path does not appear in the log: it contains the slug or the file name.
func (h *handler) reject(w http.ResponseWriter, r *http.Request, level slog.Level, status int, reason, msg string) {
	h.logReject(r, level, status, reason)
	writeError(w, r, status, reason, msg)
}

func (h *handler) logReject(r *http.Request, level slog.Level, status int, reason string) {
	h.opts.Logger.LogAttrs(r.Context(), level, "request_rejected",
		slog.String("method", r.Method), slog.Int("status", status), slog.String("reason", reason))
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Every response carries a file name. curl -J opens the file with O_EXCL (refuses to overwrite) only
	// if it finds Content-Disposition; without it, it falls back to the name in the URL and truncates it.
	// A sender that adds -OJ to curl -T src URL/src would thus write the response inside the source. The
	// download replaces it with the real name. Recent curl (8.18) ignores the header on 4xx/5xx responses:
	// there only older versions are protected (README, "The source is never altered").
	w.Header().Set("Content-Disposition", defaultDisposition)
	// The path is still encoded: a %2F in the name stays part of the segment instead of becoming a
	// separator (SanitizeName then keeps only its last part).
	seg, ok := singleSegment(r.URL.EscapedPath())
	if !ok {
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	}
	if seg != "" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		// The same URL gives the download page or the file: caches must not confuse them, and the
		// link's state changes on every request.
		w.Header().Set("Vary", "Accept, User-Agent")
		w.Header().Set("Cache-Control", "no-store")
	}
	switch {
	case r.Method == http.MethodGet && seg == "":
		h.usage(w, r)
	case r.Method == http.MethodPost && seg == "":
		h.reserve(w, r)
	case r.Method == http.MethodGet && r.URL.Query().Get("status") == "1":
		h.linkStatus(w, r, seg)
	case r.Method == http.MethodPost && seg != "" && r.Header.Get(tokenHeader) != "":
		h.foreground(w, r, seg)
	case r.Method == http.MethodGet && (wantsHTML(r) || isPreviewAgent(r)):
		// A browser, or a chat or mail preview, sees the download page: only the button (POST)
		// claims the link, so opening it or pasting it into a chat does not consume it.
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

// singleSegment accepts "/" (empty segment) and "/<seg>" with p still encoded; other paths are
// 404. The returned segment is decoded.
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

// unsafeHostChars are the characters the shell interprets inside double quotes, plus spaces and
// line breaks. net/http accepts `$`, `!` and parentheses in Host: without this check a Host such as
// `x$(cmd)` would end up in the printed command line.
const unsafeHostChars = "\"`$\\ !\t\r\n"

// baseURL builds the base of the commands (D12). ok is false if Host is empty or contains characters
// that the shell would interpret in the printed line.
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
	// "/" has no file name: curl -OJ has nothing to overwrite, and the browser shows the page.
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

// authorized reports whether the request may open an upload; otherwise it responds 401.
func (h *handler) authorized(w http.ResponseWriter, r *http.Request) bool {
	if h.opts.Authorize == nil || h.opts.Authorize(r) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="bashtransfer"`)
	h.reject(w, r, slog.LevelWarn, http.StatusUnauthorized, reasonUnauthorized, "unauthorized")
	return false
}

// rejectCreate responds to an error from Create or Reserve.
func (h *handler) rejectCreate(w http.ResponseWriter, r *http.Request, err error) {
	level, reason := slog.LevelWarn, reasonCapacity
	if !errors.Is(err, relay.ErrCapacity) {
		level, reason = slog.LevelError, reasonInternal
		h.opts.Logger.Error("session_create_failed", "err", err)
	}
	h.reject(w, r, level, http.StatusServiceUnavailable, reason, "service unavailable")
}

// reservation is the response of reserve: the link, the table's commands and the PUT token.
type reservation struct {
	Slug     string    `json:"slug"`
	URL      string    `json:"url"`
	Token    string    `json:"token"`
	Commands []command `json:"commands"`
	// Grace, if > 0, is the tolerance in seconds for a sender that dropped (BASHT_SENDER_GRACE): the
	// page reattaches by resuming the stream (POST / with resume) and a new PUT.
	Grace    int64 `json:"grace,omitempty"`
	Started  bool  `json:"started,omitempty"`
	Bytes    int64 `json:"bytes,omitempty"`
	Ready    *bool `json:"ready,omitempty"`
	ReadyTTL int64 `json:"ready_ttl,omitempty"`
}

// reserve reserves a session for an upload from the browser: POST / with JSON {"name": ..., "size": ...}.
// The browser then sends the body with PUT /<slug> and the token (upload). JSON only: another site cannot
// send it without a CORS preflight, which the relay does not authorize.
//
// The response is an NDJSON stream kept open for the whole transfer: the reservation (link, commands,
// token), {"event":"attached"} when the receiver attaches, {"event":"progress","bytes":N} with the
// bytes delivered to the receiver, and finally the outcome, as in the PUT response. The outcome cannot
// travel only on the PUT: on HTTP/1 the browser reads the response only after sending the whole
// body, and that of an interrupted transfer is lost in the connection reset. Closing the stream
// (page closed, Cancel) ends the session: behind the unread PUT body the server would not see the
// sender leave.
//
// With BASHT_SENDER_GRACE the mere drop of the stream no longer ends the session: the sender's drop
// is detected by the PUT (upload), which puts it in tolerance. A sender that returns reopens the
// stream with POST / {"resume": "<slug>"} and the token in the header, and repeats the PUT.
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
		Name       string `json:"name"`
		Size       *int64 `json:"size"`
		Resume     string `json:"resume"`
		Rebind     bool   `json:"rebind"`
		Foreground *bool  `json:"foreground"`
		Readiness  bool   `json:"readiness"`
	}
	// The body must be read to the end: only then net/http detects the connection close and
	// cancels the request context.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || json.Unmarshal(body, &req) != nil || (req.Resume == "" && (req.Size == nil || *req.Size < 0)) {
		h.reject(w, r, slog.LevelDebug, http.StatusBadRequest, reasonBadRequest, "bad request")
		return
	}
	grace := h.reg.SenderGrace()
	var s *relay.Session
	var alreadyStarted bool
	if req.Resume != "" {
		// Reopens the stream of a live reservation: only with tolerance active and the right token.
		var err error
		if s, alreadyStarted, err = h.reg.Resume(req.Resume, r.Header.Get(tokenHeader), req.Rebind); err != nil {
			h.reject(w, r, slog.LevelInfo, http.StatusNotFound, reasonNotFound, "404 page not found")
			return
		}
		h.opts.Logger.LogAttrs(r.Context(), slog.LevelInfo, "upload_resumed", slog.String("id", s.ID))
	} else {
		if s, err = h.reg.Reserve(SanitizeName(req.Name), *req.Size, bindTimeout); err != nil {
			h.rejectCreate(w, r, err)
			return
		}
		h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "upload_reserved",
			slog.String("id", s.ID), slog.Int64("length", s.Length))
		if req.Foreground != nil {
			_ = h.reg.Foreground(s.Slug, s.Token, *req.Foreground)
		}
	}
	// On exit from the stream (page closed, network dropped, Cancel, session finished): without tolerance
	// the session ends (Cancel does nothing on a finished one); with tolerance the closing of the last
	// stream puts the sender in tolerance and the link stays alive for a reattach.
	h.reg.StreamOpen(s)
	defer func() {
		if grace <= 0 {
			h.reg.Cancel(s)
		} else if h.reg.StreamClosed(s) {
			h.opts.Logger.LogAttrs(context.Background(), slog.LevelInfo, "sender_detached",
				slog.String("id", s.ID), seconds("wait_s", time.Since(s.Created)), seconds("grace_s", grace))
		}
	}()
	u := base + "/" + s.Slug
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	send := func(v any) bool { return enc.Encode(v) == nil && rc.Flush() == nil }
	var ready *bool
	var readyTTL int64
	lastReady := false
	if req.Readiness {
		lastReady = s.PeerReady()
		ready = &lastReady
		readyTTL = int64(relay.ReceiverPresenceTimeout / time.Second)
	}
	if !send(reservation{
		Slug: s.Slug, URL: u, Token: s.Token, Commands: append(receiverCommands(u), command{"direct", u}),
		Grace:   int64(grace / time.Second),
		Started: alreadyStarted, Bytes: s.Delivered(),
		Ready: ready, ReadyTTL: readyTTL,
	}) {
		return
	}

	type event struct {
		Event string `json:"event"`
		Bytes *int64 `json:"bytes,omitempty"`
		Ready *bool  `json:"ready,omitempty"`
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
				// Session finished without a PUT outcome: expired before the body, closed for bindTimeout,
				// or sender gone.
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
			var ready *bool
			if req.Readiness {
				value := s.PeerReady()
				ready = &value
			}
			if !send(event{Event: "attached", Ready: ready}) {
				return
			}
		case <-tick.C:
			// Progress when it changes; without news one line every keepaliveTicks: a failed write
			// reveals the dropped sender even if the close does not reach the server (proxy).
			ticks++
			if req.Readiness {
				value := s.PeerReady()
				if value != lastReady || ticks%keepaliveTicks == 0 {
					if !send(event{Event: "readiness", Ready: &value}) {
						return
					}
					lastReady = value
				}
			}
			ok := true
			switch n := s.Delivered(); {
			case attached && (n != last || ticks%keepaliveTicks == 0):
				last = n
				ok = send(event{Event: "progress", Bytes: &n})
			case !attached && ticks%keepaliveTicks == 0:
				ok = send(event{Event: "waiting"})
			}
			if !ok {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (h *handler) foreground(w http.ResponseWriter, r *http.Request, id string) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		h.reject(w, r, slog.LevelDebug, http.StatusUnsupportedMediaType, reasonMediaType, "unsupported media type")
		return
	}
	var req struct {
		Foreground *bool `json:"foreground"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || json.Unmarshal(body, &req) != nil || req.Foreground == nil {
		h.reject(w, r, slog.LevelDebug, http.StatusBadRequest, reasonBadRequest, "bad request")
		return
	}
	if h.reg.Foreground(id, r.Header.Get(tokenHeader), *req.Foreground) != nil {
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancel cancels an upload from the browser: DELETE /<slug> with the reservation's token. The page sends
// it on Cancel and on close (fetch keepalive): closing the reservation's stream is enough only without
// a proxy, because Apache forwards it to the server late. Before the receiver, the link becomes 410;
// during the download the receiver sees the transfer interrupted.
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

// upload opens the session, writes the commands immediately and reads the body only when the receiver attaches.
// With tokenHeader it is the body of a session reserved by the browser (reserve): seg is the slug, the token
// replaces the credentials, and the response contains only the final outcome in JSON (webOutcome).
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
	var binding <-chan struct{}
	if web {
		var err error
		if s, binding, err = h.reg.BindDetachable(seg, token); err != nil {
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
	h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "upload_accepted",
		slog.String("id", s.ID), slog.Int64("length", s.Length))
	tr := transferLog{session: s}
	// detached: the session must not be ended here. The sender dropped and the tolerance (Detach) keeps it
	// alive for a reattach, or a new PUT with the same token has taken its place.
	detached := false
	defer func() {
		if detached {
			return
		}
		if !ok {
			s.Abort(relay.ErrSenderGone)
		}
		h.reg.Finish(s)
		err := s.Err()
		if ok {
			// Full copy: an ErrReceiverGone recorded after the last byte (the client closes as soon as it
			// has read Content-Length bytes) is not a transfer error.
			err = nil
		}
		tr.log(h.opts.Logger, err)
	}()

	// The length announced at the reservation is the one the receiver will see in Content-Length.
	if web && r.ContentLength >= 0 && r.ContentLength != s.Length {
		s.Abort(relay.ErrLengthMismatch)
		s.SetResult(webOutcome(failLengthMismatch, 0, 0))
		h.reject(w, r, slog.LevelInfo, http.StatusBadRequest, reasonBadRequest, "length mismatch")
		return
	}

	// curl sends "Expect: 100-continue" for large uploads. If the final response arrives before
	// the 100 and before the body is read, Go closes the body (ErrBodyReadAfterClose).
	// So the 100 is sent explicitly, before the commands.
	// Behind an h2c proxy (Apache) the Expect header does not arrive: the proxy strips it and curl, seeing
	// the final response without a 100, does not send the body. On HTTP/2 the 100 must therefore always be sent.
	if expectsContinue(r) || (r.ProtoMajor == 2 && r.ContentLength != 0) {
		w.WriteHeader(http.StatusContinue)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// The sender's connection is not reused: Stop may leave a past read deadline on it, and an upload
	// is in any case the only request of curl -T.
	// HTTP/1 only: in HTTP/2 the header becomes a GOAWAY that blocks h2c proxies (Apache) with the body pending.
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "close")
	}
	if !web {
		io.WriteString(w, commands(base+"/"+s.Slug))
		_ = rc.Flush()
	}

	// Without reading the body net/http does not see the sender drop: the connection is
	// checked at intervals until the receiver attaches.
	conn := connFrom(r.Context())
	var peerCheck <-chan time.Time
	if conn != nil && peerCheckSupported {
		t := time.NewTicker(peerCheckInterval)
		defer t.Stop()
		peerCheck = t.C
	}

	// Browser only: for curl, an interruption before the copy (receiver dropped right after attaching)
	// goes through the copy, which writes the reason to the sender.
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

	// senderLost records the sender's drop before the receiver: with tolerance active the
	// session stays alive without it (detached), otherwise the defer ends it.
	senderLost := func() {
		if h.reg.Detach(s, binding) {
			detached = true
			drain = 0
			h.opts.Logger.LogAttrs(r.Context(), slog.LevelInfo, "sender_detached",
				slog.String("id", s.ID), seconds("wait_s", time.Since(s.Created)),
				seconds("grace_s", h.reg.SenderGrace()))
		}
	}

	for {
		select {
		case rcv := <-s.Attached():
			// A replacement attach has already taken this one's place: the receiver goes to the new handler.
			if !h.reg.BeginRelay(s, binding) {
				s.Requeue(rcv)
				detached = true
				drain = 0
				return
			}
			rr := rcv.(*responseReceiver)
			tr.attached = time.Now()
			h.opts.Logger.LogAttrs(r.Context(), slog.LevelDebug, "receiver_attached",
				slog.String("id", s.ID), seconds("wait_s", tr.attached.Sub(s.Created)))
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
			// Before the deferred Abort with ErrSenderGone: the log must say expiry, not sender gone.
			s.Abort(relay.ErrExpired)
			drain = 0
			if web {
				res := webOutcome(outcomeExpired, 0, time.Since(s.Created))
				s.SetResult(res)
				writeWebOutcome(w, res)
				return
			}
			abortSender(w, r, fmt.Sprintf("\nERRORE: download NOT completed: no receiver connected "+
				"in %s, the link has been closed.\n", roundDuration(time.Since(s.Created))))
			return
		case <-cancelled:
			// The sender in the browser closed the page or cancelled (Cancel from the reservation's
			// stream): the unread body must no longer be awaited. The outcome is written by the stream.
			drain = 0
			return
		case <-binding:
			// A new PUT with the same token has taken this one's place (the sender came back before the
			// server saw the old attachment drop): the session is now its own.
			detached = true
			drain = 0
			return
		case <-r.Context().Done():
			senderLost()
			return
		case <-heartbeat:
			if _, err := io.WriteString(w, "\r"); err != nil {
				drain = 0
				senderLost()
				panic(http.ErrAbortHandler)
			}
			if err := rc.Flush(); err != nil {
				drain = 0
				senderLost()
				panic(http.ErrAbortHandler)
			}
		case <-peerCheck:
			if peerClosed(conn) {
				// outcome stays sender_gone; the defer records ErrSenderGone and frees the slug, unless
				// the tolerance (senderLost) applies.
				drain = 0
				senderLost()
				panic(http.ErrAbortHandler)
			}
		}
	}
}

// doneMessage is the last line for the sender on a successful transfer: all bytes have been
// written to the receiver. Saving to disk is verified by the receiver's program.
func doneMessage(n int64, d time.Duration) string {
	return fmt.Sprintf("\nOK: download completed, %s delivered to the receiver in %s.\n", sizeText(n), roundDuration(d))
}

// Reasons for an interrupted relay (failReason), in the outcome for the browser.
const (
	failReceiverGone   = "receiver_gone"
	failIdle           = "idle"
	failLengthMismatch = "length_mismatch"
	failSenderGone     = "sender_gone"
)

// failReason classifies the first error of the session of an interrupted relay.
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

// failedMessage is the last line for the sender when the relay is interrupted after n bytes. err is
// the first error of the session.
func failedMessage(err error, n, length int64, idle time.Duration) string {
	var why string
	switch failReason(err) {
	case failReceiverGone:
		why = "the receiver disconnected or stopped receiving"
	case failIdle:
		why = "no data from the sender for " + idle.String()
	case failLengthMismatch:
		why = "the sender sent fewer bytes than declared"
	default:
		why = "connection with the sender interrupted"
	}
	done := strconv.FormatInt(n, 10)
	if length >= 0 {
		done += " of " + strconv.FormatInt(length, 10)
	}
	return fmt.Sprintf("\nERRORE: download NOT completed after %s bytes: %s.\n", done, why)
}

// webOutcome is the last line of the response to an upload from the browser: the outcome in JSON, which the page
// shows in its own language. outcome is outcomeOK, outcomeExpired or a failReason reason; seconds is
// the relay's duration (the wait, for outcomeExpired).
func webOutcome(outcome string, n int64, d time.Duration) string {
	b, _ := json.Marshal(struct {
		Outcome string  `json:"outcome"`
		Bytes   int64   `json:"bytes"`
		Seconds float64 `json:"seconds"`
	}{outcome, n, math.Round(d.Seconds()*10) / 10})
	return "\n" + string(b) + "\n"
}

// abortSender writes msg to the sender and closes the response. On HTTP/1 it interrupts without a final
// chunk, so curl exits with an error: it first closes the write side (curl reads the message and
// then the end of the stream) and waits abortLinger before the RST. On HTTP/2 the stream ends
// normally: an h2c proxy (Apache) that receives a reset adds its own 502 page to the message
// and closes the response to the client without an error anyway.
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

// writeWebOutcome writes the outcome of an upload from the browser and lets the response end
// normally even on HTTP/1: for XMLHttpRequest a truncated response is only a network error,
// without the outcome. With the body unread, net/http then closes the connection for writing and waits
// before the reset, as in abortSender.
func writeWebOutcome(w http.ResponseWriter, msg string) {
	_, _ = io.WriteString(w, msg)
	_ = http.NewResponseController(w).Flush()
}

// sizeText renders n as "3000000 bytes (2.9 MiB)".
func sizeText(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " bytes"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%d bytes (%.1f %ciB)", n, float64(n)/float64(div), "KMGTPE"[exp])
}

// roundDuration rounds d to milliseconds below one second, to tenths of a second above.
func roundDuration(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(100 * time.Millisecond)
}

// copyUntilAbort unblocks both the read from the sender and a stalled write to the receiver.
func (h *handler) copyUntilAbort(s *relay.Session, rr *responseReceiver, body *relay.IdleReader) (int64, error) {
	copied := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-s.Aborted():
			body.Stop()
			rr.stop()
		case <-copied:
		}
	}()
	rr.start(s.Name, s.Length)
	var n int64
	err := rr.flushErr
	if err == nil {
		n, err = h.copier.Copy(deliveredCounter{rr, s}, body)
	}
	close(copied)
	<-watched
	if err == nil {
		err = rr.flushErr
	}
	return n, err
}

// deliveredCounter counts in the session the bytes written to the receiver, for the progress that the
// reservation's stream shows the sender in the browser.
type deliveredCounter struct {
	relay.Receiver
	s *relay.Session
}

func (c deliveredCounter) Write(p []byte) (int, error) {
	n, err := c.Receiver.Write(p)
	c.s.AddDelivered(int64(n))
	return n, err
}

// command is a download command: the tool and the line to run.
type command struct {
	Label string `json:"label"`
	Line  string `json:"line"`
}

// receiverCommands returns the download commands for the link u, one per tool (curl, wget,
// aria2c): the sender's table and the HTML pages take them from here. aria2c uses a single
// connection: the relay is a single stream and does not need byte ranges.
// Invariant: the source is never altered. If the receiver works in the sender's folder, the file with
// the same name is exactly the one being sent, and no command, nor any variant of it with
// fewer flags, must overwrite it (scripts/invariant.py checks this). For this reason the commands do not
// contain the name: an explicit name (curl -o, wget -O) truncates the file before any check,
// and busybox wget ignores -nc. The name comes from Content-Disposition: curl -OJ refuses to
// overwrite, while wget --content-disposition and aria2c save under another name (.1). Without -J or
// --content-disposition the name is the slug; without -O curl writes to stdout. -f avoids saving
// the error page; --allow-overwrite=false takes precedence over an aria2.conf that asks to overwrite.
func receiverCommands(u string) []command {
	return []command{
		{"curl", fmt.Sprintf("curl -f -OJ \"%s\"", u)},
		{"wget", fmt.Sprintf("wget --content-disposition \"%s\"", u)},
		{"aria2c", fmt.Sprintf("aria2c -x1 -s1 --allow-overwrite=false \"%s\"", u)},
	}
}

// commands returns the ASCII table printed to the sender: one line per tool with the
// download command (receiverCommands) and the direct line with only the link, without quotes, to
// paste into the browser: it opens the download page, and the file starts only from its button.
func commands(u string) string {
	cmds := append(receiverCommands(u), command{"direct", u})
	const (
		title = "bashtransfer: commands for the receiver"
		note  = "One receiver per link. Keep the terminal open until the end."
	)
	// Two columns (tool, command) as wide as the longest content; title and note span
	// the full width. If they are wider than the commands, the command column widens.
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

// Outcomes of a transfer (outcome field of the transfer event).
const (
	outcomeOK         = "ok"          // all bytes delivered to the receiver
	outcomeAborted    = "aborted"     // relay started and interrupted (sender, receiver, idle)
	outcomeExpired    = "expired"     // no receiver within BASHT_WAIT_TIMEOUT (or shutdown)
	outcomeSenderGone = "sender_gone" // the sender dropped before a receiver attached
)

// transferLog collects the data of the transfer event, recorded once per session at the end of the
// upload.
type transferLog struct {
	session  *relay.Session
	outcome  string
	attached time.Time // zero if no receiver attached
	bytes    int64
}

// log records the outcome: Info for ok, expiry and a sender that dropped while waiting (normal
// user behavior), Warn for a relay interrupted midway.
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

// seconds renders a duration as seconds with three decimals: readable in text and numeric in JSON
// (slog would write time.Duration in nanoseconds in JSON).
func seconds(key string, d time.Duration) slog.Attr {
	return slog.Float64(key, math.Round(d.Seconds()*1000)/1000)
}

// expectsContinue reports whether the client asked for 100 Continue with a non-empty body.
func expectsContinue(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Expect"), "100-continue") && r.ContentLength != 0
}

// download claims the slug and waits for the end of the transfer; the sender writes the relay.
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
	var s *relay.Session
	var err error
	if wantsHTML(r) {
		s, err = h.reg.ClaimReady(id, rcv)
	} else {
		s, err = h.reg.Claim(id, rcv)
	}
	switch {
	case errors.Is(err, relay.ErrSenderAway):
		h.landing(w, r, id)
		return
	case errors.Is(err, relay.ErrNotFound):
		h.reject(w, r, slog.LevelDebug, http.StatusNotFound, reasonNotFound, "404 page not found")
		return
	case errors.Is(err, relay.ErrClaimed):
		// The log keeps "gone"; the browser (button pressed twice) sees that the download has started.
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
			// Interrupts the response without a final chunk: the client sees an error (D8).
			panic(http.ErrAbortHandler)
		}
		writeError(w, r, http.StatusServiceUnavailable, pageAborted, "transfer aborted")
	}
}

// rangeFromStart reports whether the request is served from the first byte: no Range, a Range to
// ignore (with If-Range, never satisfied without validators; unit other than bytes; invalid
// syntax) or ranges that all start at 0. In these cases the relay responds 200 with the whole
// body, as RFC 9110 allows.
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
			// Suffix range (last N bytes): requires the end of the stream.
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

// rejectRange responds to a range request that does not start at the first byte (resume with
// curl -C -, wget -c, additional aria2 connections) without claiming the slug: the relay is a
// single stream, and a 200 with the whole body would consume the transfer. An unknown slug,
// completed or already claimed, responds like a normal GET. Not 416: curl -C - treats it as a
// file already complete and exits successfully, leaving the partial file.
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
