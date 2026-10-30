package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"runtime"
	"strings"
	"sync"
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
	resp      *http.Response
	br        *bufio.Reader
	curlLine  string
	wgetLine  string
	aria2Line string
	slug      string
}

// openUpload invia PUT e legge i tre comandi della prima risposta.
func openUpload(t *testing.T, ctx context.Context, url string, body io.Reader) *upload {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		t.Fatal(err)
	}
	return openUploadRequest(t, ctx, req)
}

// openUploadRequest invia req e legge i tre comandi della prima risposta.
func openUploadRequest(t *testing.T, ctx context.Context, req *http.Request) *upload {
	t.Helper()
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
	var curl, wget, aria2 string
	within(t, 5*time.Second, "righe dei comandi", func() bool {
		for i := 0; i < 20 && (curl == "" || wget == "" || aria2 == ""); i++ {
			l, err := br.ReadString('\n')
			switch {
			case strings.HasPrefix(l, "curl -o"):
				curl = l
			case strings.HasPrefix(l, "wget -O"):
				wget = l
			case strings.HasPrefix(l, "aria2c "):
				aria2 = l
			}
			if err != nil {
				break
			}
		}
		return true
	})
	return &upload{resp: resp, br: br, curlLine: curl, wgetLine: wget, aria2Line: aria2, slug: slugOf(curl)}
}

// readCurlLine legge la risposta del mittente fino alla riga del comando curl, saltando la
// cornice della tabella dei comandi.
func readCurlLine(br *bufio.Reader) (string, error) {
	for {
		l, err := br.ReadString('\n')
		if strings.HasPrefix(l, "curl -o") {
			return l, nil
		}
		if err != nil {
			return l, err
		}
	}
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
		l, _ := readCurlLine(br)
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

// TestUploadIdleTimeout: il mittente scrive 1 KiB di 1 MiB dichiarato e si ferma.
// Con IdleTimeout il relay deve interrompere il ricevente e liberare la sessione (D15).
func TestUploadIdleTimeout(t *testing.T) {
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{IdleTimeout: 200 * time.Millisecond}))
	t.Cleanup(srv.Close)

	addr := srv.Listener.Addr().String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	const total = 1 << 20
	fmt.Fprintf(conn, "PUT /idle.bin HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n", addr, total)

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

	recv := make(chan error, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/" + slug)
		if err != nil {
			recv <- err
			return
		}
		defer resp.Body.Close()
		n, err := io.Copy(io.Discard, resp.Body)
		if err == nil && n < total {
			err = fmt.Errorf("ricevuti %d byte senza errore", n)
		}
		recv <- err
	}()

	if _, err := conn.Write(make([]byte, 1<<10)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-recv:
		if err == nil {
			t.Fatal("il ricevente non ha visto errore dopo l'inattività")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("il ricevente non termina dopo IdleTimeout")
	}
	deadline := time.Now().Add(3 * time.Second)
	for reg.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reg.Len() != 0 {
		t.Fatalf("sessioni vive %d dopo IdleTimeout, attese 0", reg.Len())
	}
}

// TestDownloadHeaders: lunghezza nota → Content-Length; sconosciuta → nessun Content-Length.
// Content-Disposition riporta il nome usato nei comandi.
func TestDownloadHeaders(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	data := []byte("strano contenuto")
	u := openUpload(t, ctx, srv.URL+"/strano%20nome.zip", bytes.NewReader(data))
	dl := get(t, ctx, srv.URL+"/"+u.slug)
	_, _ = io.Copy(io.Discard, dl.Body)
	dl.Body.Close()
	u.finish(t)
	if got := dl.Header.Get("Content-Length"); got != fmt.Sprint(len(data)) {
		t.Errorf("Content-Length %q, atteso %d", got, len(data))
	}
	if got := dl.Header.Get("Content-Disposition"); got != `attachment; filename="strano nome.zip"` {
		t.Errorf("Content-Disposition %q", got)
	}
	if got := dl.Header.Get("Accept-Ranges"); got != "none" {
		t.Errorf("Accept-Ranges %q, atteso none", got)
	}

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(data)
		_ = pw.Close()
	}()
	u2 := openUpload(t, ctx, srv.URL+"/chunk.bin", pr)
	dl2 := get(t, ctx, srv.URL+"/"+u2.slug)
	_, _ = io.Copy(io.Discard, dl2.Body)
	dl2.Body.Close()
	u2.finish(t)
	if got := dl2.Header.Get("Content-Length"); got != "" {
		t.Errorf("Content-Length %q con lunghezza sconosciuta, atteso assente", got)
	}
}

// TestStalledReceiverReleasesSession: un ricevente connesso che non legge mai non deve bloccare
// per sempre mittente e sessione. Il timeout di scrittura (IdleTimeout) interrompe il relay.
func TestStalledReceiverReleasesSession(t *testing.T) {
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{IdleTimeout: 200 * time.Millisecond}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	defer pw.CloseWithError(io.ErrClosedPipe)
	go func() {
		block := make([]byte, 64<<10)
		for {
			if _, err := pw.Write(block); err != nil {
				return
			}
		}
	}()
	u := openUpload(t, ctx, srv.URL+"/big.bin", pr)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(conn, "GET /%s HTTP/1.1\r\nHost: x\r\n\r\n", u.slug)

	deadline := time.Now().Add(10 * time.Second)
	for reg.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if reg.Len() != 0 {
		t.Fatalf("sessioni vive %d con ricevente fermo, attese 0", reg.Len())
	}
}

// TestUploadExpiredClosesSender: senza ricevente, allo scadere dell'attesa il mittente è
// interrotto (nessuna risposta completa) e lo slug risponde 410.
func TestUploadExpiredClosesSender(t *testing.T) {
	srv, reg := newTestServerWait(t, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	u := openUpload(t, ctx, srv.URL+"/w.bin", pr)

	within(t, 5*time.Second, "chiusura mittente scaduto", func() error {
		_, err := io.ReadAll(u.br)
		return err
	})
	if reg.Len() != 0 {
		t.Fatalf("sessioni vive %d dopo scadenza, attese 0", reg.Len())
	}
	resp := get(t, ctx, srv.URL+"/"+u.slug)
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("slug scaduto: stato %d, atteso 410", resp.StatusCode)
	}
}

// TestCommandLineHasNoShellExpansion: il nome nei comandi non contiene espansioni di shell
// ($, backtick, !) né virgolette: l'utente incolla la riga in una shell.
func TestCommandLineHasNoShellExpansion(t *testing.T) {
	srv, _ := newTestServerWait(t, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()

	u := openUpload(t, ctx, srv.URL+"/"+"x$(touch%20PWNED)%60id%60%21%22.zip", pr)
	for _, line := range []string{u.curlLine, u.wgetLine, u.aria2Line} {
		name := line[strings.Index(line, `"`)+1:]
		name = name[:strings.Index(name, `"`)]
		if strings.ContainsAny(name, "$`!\\") {
			t.Errorf("nome non sicuro nella riga %q", line)
		}
	}
}

// TestDownloadHeadersNonASCII: nome non ASCII codificato con filename* (RFC 6266).
func TestDownloadHeadersNonASCII(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := openUpload(t, ctx, srv.URL+"/citt%C3%A0.zip", strings.NewReader("x"))
	dl := get(t, ctx, srv.URL+"/"+u.slug)
	_, _ = io.Copy(io.Discard, dl.Body)
	dl.Body.Close()
	u.finish(t)
	cd := dl.Header.Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(cd)
	if err != nil || params["filename"] != "città.zip" {
		t.Fatalf("Content-Disposition %q: filename %q err %v", cd, params["filename"], err)
	}
	if dl.Header.Get("Cache-Control") != "no-store" || dl.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("header di sicurezza mancanti: %v", dl.Header)
	}
}

// TestConcurrentTransfers: molti trasferimenti simultanei, di dimensioni e modalità diverse
// (lunghezza nota e chunked), ciascuno verificato con sha256. Sotto -race rileva data race
// nel registry e nel passaggio mittente-ricevente.
func TestConcurrentTransfers(t *testing.T) {
	reg := relay.NewRegistry(500, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{IdleTimeout: 10 * time.Second}))
	t.Cleanup(srv.Close)

	const workers = 40
	var wg sync.WaitGroup
	errc := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errc <- func() error {
				data := make([]byte, (i+1)*37_001)
				if _, err := rand.Read(data); err != nil {
					return err
				}
				want := sha256.Sum256(data)
				var body io.Reader = bytes.NewReader(data)
				if i%2 == 1 {
					body = io.NopCloser(bytes.NewReader(data)) // lunghezza ignota: chunked
				}
				req, err := http.NewRequest(http.MethodPut, srv.URL+"/f.bin", body)
				if err != nil {
					return err
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				br := bufio.NewReader(resp.Body)
				line, err := readCurlLine(br)
				if err != nil {
					return err
				}
				dl, err := http.Get(srv.URL + "/" + slugOf(line))
				if err != nil {
					return err
				}
				defer dl.Body.Close()
				h := sha256.New()
				n, err := io.Copy(h, dl.Body)
				if err != nil {
					return err
				}
				if n != int64(len(data)) || !bytes.Equal(h.Sum(nil), want[:]) {
					return fmt.Errorf("worker %d: %d byte su %d, sha256 diverso", i, n, len(data))
				}
				_, err = io.Copy(io.Discard, br)
				return err
			}()
		}(i)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Error(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for reg.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if reg.Len() != 0 {
		t.Fatalf("sessioni vive %d, attese 0", reg.Len())
	}
}

// TestKeepAliveAfterDownload: dopo un download con IdleTimeout la stessa connessione serve
// un'altra richiesta (il deadline di scrittura non deve restare impostato).
func TestKeepAliveAfterDownload(t *testing.T) {
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{IdleTimeout: 300 * time.Millisecond}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &http.Client{Transport: &http.Transport{MaxConnsPerHost: 1}}
	defer client.CloseIdleConnections()
	u := openUpload(t, ctx, srv.URL+"/k.bin", strings.NewReader("contenuto"))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/"+u.slug, nil)
	dl, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, dl.Body); err != nil {
		t.Fatal(err)
	}
	dl.Body.Close()
	u.finish(t)

	time.Sleep(600 * time.Millisecond) // oltre IdleTimeout: un deadline residuo sarebbe scaduto
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/", nil)
	resp, err := client.Do(req2)
	if err != nil {
		t.Fatalf("seconda richiesta sulla stessa connessione: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("seconda richiesta: stato %d err %v", resp.StatusCode, err)
	}
}

// TestTransferLogHasNoSlug: il log dell'esito non contiene slug né nome del file.
func TestTransferLogHasNoSlug(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{Logger: logger}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	u := openUpload(t, ctx, srv.URL+"/segreto.bin", strings.NewReader("abc"))
	dl := get(t, ctx, srv.URL+"/"+u.slug)
	_, _ = io.Copy(io.Discard, dl.Body)
	dl.Body.Close()
	u.finish(t)

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), "outcome=ok") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, "outcome=ok") || !strings.Contains(out, "bytes=3") {
		t.Fatalf("log senza esito: %q", out)
	}
	if strings.Contains(out, u.slug) || strings.Contains(out, "segreto") {
		t.Fatalf("il log espone slug o nome: %q", out)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestRejectedUploadDoesNotHoldConnection: dopo un 401 con body dichiarato e mai inviato,
// il server chiude la connessione entro drainTimeout invece di aspettare il body all'infinito.
func TestRejectedUploadDoesNotHoldConnection(t *testing.T) {
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{Authorize: BasicAuth("u", "p")}))
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT /x HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(drainTimeout + 3*time.Second))
	n, err := io.Copy(io.Discard, conn)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("connessione ancora aperta dopo %d byte di risposta", n)
	}
}

// TestNoGoroutineLeak: trasferimenti riusciti, scaduti, con ricevente caduto e con mittente
// caduto non lasciano goroutine né sessioni. Il numero di goroutine torna al valore iniziale.
func TestNoGoroutineLeak(t *testing.T) {
	reg := relay.NewRegistry(1000, 80*time.Millisecond)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{IdleTimeout: 300 * time.Millisecond}))
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	t.Cleanup(srv.Close)

	settle := func() int {
		var n int
		for i := 0; i < 100; i++ {
			time.Sleep(50 * time.Millisecond)
			n = runtime.NumGoroutine()
			if i > 2 && n == runtime.NumGoroutine() {
				break
			}
		}
		return n
	}
	baseline := settle()

	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		pr, pw := io.Pipe()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/l.bin", pr)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(resp.Body)
		line, err := readCurlLine(br)
		if err != nil {
			t.Fatal(err)
		}
		slug := slugOf(line)
		switch i % 3 {
		case 0: // riuscito
			go func() { _, _ = pw.Write([]byte("dati")); _ = pw.Close() }()
			dl, err := client.Get(srv.URL + "/" + slug)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, dl.Body)
			dl.Body.Close()
		case 1: // scade senza ricevente
			time.Sleep(150 * time.Millisecond)
		case 2: // ricevente cade a metà
			go func() {
				block := make([]byte, 64<<10)
				for {
					if _, err := pw.Write(block); err != nil {
						return
					}
				}
			}()
			rctx, rcancel := context.WithCancel(ctx)
			rreq, _ := http.NewRequestWithContext(rctx, http.MethodGet, srv.URL+"/"+slug, nil)
			dl, err := client.Do(rreq)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.CopyN(io.Discard, dl.Body, 128<<10)
			rcancel()
			dl.Body.Close()
		}
		cancel()
		pw.CloseWithError(io.ErrClosedPipe)
		resp.Body.Close()
	}

	deadline := time.Now().Add(10 * time.Second)
	for reg.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if reg.Len() != 0 {
		t.Fatalf("sessioni vive %d, attese 0", reg.Len())
	}
	client.CloseIdleConnections()
	after := settle()
	if after > baseline+3 {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutine: %d prima, %d dopo\n%s", baseline, after, buf)
	}
}

// TestUnsafeHostRejected: senza PublicURL la base dei comandi viene da Host; un Host con
// caratteri che la shell interpreta tra virgolette doppie ($, !, spazi) riceve 400 e nessuno slug.
func TestUnsafeHostRejected(t *testing.T) {
	srv, reg := newTestServer(t)
	for _, host := range []string{"x$(id)", "x!(id)"} {
		for _, line := range []string{"PUT /f.txt", "GET /"} {
			conn, err := net.Dial("tcp", srv.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(conn, "%s HTTP/1.1\r\nHost: %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", line, host)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("%s Host %q: %v", line, host, err)
			}
			body, _ := io.ReadAll(resp.Body)
			conn.Close()
			if resp.StatusCode != http.StatusBadRequest || strings.Contains(string(body), host) {
				t.Fatalf("%s Host %q: stato %d body %q, atteso 400 senza Host", line, host, resp.StatusCode, body)
			}
		}
	}
	if reg.Len() != 0 {
		t.Fatalf("sessioni vive %d dopo Host non valido, attese 0", reg.Len())
	}
}

// TestPublicURLIgnoresHost: con PublicURL impostata Host non entra nei comandi, anche se non valido.
func TestPublicURLIgnoresHost(t *testing.T) {
	reg := relay.NewRegistry(100, 200*time.Millisecond)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{PublicURL: "https://pub.example/"}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/f.bin", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "x$(id)"
	u := openUploadRequest(t, ctx, req)
	want := `"https://pub.example/` + u.slug + `"`
	if !strings.HasSuffix(strings.TrimSpace(u.curlLine), want) || strings.Contains(u.curlLine, "$") {
		t.Fatalf("riga curl %q, attesa base pubblica %s", u.curlLine, want)
	}
}

// TestExpiredLogsExpired: lo scadere dell'attesa è registrato come scadenza, non come mittente caduto.
func TestExpiredLogsExpired(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	reg := relay.NewRegistry(100, 100*time.Millisecond)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{Logger: logger}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	u := openUpload(t, ctx, srv.URL+"/w.bin", pr)
	within(t, 5*time.Second, "chiusura mittente scaduto", func() error {
		_, err := io.ReadAll(u.br)
		return err
	})

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), "outcome=expired") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, "outcome=expired") || !strings.Contains(out, relay.ErrExpired.Error()) ||
		strings.Contains(out, relay.ErrSenderGone.Error()) {
		t.Fatalf("log della scadenza: %q", out)
	}
}

// TestUploadCapacity503: oltre BASHT_MAX_SESSIONS il PUT riceve 503 senza comandi; liberata la
// sessione, un nuovo upload riesce.
func TestUploadCapacity503(t *testing.T) {
	reg := relay.NewRegistry(1, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	u := openUpload(t, ctx, srv.URL+"/a.bin", strings.NewReader("abc"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/b.bin", strings.NewReader("def"))
	if err != nil {
		t.Fatal(err)
	}
	resp := within(t, 5*time.Second, "PUT oltre capacità", func() *http.Response {
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
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), "curl -o") {
		t.Fatalf("PUT oltre capacità: stato %d body %q, atteso 503", resp.StatusCode, body)
	}

	dl := get(t, ctx, srv.URL+"/"+u.slug)
	_, _ = io.Copy(io.Discard, dl.Body)
	dl.Body.Close()
	u.finish(t)
	u2 := openUpload(t, ctx, srv.URL+"/c.bin", strings.NewReader("ghi"))
	dl2 := get(t, ctx, srv.URL+"/"+u2.slug)
	got, _ := io.ReadAll(dl2.Body)
	dl2.Body.Close()
	u2.finish(t)
	if string(got) != "ghi" {
		t.Fatalf("upload dopo capacità liberata: %q", got)
	}
}

// TestEmptyUpload: un file vuoto (Content-Length 0) arriva come risposta 200 vuota, non come errore.
func TestEmptyUpload(t *testing.T) {
	srv, reg := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/empty.txt", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 0
	u := openUploadRequest(t, ctx, req)
	dl := get(t, ctx, srv.URL+"/"+u.slug)
	got, err := io.ReadAll(dl.Body)
	dl.Body.Close()
	if err != nil || dl.StatusCode != http.StatusOK || len(got) != 0 || dl.ContentLength != 0 {
		t.Fatalf("download vuoto: stato %d len %d CL %d err %v", dl.StatusCode, len(got), dl.ContentLength, err)
	}
	u.finish(t)
	if reg.Len() != 0 {
		t.Fatalf("sessioni vive %d, attese 0", reg.Len())
	}
}

// newTestServerConn avvia il server con ConnContext, come main: serve al rilevamento del
// mittente caduto durante l'attesa. L'attesa massima è 10 s: se il rilevamento non funziona,
// la scadenza chiude comunque gli handler e srv.Close non resta bloccato.
func newTestServerConn(t *testing.T, logger *slog.Logger) (*httptest.Server, *relay.Registry) {
	t.Helper()
	reg := relay.NewRegistry(100, 10*time.Second)
	srv := httptest.NewUnstartedServer(New(reg, relay.NewCopier(64<<10), Options{Logger: logger}))
	srv.Config.ConnContext = ConnContext
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, reg
}

// openRawUpload invia un PUT con Content-Length 100 e 10 byte di body, e restituisce lo slug.
func openRawUpload(t *testing.T, addr string) (net.Conn, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "PUT /f.bin HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n0123456789")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	line, err := readCurlLine(bufio.NewReader(resp.Body))
	if err != nil {
		t.Fatalf("riga curl: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn, slugOf(line)
}

// TestWaitingSenderGoneFreesSlug: un mittente che chiude mentre attende (body non letto) viene
// rilevato entro pochi secondi: la sessione si libera e lo slug risponde 410. Un mittente ancora
// connesso resta in attesa.
func TestWaitingSenderGoneFreesSlug(t *testing.T) {
	if !peerCheckSupported {
		t.Skip("rilevamento disponibile solo su Linux")
	}
	var buf syncBuffer
	srv, reg := newTestServerConn(t, slog.New(slog.NewTextHandler(&buf, nil)))
	addr := srv.Listener.Addr().String()

	alive, _ := openRawUpload(t, addr)
	defer alive.Close()
	gone, goneSlug := openRawUpload(t, addr)
	gone.Close()

	deadline := time.Now().Add(3*peerCheckInterval + 2*time.Second)
	for reg.Len() != 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if reg.Len() != 1 {
		t.Fatalf("sessioni vive %d, attesa 1 (solo il mittente connesso)", reg.Len())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := get(t, ctx, srv.URL+"/"+goneSlug)
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("slug del mittente caduto: stato %d, atteso 410", resp.StatusCode)
	}
	if out := buf.String(); !strings.Contains(out, "outcome=sender_gone") {
		t.Fatalf("log senza sender_gone: %q", out)
	}
}

// TestOkLogIgnoresReceiverCloseAfterLastByte: con Content-Length il ricevente chiude appena ha
// letto tutti i byte, anche prima che il mittente concluda; l'esito resta ok, senza errore nel log.
// Con lunghezza ignota una chiusura prima del chunk finale è invece un'interruzione
// (TestReceiverGoneInterruptsIdleSender).
func TestOkLogIgnoresReceiverCloseAfterLastByte(t *testing.T) {
	var buf syncBuffer
	srv, _ := newTestServerConn(t, slog.New(slog.NewTextHandler(&buf, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := randomBytes(t, 4096)
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/f.bin", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(data))
	u := openUploadRequest(t, ctx, req)

	dctx, dcancel := context.WithCancel(ctx)
	dl := get(t, dctx, srv.URL+"/"+u.slug)
	go func() { _, _ = pw.Write(data); pw.Close() }()
	got := make([]byte, len(data))
	if _, err := io.ReadFull(dl.Body, got); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("download: %v", err)
	}
	dcancel()
	dl.Body.Close()
	u.finish(t)

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), "outcome=") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, "outcome=ok") || strings.Contains(out, "err=") {
		t.Fatalf("log: %q, atteso outcome=ok senza err", out)
	}
}

// TestUploadNameEncodedSlash: un %2F nel nome non rende il path multi-segmento; resta l'ultima parte.
func TestUploadNameEncodedSlash(t *testing.T) {
	srv, _ := newTestServerWait(t, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	u := openUpload(t, ctx, srv.URL+"/a%2Fb.txt", pr)
	if !strings.HasPrefix(u.curlLine, `curl -o "b.txt" `) {
		t.Fatalf("riga curl %q, atteso nome b.txt", u.curlLine)
	}
	resp := get(t, ctx, srv.URL+"/AbC%2F12345")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET con %%2F: stato %d, atteso 404", resp.StatusCode)
	}
}

// TestUploadCommands: le tre righe usano lo stesso nome e lo stesso URL; aria2c usa una sola
// connessione. La risposta al mittente chiude la connessione.
func TestUploadCommands(t *testing.T) {
	srv, _ := newTestServerWait(t, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	u := openUpload(t, ctx, srv.URL+"/a%20b.tgz", pr)
	url := srv.URL + "/" + u.slug
	want := []string{
		`curl -o "a b.tgz" "` + url + `"` + "\n",
		`wget -O "a b.tgz" "` + url + `"` + "\n",
		`aria2c -x1 -s1 -o "a b.tgz" "` + url + `"` + "\n",
	}
	for i, got := range []string{u.curlLine, u.wgetLine, u.aria2Line} {
		if got != want[i] {
			t.Errorf("riga %d: %q, attesa %q", i+1, got, want[i])
		}
	}
	if !u.resp.Close {
		t.Error("risposta al mittente senza Connection: close")
	}
}

func TestRangeFromStart(t *testing.T) {
	cases := []struct {
		rng, ifRange string
		want         bool
	}{
		{"", "", true},
		{"bytes=0-", "", true},
		{"bytes=0-99", "", true},
		{"BYTES = 0-", "", true},
		{"bytes=0-,0-10", "", true},
		{"bytes=00-", "", true},
		{"bytes=100-", "", false},
		{"bytes=100-199", "", false},
		{"bytes=-500", "", false},
		{"bytes=0-99, 200-299", "", false},
		{" bytes=1-", "", false},
		{"bytes=100-", `"etag"`, true},
		{"items=5-", "", true},
		{"bytes=abc", "", true},
		{"bytes=a-", "", true},
		{"bytes=5-x", "", true},
		{"bytes=-", "", true},
		{"bytes=", "", true},
		{"bytes=,", "", true},
		{"bytes", "", true},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.rng != "" {
			h.Set("Range", c.rng)
		}
		if c.ifRange != "" {
			h.Set("If-Range", c.ifRange)
		}
		if got := rangeFromStart(h); got != c.want {
			t.Errorf("Range %q If-Range %q: %v, atteso %v", c.rng, c.ifRange, got, c.want)
		}
	}
}

// getWithRange esegue un GET con l'header Range indicato.
func getWithRange(t *testing.T, ctx context.Context, url, rng string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", rng)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestRangeResumeDoesNotClaim: una ripresa (Range da un byte > 0) riceve 400 senza consumare
// lo slug; il download completo successivo riesce. Su slug sconosciuti, reclamati o conclusi la
// risposta è quella di un GET normale. Range da 0 è servito con 200 e il body intero.
func TestRangeResumeDoesNotClaim(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := randomBytes(t, 64<<10)
	u := openUpload(t, ctx, srv.URL+"/r.bin", bytes.NewReader(data))
	url := srv.URL + "/" + u.slug

	for _, rng := range []string{"bytes=100-", "bytes=-10", "bytes=0-9,20-29"} {
		resp := getWithRange(t, ctx, url, rng)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Accept-Ranges") != "none" {
			t.Fatalf("Range %q: stato %d Accept-Ranges %q, atteso 400 e none", rng, resp.StatusCode, resp.Header.Get("Accept-Ranges"))
		}
	}
	unknown := getWithRange(t, ctx, srv.URL+"/ZZZZZZZZZZ", "bytes=100-")
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("Range su slug sconosciuto: stato %d, atteso 404", unknown.StatusCode)
	}

	dl := getWithRange(t, ctx, url, "bytes=0-")
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("Range da 0: stato %d, atteso 200", dl.StatusCode)
	}
	claimed := getWithRange(t, ctx, url, "bytes=100-")
	claimed.Body.Close()
	if claimed.StatusCode != http.StatusGone {
		t.Fatalf("Range su slug reclamato: stato %d, atteso 410", claimed.StatusCode)
	}
	got, err := io.ReadAll(dl.Body)
	dl.Body.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("download dopo i rifiuti: %d byte, %v", len(got), err)
	}
	u.finish(t)
	gone := getWithRange(t, ctx, url, "bytes=100-")
	gone.Body.Close()
	if gone.StatusCode != http.StatusGone {
		t.Fatalf("Range su slug concluso: stato %d, atteso 410", gone.StatusCode)
	}
}

// TestReceiverGoneInterruptsIdleSender: il ricevente chiude mentre il mittente non sta inviando
// (lettura del body in attesa). Il relay si interrompe subito, senza attendere il prossimo byte
// del mittente o IdleTimeout: la sessione si libera e il mittente vede un errore.
func TestReceiverGoneInterruptsIdleSender(t *testing.T) {
	for _, idle := range []time.Duration{0, time.Minute} {
		t.Run(fmt.Sprint("idle=", idle), func(t *testing.T) {
			var buf syncBuffer
			reg := relay.NewRegistry(100, time.Hour)
			srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{
				IdleTimeout: idle,
				Logger:      slog.New(slog.NewTextHandler(&buf, nil)),
			}))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pr, pw := io.Pipe()
			defer pw.Close()
			u := openUpload(t, ctx, srv.URL+"/slow.bin", pr)

			dctx, dcancel := context.WithCancel(ctx)
			dl := get(t, dctx, srv.URL+"/"+u.slug)
			data := randomBytes(t, 4096)
			go func() { _, _ = pw.Write(data) }()
			if _, err := io.ReadFull(dl.Body, make([]byte, len(data))); err != nil {
				t.Fatalf("download: %v", err)
			}
			dcancel()
			dl.Body.Close()

			err := within(t, 5*time.Second, "fine risposta del mittente", func() error {
				_, err := io.ReadAll(u.br)
				return err
			})
			u.resp.Body.Close()
			if err == nil {
				t.Fatal("risposta del mittente completa dopo la caduta del ricevente, atteso errore")
			}
			deadline := time.Now().Add(2 * time.Second)
			for reg.Len() != 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if reg.Len() != 0 {
				t.Fatalf("sessioni vive %d, attese 0", reg.Len())
			}
			for !strings.Contains(buf.String(), "outcome=") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if out := buf.String(); !strings.Contains(out, "outcome=aborted") || !strings.Contains(out, `err="receiver gone"`) {
				t.Fatalf("log %q, atteso outcome=aborted con receiver gone", out)
			}
		})
	}
}

// TestPeerClosedOnClosedConn: una connessione già chiusa dal server conta come caduta, così un
// mittente in attesa non resta appeso dopo srv.Close.
func TestPeerClosedOnClosedConn(t *testing.T) {
	if !peerCheckSupported {
		t.Skip("rilevamento disponibile solo su Linux")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if peerClosed(conn) {
		t.Fatal("connessione aperta segnalata come chiusa")
	}
	conn.Close()
	if !peerClosed(conn) {
		t.Fatal("connessione chiusa non segnalata")
	}
}

// jsonLines decodifica le righe JSON del log.
func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("riga di log non JSON %q: %v", l, err)
		}
		lines = append(lines, m)
	}
	return lines
}

// TestTransferLogFields: a livello debug una sessione completa produce upload_accepted,
// download_claimed, receiver_attached e transfer con lo stesso id; transfer ha byte, lunghezza,
// tempi e velocità. Nessuna riga contiene slug, nome o path.
func TestTransferLogFields(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{Logger: logger}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := randomBytes(t, 1<<20)
	u := openUpload(t, ctx, srv.URL+"/segreto.bin", bytes.NewReader(data))
	dl := get(t, ctx, srv.URL+"/"+u.slug)
	_, _ = io.Copy(io.Discard, dl.Body)
	dl.Body.Close()
	u.finish(t)

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), `"msg":"transfer"`) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	if strings.Contains(out, u.slug) || strings.Contains(out, "segreto") {
		t.Fatalf("il log espone slug o nome: %q", out)
	}
	byMsg := map[string]map[string]any{}
	for _, m := range jsonLines(t, out) {
		byMsg[m["msg"].(string)] = m
	}
	tr := byMsg["transfer"]
	if tr == nil {
		t.Fatalf("evento transfer mancante: %q", out)
	}
	id, _ := tr["id"].(string)
	if len(id) != 16 {
		t.Fatalf("id %q, attesi 16 caratteri esadecimali", id)
	}
	for _, msg := range []string{"upload_accepted", "download_claimed", "receiver_attached"} {
		if byMsg[msg] == nil || byMsg[msg]["id"] != id || byMsg[msg]["level"] != "DEBUG" {
			t.Errorf("evento %s: %v, atteso a DEBUG con id %s", msg, byMsg[msg], id)
		}
	}
	if tr["level"] != "INFO" || tr["outcome"] != "ok" || tr["bytes"] != float64(len(data)) || tr["length"] != float64(len(data)) {
		t.Errorf("transfer: %v", tr)
	}
	for _, k := range []string{"wait_s", "transfer_s", "rate_mbs"} {
		if _, ok := tr[k].(float64); !ok {
			t.Errorf("transfer: campo %s non numerico: %v", k, tr[k])
		}
	}
	if _, ok := tr["err"]; ok {
		t.Errorf("transfer ok con err: %v", tr)
	}
}

// TestRejectionLogs: i rifiuti sono registrati come request_rejected con stato e motivo, al
// livello previsto, senza path né credenziali.
func TestRejectionLogs(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{Logger: logger, Authorize: BasicAuth("u", "p")}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/segreto.bin", strings.NewReader("x"))
	req.SetBasicAuth("u", "passwordsbagliata")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = http.Get(srv.URL + "/ZZZZZZZZZZ")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	out := buf.String()
	if strings.Contains(out, "segreto") || strings.Contains(out, "passwordsbagliata") || strings.Contains(out, "ZZZZZZZZZZ") {
		t.Fatalf("il log espone path o credenziali: %q", out)
	}
	want := []struct {
		level, reason string
		status        float64
	}{{"WARN", "unauthorized", 401}, {"DEBUG", "not_found", 404}}
	lines := jsonLines(t, out)
	if len(lines) != len(want) {
		t.Fatalf("righe %d, attese %d: %q", len(lines), len(want), out)
	}
	for i, w := range want {
		m := lines[i]
		if m["msg"] != "request_rejected" || m["level"] != w.level || m["reason"] != w.reason || m["status"] != w.status {
			t.Errorf("riga %d: %v, attesi %s %s %v", i, m, w.level, w.reason, w.status)
		}
	}
}

// TestAbortedTransferLogsWarn: un relay interrotto a metà è registrato a WARN con l'errore; la
// scadenza dell'attesa resta INFO.
func TestAbortedTransferLogsWarn(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{Logger: logger}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/f.bin", pr)
	req.ContentLength = 1 << 20
	u := openUploadRequest(t, ctx, req)
	dl := get(t, ctx, srv.URL+"/"+u.slug)
	go func() { _, _ = pw.Write(make([]byte, 1000)); pw.CloseWithError(errors.New("mittente caduto")) }()
	_, _ = io.Copy(io.Discard, dl.Body)
	dl.Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), `"msg":"transfer"`) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	lines := jsonLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("righe %d: %q", len(lines), buf.String())
	}
	m := lines[0]
	if m["level"] != "WARN" || m["outcome"] != "aborted" || m["err"] == nil || m["length"] != float64(1<<20) {
		t.Fatalf("transfer interrotto: %v", m)
	}
}

// TestUploadH2C verifica il relay su HTTP/2 in chiaro (proxy Apache h2c://): il 100 Continue
// arriva anche senza Expect, i comandi precedono il body e il file passa intatto.
func TestUploadH2C(t *testing.T) {
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewUnstartedServer(New(reg, relay.NewCopier(64<<10), Options{}))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)

	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)

	data := randomBytes(t, 4<<20)
	continued := make(chan struct{}, 1)
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
			if code == http.StatusContinue {
				continued <- struct{}{}
			}
			return nil
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/h2.bin", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	resp := within(t, 5*time.Second, "risposta PUT h2c", func() *http.Response {
		r, err := client.Do(req)
		if err != nil {
			t.Error(err)
			return nil
		}
		return r
	})
	if resp == nil {
		t.FailNow()
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("protocollo %s, atteso HTTP/2", resp.Proto)
	}
	select {
	case <-continued:
	default:
		t.Error("100 Continue non ricevuto")
	}
	if c := resp.Header.Get("Connection"); c != "" {
		t.Errorf("header Connection %q su HTTP/2", c)
	}
	br := bufio.NewReader(resp.Body)
	line, _ := readCurlLine(br)

	dl := get(t, context.Background(), srv.URL+"/"+slugOf(line))
	defer dl.Body.Close()
	got := within(t, 10*time.Second, "download", func() []byte {
		b, _ := io.ReadAll(dl.Body)
		return b
	})
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Errorf("contenuto diverso: %d byte, attesi %d", len(got), len(data))
	}
}

// TestUploadH2CHeartbeat verifica che su HTTP/2 un mittente in attesa riceva "\r" periodici
// dopo i comandi: è la scrittura che fa cadere lo stream quando un proxy h2c ha perso il mittente.
func TestUploadH2CHeartbeat(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = old })

	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewUnstartedServer(New(reg, relay.NewCopier(64<<10), Options{}))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)

	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/hb.bin", pr)
	if err != nil {
		t.Fatal(err)
	}
	resp := within(t, 5*time.Second, "risposta PUT h2c", func() *http.Response {
		r, err := client.Do(req)
		if err != nil {
			t.Error(err)
			return nil
		}
		return r
	})
	if resp == nil {
		t.FailNow()
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	// Dopo la tabella dei comandi (che termina con "\n") il primo byte è l'heartbeat.
	tail := within(t, 5*time.Second, "heartbeat", func() string {
		s, _ := br.ReadString('\r')
		return s
	})
	if !strings.HasSuffix(tail, "+\n\r") || !strings.Contains(tail, "aria2c") {
		t.Errorf("dopo la tabella: %q, atteso un heartbeat \"\\r\"", tail)
	}
}

// TestHomeBrowser: con Accept text/html la home è la pagina di cortesia, con la base dei comandi
// e la CSP; senza (curl) resta il testo delle istruzioni.
func TestHomeBrowser(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stato %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("CSP mancante")
	}
	for _, want := range []string{
		"<title>Bashtransfer</title>",
		"Relay sicuro per invio file senza persistenza server",
		"curl -T FILE " + srv.URL,
		`rel="icon"`,
		"sudo apt install curl wget aria2",
		"brew install curl wget aria2",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("la pagina non contiene %q", want)
		}
	}
	if strings.Contains(string(body), "%%BASE%%") {
		t.Error("segnaposto %%BASE%% non sostituito")
	}

	plain := get(t, context.Background(), srv.URL+"/")
	defer plain.Body.Close()
	if ct := plain.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("senza Accept html: Content-Type %q", ct)
	}
}

// TestHomeEscapesBase: la base pubblica finisce nell'HTML solo con escape.
func TestHomeEscapesBase(t *testing.T) {
	reg := relay.NewRegistry(10, time.Hour)
	h := New(reg, relay.NewCopier(64<<10), Options{PublicURL: `https://x.it/"><script>alert(1)</script>`})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "<script>alert(1)") {
		t.Error("base non escapata nell'HTML")
	}
}

// TestCommandsTable: tabella ASCII con le righe di comando a inizio riga e senza spazi finali
// (copiabili con un triplo clic); le righe di cornice hanno tutte la stessa larghezza.
func TestCommandsTable(t *testing.T) {
	out := commands("a b.tgz", "https://h.it/AbC123xY9z")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	width := -1
	cmds := 0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+") || strings.HasPrefix(l, "|"):
			if width == -1 {
				width = len(l)
			}
			if len(l) != width {
				t.Errorf("riga di cornice di larghezza %d, attesa %d: %q", len(l), width, l)
			}
		default:
			cmds++
			if l != strings.TrimSpace(l) {
				t.Errorf("riga di comando con spazi ai bordi: %q", l)
			}
			if !strings.HasPrefix(l, "curl ") && !strings.HasPrefix(l, "wget ") && !strings.HasPrefix(l, "aria2c ") {
				t.Errorf("riga inattesa: %q", l)
			}
		}
	}
	if cmds != 3 {
		t.Errorf("%d righe di comando, attese 3", cmds)
	}
}
