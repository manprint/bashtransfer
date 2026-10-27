package server

import (
	"net/http"
	"strconv"
)

// responseReceiver adatta la risposta HTTP del ricevente a relay.Receiver.
// start viene chiamata solo dalla goroutine del mittente, prima della copia.
// started è letto dal ricevente solo dopo Finished (happens-before via chiusura del canale).
type responseReceiver struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	started bool
}

func newResponseReceiver(w http.ResponseWriter) *responseReceiver {
	return &responseReceiver{w: w, rc: http.NewResponseController(w)}
}

// start imposta gli header di download e invia 200 con flush.
func (r *responseReceiver) start(name string, length int64) {
	if r.started {
		return
	}
	r.started = true
	h := r.w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if length >= 0 {
		h.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	r.w.WriteHeader(http.StatusOK)
	r.Flush()
}

func (r *responseReceiver) Write(p []byte) (int, error) {
	return r.w.Write(p)
}

func (r *responseReceiver) Flush() {
	_ = r.rc.Flush()
}
