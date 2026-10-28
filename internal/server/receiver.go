package server

import (
	"mime"
	"net/http"
	"strconv"
	"time"
)

// responseReceiver adatta la risposta HTTP del ricevente a relay.Receiver.
// start viene chiamata solo dalla goroutine del mittente, prima della copia.
// started è letto dal ricevente solo dopo Finished (happens-before via chiusura del canale).
type responseReceiver struct {
	w  http.ResponseWriter
	rc *http.ResponseController
	// writeTimeout, se > 0, è il tempo massimo per una singola scrittura verso il ricevente:
	// un ricevente connesso che non legge bloccherebbe altrimenti mittente e sessione senza fine.
	writeTimeout time.Duration
	started      bool
}

func newResponseReceiver(w http.ResponseWriter, writeTimeout time.Duration) *responseReceiver {
	return &responseReceiver{w: w, rc: http.NewResponseController(w), writeTimeout: writeTimeout}
}

// start imposta gli header di download e invia 200 con flush.
func (r *responseReceiver) start(name string, length int64) {
	if r.started {
		return
	}
	r.started = true
	h := r.w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", contentDisposition(name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if length >= 0 {
		h.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	r.armWriteDeadline()
	r.w.WriteHeader(http.StatusOK)
	r.Flush()
}

func (r *responseReceiver) Write(p []byte) (int, error) {
	r.armWriteDeadline()
	return r.w.Write(p)
}

func (r *responseReceiver) Flush() {
	_ = r.rc.Flush()
}

func (r *responseReceiver) armWriteDeadline() {
	if r.writeTimeout > 0 {
		_ = r.rc.SetWriteDeadline(time.Now().Add(r.writeTimeout))
	}
}

// clearWriteDeadline toglie la scadenza lasciata dall'ultima scrittura: la connessione può essere
// riusata (keep-alive) e un deadline passato farebbe fallire la risposta successiva.
func (r *responseReceiver) clearWriteDeadline() {
	if r.writeTimeout > 0 {
		_ = r.rc.SetWriteDeadline(time.Time{})
	}
}

// contentDisposition codifica il nome (RFC 6266): ASCII tra virgolette, altrimenti filename*.
func contentDisposition(name string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return "attachment"
}
