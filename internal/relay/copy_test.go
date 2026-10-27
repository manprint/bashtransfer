package relay

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
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
