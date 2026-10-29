package relay

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Copier copia il body di un upload verso un Receiver con buffer riusabili (D13, I-1).
type Copier struct {
	pool sync.Pool
}

// NewCopier crea un Copier con buffer di size byte.
func NewCopier(size int) *Copier {
	c := &Copier{}
	c.pool.New = func() any {
		b := make([]byte, size)
		return &b
	}
	return c
}

// Copy copia src in dst con flush dopo ogni scrittura riuscita.
func (c *Copier) Copy(dst Receiver, src io.Reader) (int64, error) {
	bp := c.pool.Get().(*[]byte)
	defer c.pool.Put(bp)
	return io.CopyBuffer(&flushWriter{dst: dst}, src, *bp)
}

// NewIdleReader limita l'inattività del mittente (D15): prima di ogni Read imposta la
// scadenza di lettura con setDeadline; una lettura scaduta restituisce ErrIdle. Con idle <= 0
// non imposta scadenze e serve solo a Stop.
func NewIdleReader(r io.Reader, idle time.Duration, setDeadline func(time.Time) error) *IdleReader {
	return &IdleReader{r: r, idle: idle, setDeadline: setDeadline}
}

// IdleReader è il reader restituito da NewIdleReader.
type IdleReader struct {
	r           io.Reader
	idle        time.Duration
	setDeadline func(time.Time) error
	stopped     atomic.Bool
}

// aLongTimeAgo è una scadenza già passata: sblocca subito una Read in corso.
var aLongTimeAgo = time.Unix(1, 0)

func (d *IdleReader) Read(p []byte) (int, error) {
	if d.idle > 0 {
		if err := d.setDeadline(time.Now().Add(d.idle)); err != nil {
			return 0, err
		}
	}
	// Uno Stop concorrente può aver impostato la scadenza passata prima di quella appena
	// rinnovata: la si ripristina. Non si restituisce subito errore, perché la lettura può
	// ancora concludersi senza toccare la connessione (EOF di un body con Content-Length).
	if d.stopped.Load() {
		_ = d.setDeadline(aLongTimeAgo)
	}
	n, err := d.r.Read(p)
	if err == nil || err == io.EOF {
		return n, err
	}
	if d.stopped.Load() {
		return n, ErrInterrupted
	}
	var ne net.Error
	if errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return n, ErrIdle
	}
	return n, err
}

// Stop sblocca una Read in attesa di dati e fa fallire le successive letture dalla connessione
// con ErrInterrupted. Può essere chiamata da un'altra goroutine durante una Read.
func (d *IdleReader) Stop() {
	d.stopped.Store(true)
	_ = d.setDeadline(aLongTimeAgo)
}

// flushWriter fa flush del Receiver dopo ogni Write che ha scritto byte.
type flushWriter struct {
	dst Receiver
}

func (w *flushWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.dst.Flush()
	}
	return n, err
}
