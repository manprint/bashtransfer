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

// benchSize is the transfer per iteration (D13).
const benchSize = 256 << 20

// benchBufSize reads the buffer from BASHT_BENCH_BUF (bytes); default 256 KiB.
// Relay and baseline use the same value.
func benchBufSize(tb testing.TB) int {
	tb.Helper()
	v := os.Getenv("BASHT_BENCH_BUF")
	if v == "" {
		return 256 << 10
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		tb.Fatalf("BASHT_BENCH_BUF %q invalid", v)
	}
	return n
}

// cyclicReader generates bytes from a cyclic buffer: no per-byte generation cost.
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

// receiverPath derives the receiver's path from the PUT response.
type receiverPath func(br *bufio.Reader) (string, error)

// slugPath looks for the curl line in the relay response and takes its slug.
func slugPath(br *bufio.Reader) (string, error) {
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}
		if c := cmdOf(line); strings.HasPrefix(c, "curl ") {
			return "/" + slugOf(c), nil
		}
	}
}

// fixedPath is the baseline receiver's path, which has no slug.
func fixedPath(p string) receiverPath {
	return func(*bufio.Reader) (string, error) { return p, nil }
}

// transfer sends size bytes with PUT and receives them with GET; it verifies the exact count.
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
		tb.Fatalf("PUT status %d", resp.StatusCode)
	}
	br := bufio.NewReader(resp.Body)
	path, err := recv(br)
	if err != nil {
		tb.Fatalf("receiver path: %v", err)
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
		tb.Fatalf("received %d bytes, expected %d", n, size)
	}
	if _, err := io.Copy(io.Discard, br); err != nil {
		tb.Fatalf("end of PUT: %v", err)
	}
}

// minimalRelay is the D13 baseline: same two-hop chain, same buffer and per-block flush,
// without registry, slug, commands or extra headers.
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

// copyFlush copies src to dst, flushing after each write, like relay.Copier.
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

// TestBenchHarnessSmoke: one iteration per side with 1 MiB; protects the benchmark from measuring zero bytes.
func TestBenchHarnessSmoke(t *testing.T) {
	const size = 1 << 20
	srv := httptest.NewServer(New(relay.NewRegistry(100, time.Minute), relay.NewCopier(256<<10), Options{}))
	t.Cleanup(srv.Close)
	transfer(t, srv.URL, slugPath, size)

	base := httptest.NewServer(newMinimalRelay(256 << 10))
	t.Cleanup(base.Close)
	transfer(t, base.URL, fixedPath("/"), size)
}
