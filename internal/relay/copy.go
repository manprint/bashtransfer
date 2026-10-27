package relay

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
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
// scadenza di lettura con setDeadline; una lettura scaduta restituisce ErrIdle.
func NewIdleReader(r io.Reader, idle time.Duration, setDeadline func(time.Time) error) io.Reader {
	return &idleReader{r: r, idle: idle, setDeadline: setDeadline}
}

type idleReader struct {
	r           io.Reader
	idle        time.Duration
	setDeadline func(time.Time) error
}

func (d *idleReader) Read(p []byte) (int, error) {
	if err := d.setDeadline(time.Now().Add(d.idle)); err != nil {
		return 0, err
	}
	n, err := d.r.Read(p)
	var ne net.Error
	if errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return n, ErrIdle
	}
	return n, err
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
