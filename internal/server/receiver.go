package server

import (
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bashtransfer/internal/relay"
)

// responseReceiver adapts the receiver's HTTP response to relay.Receiver.
// start is called only from the sender's goroutine, before the copy.
// started is read by the receiver only after Finished (happens-before via channel close).
type responseReceiver struct {
	w  http.ResponseWriter
	rc *http.ResponseController
	// writeTimeout, if > 0, is the maximum time for a single write to the receiver:
	// a connected receiver that does not read would otherwise block the sender and the session forever.
	writeTimeout time.Duration
	mu           sync.Mutex
	started      bool
	stopped      atomic.Bool
	// The flush error is read and written only by the goroutine copying the file.
	flushErr error
}

func newResponseReceiver(w http.ResponseWriter, writeTimeout time.Duration) *responseReceiver {
	return &responseReceiver{w: w, rc: http.NewResponseController(w), writeTimeout: writeTimeout}
}

// start sets the download headers and sends 200 with a flush.
func (r *responseReceiver) start(name string, length int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return
	}
	r.started = true
	h := r.w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", contentDisposition(name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	// The relay does not serve ranges: aria2 and download managers do not open additional connections.
	h.Set("Accept-Ranges", "none")
	if length >= 0 {
		h.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	r.armWriteDeadline()
	r.w.WriteHeader(http.StatusOK)
	r.Flush()
}

func (r *responseReceiver) Write(p []byte) (int, error) {
	if r.flushErr != nil {
		return 0, r.flushErr
	}
	if r.stopped.Load() {
		return 0, relay.ErrReceiverGone
	}
	r.armWriteDeadline()
	n, err := r.w.Write(p)
	if err != nil {
		// A failed write always depends on the receiver: disconnected, or stalled beyond writeTimeout.
		err = fmt.Errorf("%w: %w", relay.ErrReceiverGone, err)
	}
	return n, err
}

func (r *responseReceiver) Flush() {
	if r.flushErr == nil {
		if err := r.rc.Flush(); err != nil {
			r.flushErr = fmt.Errorf("%w: %w", relay.ErrReceiverGone, err)
		}
	}
}

func (r *responseReceiver) stop() {
	r.stopped.Store(true)
	_ = r.rc.SetWriteDeadline(time.Unix(1, 0))
}

func (r *responseReceiver) armWriteDeadline() {
	if r.writeTimeout > 0 {
		_ = r.rc.SetWriteDeadline(time.Now().Add(r.writeTimeout))
	}
	if r.stopped.Load() {
		_ = r.rc.SetWriteDeadline(time.Unix(1, 0))
	}
}

// clearWriteDeadline removes the deadline left by the last write: the connection may be
// reused (keep-alive) and a past deadline would make the next response fail.
func (r *responseReceiver) clearWriteDeadline() {
	if r.writeTimeout > 0 || r.stopped.Load() {
		_ = r.rc.SetWriteDeadline(time.Time{})
	}
}

// contentDisposition encodes the name (RFC 6266): ASCII in quotes, otherwise filename*.
// With filename* it first adds filename="..." with the UTF-8 bytes of the name: curl -OJ reads only
// filename (without it, it would save the file under the slug name), while browsers prefer filename*.
// The name is already cleaned by SanitizeName: no quotes, backslashes or control characters.
func contentDisposition(name string) string {
	v := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if v == "" {
		return "attachment"
	}
	if rest, ok := strings.CutPrefix(v, "attachment; filename*="); ok {
		return `attachment; filename="` + name + `"; filename*=` + rest
	}
	return v
}
