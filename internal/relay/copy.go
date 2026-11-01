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

// Copier copies an upload's body to a Receiver using reusable buffers (D13, I-1).
type Copier struct {
	pool sync.Pool
}

// NewCopier creates a Copier with buffers of size bytes.
func NewCopier(size int) *Copier {
	c := &Copier{}
	c.pool.New = func() any {
		b := make([]byte, size)
		return &b
	}
	return c
}

// Copy copies src to dst, flushing after each successful write.
func (c *Copier) Copy(dst Receiver, src io.Reader) (int64, error) {
	bp := c.pool.Get().(*[]byte)
	defer c.pool.Put(bp)
	return io.CopyBuffer(&flushWriter{dst: dst}, src, *bp)
}

// NewIdleReader limits the sender's inactivity (D15): before each Read it sets the
// read deadline with setDeadline; an expired read returns ErrIdle. With idle <= 0
// it sets no deadlines and only serves Stop.
func NewIdleReader(r io.Reader, idle time.Duration, setDeadline func(time.Time) error) *IdleReader {
	return &IdleReader{r: r, idle: idle, setDeadline: setDeadline}
}

// IdleReader is the reader returned by NewIdleReader.
type IdleReader struct {
	r           io.Reader
	idle        time.Duration
	setDeadline func(time.Time) error
	stopped     atomic.Bool
}

// aLongTimeAgo is a deadline already in the past: it immediately unblocks an ongoing Read.
var aLongTimeAgo = time.Unix(1, 0)

func (d *IdleReader) Read(p []byte) (int, error) {
	if d.idle > 0 {
		if err := d.setDeadline(time.Now().Add(d.idle)); err != nil {
			return 0, err
		}
	}
	// A concurrent Stop may have set the past deadline before the one just renewed:
	// it is restored. No error is returned right away, because the read can still
	// complete without touching the connection (EOF of a Content-Length body).
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

// Stop unblocks a Read waiting for data and makes subsequent reads from the connection fail
// with ErrInterrupted. It may be called from another goroutine during a Read.
func (d *IdleReader) Stop() {
	d.stopped.Store(true)
	_ = d.setDeadline(aLongTimeAgo)
}

// flushWriter flushes the Receiver after each Write that wrote bytes.
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
