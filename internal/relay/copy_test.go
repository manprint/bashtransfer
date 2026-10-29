package relay

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

type countRcv struct {
	writes, flushes int
	got             bytes.Buffer
}

func (c *countRcv) Write(p []byte) (int, error) {
	c.writes++
	return c.got.Write(p)
}

func (c *countRcv) Flush() { c.flushes++ }

// plainReader nasconde io.WriterTo, così io.CopyBuffer usa il buffer del Copier.
type plainReader struct{ io.Reader }

type discardRcv struct{}

func (discardRcv) Write(p []byte) (int, error) { return len(p), nil }
func (discardRcv) Flush()                      {}

func TestCopyFlushesEachWrite(t *testing.T) {
	src := make([]byte, 1<<20)
	if _, err := rand.Read(src); err != nil {
		t.Fatal(err)
	}
	dst := &countRcv{}
	if _, err := NewCopier(64<<10).Copy(dst, plainReader{bytes.NewReader(src)}); err != nil {
		t.Fatal(err)
	}
	if dst.writes == 0 {
		t.Fatal("nessuna scrittura")
	}
	if dst.flushes != dst.writes {
		t.Fatalf("flush %d, scritture %d: attesi uguali", dst.flushes, dst.writes)
	}
}

func TestCopyRoundTripBytes(t *testing.T) {
	src := make([]byte, 1<<20)
	if _, err := rand.Read(src); err != nil {
		t.Fatal(err)
	}
	dst := &countRcv{}
	n, err := NewCopier(256<<10).Copy(dst, plainReader{bytes.NewReader(src)})
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(src)) || !bytes.Equal(dst.got.Bytes(), src) {
		t.Fatalf("copia diversa: %d byte su %d", n, len(src))
	}
}

func TestCopyAllocsBounded(t *testing.T) {
	const total = 16 << 20
	src := make([]byte, total)
	c := NewCopier(256 << 10)
	allocs := testing.AllocsPerRun(5, func() {
		if _, err := c.Copy(discardRcv{}, plainReader{bytes.NewReader(src)}); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 20 {
		t.Fatalf("allocazioni per copia da 16 MiB: %.0f, attese <= 20", allocs)
	}
}

type fakeConnReader struct {
	data     []byte
	err      error
	deadline time.Time
}

func (f *fakeConnReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func (f *fakeConnReader) setDeadline(t time.Time) error { f.deadline = t; return nil }

// TestIdleReaderSetsDeadlineBeforeEachRead: la scadenza è rinnovata a ogni Read.
func TestIdleReaderSetsDeadlineBeforeEachRead(t *testing.T) {
	f := &fakeConnReader{data: []byte("abc"), err: io.EOF}
	r := NewIdleReader(f, time.Minute, f.setDeadline)
	before := time.Now()
	buf := make([]byte, 2)
	if _, err := r.Read(buf); err != nil {
		t.Fatal(err)
	}
	if d := f.deadline.Sub(before); d < 59*time.Second || d > 61*time.Second {
		t.Fatalf("scadenza a %s, attesa ~1m", d)
	}
}

// TestIdleReaderMapsTimeoutToErrIdle: timeout di rete e os.ErrDeadlineExceeded diventano ErrIdle;
// EOF e altri errori passano invariati; un errore di setDeadline è propagato senza leggere.
func TestIdleReaderMapsTimeoutToErrIdle(t *testing.T) {
	timeout := &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}
	for _, e := range []error{os.ErrDeadlineExceeded, timeout} {
		f := &fakeConnReader{err: e}
		if _, err := NewIdleReader(f, time.Second, f.setDeadline).Read(make([]byte, 1)); !errors.Is(err, ErrIdle) {
			t.Errorf("errore %v: ottenuto %v, atteso ErrIdle", e, err)
		}
	}
	f := &fakeConnReader{err: io.EOF}
	if _, err := NewIdleReader(f, time.Second, f.setDeadline).Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("EOF: ottenuto %v", err)
	}
	other := errors.New("reset")
	f = &fakeConnReader{err: other}
	if _, err := NewIdleReader(f, time.Second, f.setDeadline).Read(make([]byte, 1)); err != other {
		t.Errorf("errore generico: ottenuto %v", err)
	}
	setErr := errors.New("no deadline")
	f = &fakeConnReader{data: []byte("x")}
	if n, err := NewIdleReader(f, time.Second, func(time.Time) error { return setErr }).Read(make([]byte, 1)); n != 0 || err != setErr {
		t.Errorf("setDeadline in errore: n=%d err=%v", n, err)
	}
}

type failRcv struct{ after int }

func (f *failRcv) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, errors.New("ricevente chiuso")
	}
	f.after--
	return len(p), nil
}
func (f *failRcv) Flush() {}

// TestCopyStopsOnReceiverError: un errore di scrittura interrompe la copia e viene restituito.
func TestCopyStopsOnReceiverError(t *testing.T) {
	src := make([]byte, 1<<20)
	n, err := NewCopier(64<<10).Copy(&failRcv{after: 2}, plainReader{bytes.NewReader(src)})
	if err == nil {
		t.Fatal("atteso errore dal ricevente")
	}
	if n != 2*(64<<10) {
		t.Fatalf("copiati %d byte, attesi %d", n, 2*(64<<10))
	}
}

// TestIdleReaderStopInterruptsRead: Stop sblocca una Read in attesa di dati, con e senza
// scadenza di inattività, e le letture successive falliscono con ErrInterrupted.
func TestIdleReaderStopInterruptsRead(t *testing.T) {
	for _, idle := range []time.Duration{0, time.Minute} {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		r := NewIdleReader(c1, idle, c1.SetReadDeadline)
		done := make(chan error, 1)
		go func() {
			_, err := r.Read(make([]byte, 16))
			done <- err
		}()
		time.Sleep(50 * time.Millisecond)
		r.Stop()
		select {
		case err := <-done:
			if !errors.Is(err, ErrInterrupted) {
				t.Fatalf("idle %s: Read dopo Stop: %v, atteso ErrInterrupted", idle, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("idle %s: Stop non sblocca la Read", idle)
		}
		go func() { _, _ = c2.Write([]byte("x")) }()
		if _, err := r.Read(make([]byte, 1)); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("idle %s: Read successiva: %v, atteso ErrInterrupted", idle, err)
		}
	}
}

// TestIdleReaderStopKeepsEOF: dopo Stop, una lettura che si conclude senza toccare la connessione
// (fine di un body con Content-Length) restituisce dati ed EOF, non un'interruzione.
func TestIdleReaderStopKeepsEOF(t *testing.T) {
	f := &fakeConnReader{data: []byte("ab"), err: io.EOF}
	r := NewIdleReader(f, time.Minute, f.setDeadline)
	r.Stop()
	if !f.deadline.Before(time.Now()) {
		t.Fatalf("Stop: scadenza %v, attesa nel passato", f.deadline)
	}
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "ab" {
		t.Fatalf("ReadAll dopo Stop: %q, %v", got, err)
	}
	if !f.deadline.Before(time.Now()) {
		t.Fatalf("Read dopo Stop ha rinnovato la scadenza: %v", f.deadline)
	}
}
