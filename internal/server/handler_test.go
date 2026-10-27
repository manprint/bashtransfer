package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bashtransfer/internal/relay"
)

func newTestServer(t *testing.T) (*httptest.Server, *relay.Registry) {
	t.Helper()
	return newTestServerWait(t, time.Hour)
}

// newTestServerWait usa wait come timeout di attesa del ricevente (D7).
// Un mittente senza ricevente e con body non letto non viene rilevato come caduto
// dal server Go: la scadenza dell'attesa è l'unico modo per chiudere il suo handler.
func newTestServerWait(t *testing.T, wait time.Duration) (*httptest.Server, *relay.Registry) {
	t.Helper()
	reg := relay.NewRegistry(100, wait)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{}))
	t.Cleanup(srv.Close)
	return srv, reg
}

// within esegue fn e fallisce il test se non termina entro d.
func within[T any](t *testing.T, d time.Duration, what string, fn func() T) T {
	t.Helper()
	ch := make(chan T, 1)
	go func() { ch <- fn() }()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("%s: nessuna risposta entro %s", what, d)
	}
	var zero T
	return zero
}

// slugOf estrae lo slug dall'ultima parte dell'URL in una riga di comando.
func slugOf(line string) string {
	line = strings.TrimSuffix(strings.TrimSpace(line), `"`)
	return line[strings.LastIndex(line, "/")+1:]
}

type upload struct {
	resp     *http.Response
	br       *bufio.Reader
	curlLine string
	wgetLine string
	slug     string
}

// openUpload invia PUT e legge i due comandi della prima risposta.
func openUpload(t *testing.T, ctx context.Context, url string, body io.Reader) *upload {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		t.Fatal(err)
	}
	resp := within(t, 5*time.Second, "risposta PUT", func() *http.Response {
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return nil
		}
		return r
	})
	if resp == nil {
		t.FailNow()
	}
	br := bufio.NewReader(resp.Body)
	curl := within(t, 5*time.Second, "riga curl", func() string {
		l, _ := br.ReadString('\n')
		return l
	})
	wget := within(t, 5*time.Second, "riga wget", func() string {
		l, _ := br.ReadString('\n')
		return l
	})
	return &upload{resp: resp, br: br, curlLine: curl, wgetLine: wget, slug: slugOf(curl)}
}

// finish legge il resto della risposta del mittente e verifica lo stato 200.
func (u *upload) finish(t *testing.T) {
	t.Helper()
	defer u.resp.Body.Close()
	within(t, 10*time.Second, "fine risposta PUT", func() error {
		_, err := io.ReadAll(u.br)
		return err
	})
	if u.resp.StatusCode != http.StatusOK {
		t.Fatalf("stato PUT %d, atteso 200", u.resp.StatusCode)
	}
}

func get(t *testing.T, ctx context.Context, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := within(t, 10*time.Second, "GET "+url, func() *http.Response {
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return nil
		}
		return r
	})
	if resp == nil {
		t.FailNow()
	}
	return resp
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUploadReturnsCommandsBeforeBody(t *testing.T) {
	srv, _ := newTestServerWait(t, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()

	u := openUpload(t, ctx, srv.URL+"/", pr)
	if !strings.HasPrefix(u.curlLine, `curl -o "`) {
		t.Errorf("riga curl: %q", u.curlLine)
	}
	if !strings.HasPrefix(u.wgetLine, `wget -O "`) {
		t.Errorf("riga wget: %q", u.wgetLine)
	}
	if len(u.slug) != 10 {
		t.Errorf("slug %q non di 10 caratteri", u.slug)
	}
}

func TestRelayRoundTripSHA(t *testing.T) {
	for _, size := range []int{0, 1, 1 << 20, 16 << 20} {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			data := randomBytes(t, size)
			want := sha256.Sum256(data)
			srv, reg := newTestServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			u := openUpload(t, ctx, srv.URL+"/file.bin", bytes.NewReader(data))
			dl := get(t, ctx, srv.URL+"/"+u.slug)
			if dl.StatusCode != http.StatusOK {
				t.Fatalf("download stato %d", dl.StatusCode)
			}
			h := sha256.New()
			n, err := io.Copy(h, dl.Body)
			dl.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if n != int64(size) || !bytes.Equal(h.Sum(nil), want[:]) {
				t.Fatalf("byte %d su %d, sha256 diverso", n, size)
			}
			u.finish(t)

			again := get(t, ctx, srv.URL+"/"+u.slug)
			again.Body.Close()
			if again.StatusCode != http.StatusGone {
				t.Errorf("secondo download stato %d, atteso 410", again.StatusCode)
			}
			if reg.Len() != 0 {
				t.Errorf("sessioni vive %d, attese 0", reg.Len())
			}
		})
	}
}

// TestUploadExpectContinue: client con "Expect: 100-continue" (come curl per upload grandi).
// Il body deve arrivare intero; senza il 100 esplicito Go chiude il body alla risposta.
func TestUploadExpectContinue(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	data := randomBytes(t, 1<<20)
	want := sha256.Sum256(data)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/exp.bin", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Expect", "100-continue")
	resp := within(t, 5*time.Second, "risposta PUT con Expect", func() *http.Response {
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return nil
		}
		return r
	})
	if resp == nil {
		t.FailNow()
	}
	br := bufio.NewReader(resp.Body)
	line := within(t, 5*time.Second, "riga curl con Expect", func() string {
		l, _ := br.ReadString('\n')
		return l
	})
	dl := get(t, ctx, srv.URL+"/"+slugOf(line))
	h := sha256.New()
	n, err := io.Copy(h, dl.Body)
	dl.Body.Close()
	if err != nil {
		t.Fatalf("download con Expect: %v", err)
	}
	if n != int64(len(data)) || !bytes.Equal(h.Sum(nil), want[:]) {
		t.Fatalf("ricevuti %d byte su %d, sha256 diverso", n, len(data))
	}
	resp.Body.Close()
}

func TestGetUnknown404(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, p := range []string{"/ABCDEFGHIJ", "/bad"} {
		resp := get(t, ctx, srv.URL+p)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s stato %d, atteso 404", p, resp.StatusCode)
		}
	}
}

func TestGetSecondClaim410(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()

	u := openUpload(t, ctx, srv.URL+"/x.bin", pr)
	// La prima GET riceve gli header solo dopo l'aggancio e lo start del relay.
	first := get(t, ctx, srv.URL+"/"+u.slug)
	defer first.Body.Close()
	second := get(t, ctx, srv.URL+"/"+u.slug)
	second.Body.Close()
	if second.StatusCode != http.StatusGone {
		t.Fatalf("secondo claim stato %d, atteso 410", second.StatusCode)
	}

	payload := []byte("payload")
	go func() {
		_, _ = pw.Write(payload)
		_ = pw.Close()
	}()
	got := within(t, 10*time.Second, "lettura primo download", func() []byte {
		b, _ := io.ReadAll(first.Body)
		return b
	})
	if !bytes.Equal(got, payload) {
		t.Fatalf("primo download %q, atteso %q", got, payload)
	}
	u.finish(t)
}

func TestSenderAbortFailsReceiver(t *testing.T) {
	srv, _ := newTestServer(t)
	addr := srv.Listener.Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const total = 16 << 20
	fmt.Fprintf(conn, "PUT /x.bin HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n", addr, total)

	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	slug := ""
	for slug == "" {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("lettura comandi: %v", err)
		}
		if strings.HasPrefix(strings.TrimSpace(line), "curl -o") {
			slug = slugOf(line)
		}
	}
	_ = conn.SetReadDeadline(time.Time{})

	type result struct {
		n   int64
		err error
	}
	recv := make(chan result, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/" + slug)
		if err != nil {
			recv <- result{err: err}
			return
		}
		defer resp.Body.Close()
		n, err := io.Copy(io.Discard, resp.Body)
		recv <- result{n: n, err: err}
	}()

	if _, err := conn.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	select {
	case r := <-recv:
		if r.err == nil {
			t.Fatalf("il ricevente non ha visto errore con %d byte", r.n)
		}
		if r.n >= total {
			t.Fatalf("ricevuti %d byte, attesi meno di %d", r.n, total)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("il ricevente non termina dopo l'abort del mittente")
	}
}

func TestReceiverAbortCleansSession(t *testing.T) {
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	writeErr := make(chan error, 1)
	block := make([]byte, 64<<10)
	go func() {
		for {
			if _, err := pw.Write(block); err != nil {
				writeErr <- err
				return
			}
		}
	}()
	u := openUpload(t, ctx, srv.URL+"/big.bin", pr)

	rctx, rcancel := context.WithCancel(context.Background())
	dl := get(t, rctx, srv.URL+"/"+u.slug)
	if _, err := io.CopyN(io.Discard, dl.Body, 1<<20); err != nil {
		t.Fatalf("lettura ricevente: %v", err)
	}
	rcancel()
	dl.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for reg.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reg.Len() != 0 {
		t.Fatalf("sessioni vive %d dopo abort del ricevente, attese 0", reg.Len())
	}
	select {
	case <-writeErr:
	case <-time.After(5 * time.Second):
		t.Fatal("la scrittura del mittente non è fallita")
	}
}

func TestUploadMethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Post(srv.URL+"/", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST stato %d, atteso 405", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Allow"), "PUT") {
		t.Fatalf("Allow %q, atteso PUT", resp.Header.Get("Allow"))
	}
}

func TestUploadUnknownLength(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, size := range []int{1 << 20, 16 << 20} {
		data := randomBytes(t, size)
		want := sha256.Sum256(data)
		pr, pw := io.Pipe()
		go func() {
			_, err := pw.Write(data)
			_ = pw.CloseWithError(err)
		}()
		u := openUpload(t, ctx, srv.URL+"/mydir.tgz", pr)

		dl := get(t, ctx, srv.URL+"/"+u.slug)
		if dl.ContentLength != -1 || dl.Header.Get("Content-Length") != "" {
			t.Errorf("download con lunghezza nota: CL=%d header=%q", dl.ContentLength, dl.Header.Get("Content-Length"))
		}
		h := sha256.New()
		n, err := io.Copy(h, dl.Body)
		dl.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if n != int64(size) || !bytes.Equal(h.Sum(nil), want[:]) {
			t.Fatalf("size %d: ricevuti %d byte, sha256 diverso", size, n)
		}
		u.finish(t)
	}
}

func TestUploadNameFromPath(t *testing.T) {
	srv, _ := newTestServerWait(t, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()

	u := openUpload(t, ctx, srv.URL+"/mydir.tgz", pr)
	if !strings.HasPrefix(u.curlLine, `curl -o "mydir.tgz" "`) {
		t.Errorf("riga curl: %q", u.curlLine)
	}
	if !strings.HasPrefix(u.wgetLine, `wget -O "mydir.tgz" "`) {
		t.Errorf("riga wget: %q", u.wgetLine)
	}
}
