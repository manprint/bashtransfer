package relay

import (
	"io"
	"sync"
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
