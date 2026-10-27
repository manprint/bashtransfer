package server

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"bashtransfer/internal/relay"
)

// benchSize è il trasferimento per iterazione (D13).
const benchSize = 256 << 20

// benchBufSize legge il buffer da BASHT_BENCH_BUF (byte); default 256 KiB.
// Relay e baseline usano lo stesso valore.
func benchBufSize(tb testing.TB) int {
	tb.Helper()
	v := os.Getenv("BASHT_BENCH_BUF")
	if v == "" {
		return 256 << 10
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		tb.Fatalf("BASHT_BENCH_BUF %q non valido", v)
	}
	return n
}

// cyclicReader genera byte da un buffer ciclico: nessun costo di generazione per byte.
type cyclicReader struct {
	buf []byte
	off int
}

func (c *cyclicReader) Read(p []byte) (int, error) {
	n := copy(p, c.buf[c.off:])
	c.off = (c.off + n) % len(c.buf)
	return n, nil
}

func benchPattern() []byte {
	p := make([]byte, 1<<20)
	for i := range p {
		p[i] = byte(i % 251)
	}
	return p
}

// receiverPath ricava il percorso del ricevente dalla risposta del PUT.
type receiverPath func(br *bufio.Reader) (string, error)

// slugPath cerca la riga curl della risposta del relay e ne prende lo slug.
func slugPath(br *bufio.Reader) (string, error) {
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(line, "curl -o") {
			return "/" + slugOf(line), nil
		}
	}
}

// fixedPath è il percorso del ricevente del baseline, che non ha slug.
func fixedPath(p string) receiverPath {
	return func(*bufio.Reader) (string, error) { return p, nil }
}

// transfer invia size byte con PUT e li riceve con GET; verifica il conteggio esatto.
func transfer(tb testing.TB, base string, recv receiverPath, size int64) {
	tb.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/bench.bin", io.LimitReader(&cyclicReader{buf: benchPattern()}, size))
	if err != nil {
		tb.Fatal(err)
	}
	req.ContentLength = size
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		tb.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tb.Fatalf("PUT stato %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	path, err := recv(br)
	if err != nil {
		tb.Fatalf("percorso ricevente: %v", err)
	}
	dl, err := http.Get(base + path)
	if err != nil {
		tb.Fatal(err)
	}
	n, err := io.Copy(io.Discard, dl.Body)
	dl.Body.Close()
	if err != nil {
		tb.Fatalf("download: %v", err)
	}
	if n != size {
		tb.Fatalf("ricevuti %d byte, attesi %d", n, size)
	}
	if _, err := io.Copy(io.Discard, br); err != nil {
		tb.Fatalf("fine PUT: %v", err)
	}
}

// minimalRelay è il baseline di D13: stessa catena a due hop, stesso buffer e flush per blocco,
// senza registry, slug, comandi o header extra.
type minimalRelay struct {
	buf    int
	attach chan handover
}

type handover struct {
	w    http.ResponseWriter
	done chan struct{}
}

func newMinimalRelay(buf int) *minimalRelay {
	return &minimalRelay{buf: buf, attach: make(chan handover)}
}

func (m *minimalRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	switch r.Method {
	case http.MethodPut:
		if err := rc.EnableFullDuplex(); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = rc.Flush()
		select {
		case h := <-m.attach:
			_ = copyFlush(h.w, r.Body, make([]byte, m.buf))
			close(h.done)
		case <-r.Context().Done():
		}
	case http.MethodGet:
		h := handover{w: w, done: make(chan struct{})}
		select {
		case m.attach <- h:
			<-h.done
		case <-r.Context().Done():
		}
	}
}

// copyFlush copia src in dst con flush dopo ogni scrittura, come relay.Copier.
func copyFlush(dst http.ResponseWriter, src io.Reader, buf []byte) error {
	rc := http.NewResponseController(dst)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			if err := rc.Flush(); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

func BenchmarkRelayLoopback(b *testing.B) {
	srv := httptest.NewServer(New(relay.NewRegistry(100, time.Minute), relay.NewCopier(benchBufSize(b)), Options{}))
	b.Cleanup(srv.Close)
	b.SetBytes(benchSize)
	for i := 0; i < b.N; i++ {
		transfer(b, srv.URL, slugPath, benchSize)
	}
}

func BenchmarkBaselineRelay(b *testing.B) {
	srv := httptest.NewServer(newMinimalRelay(benchBufSize(b)))
	b.Cleanup(srv.Close)
	b.SetBytes(benchSize)
	for i := 0; i < b.N; i++ {
		transfer(b, srv.URL, fixedPath("/"), benchSize)
	}
}

// TestBenchHarnessSmoke: una iterazione per lato con 1 MiB; protegge il benchmark da misurare zero byte.
func TestBenchHarnessSmoke(t *testing.T) {
	const size = 1 << 20
	srv := httptest.NewServer(New(relay.NewRegistry(100, time.Minute), relay.NewCopier(256<<10), Options{}))
	t.Cleanup(srv.Close)
	transfer(t, srv.URL, slugPath, size)

	base := httptest.NewServer(newMinimalRelay(256 << 10))
	t.Cleanup(base.Close)
	transfer(t, base.URL, fixedPath("/"), size)
}
