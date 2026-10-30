package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bashtransfer/internal/relay"
	"bashtransfer/internal/slug"
)

// newWebServer è newConnServer con il registry: HTTP/1 e h2c, attesa del ricevente wait.
func newWebServer(t *testing.T, wait time.Duration) (*httptest.Server, *relay.Registry) {
	t.Helper()
	reg := relay.NewRegistry(100, wait)
	srv := httptest.NewUnstartedServer(New(reg, relay.NewCopier(64<<10), Options{}))
	srv.Config.ConnContext = ConnContext
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, reg
}

// webStream è una prenotazione con il suo stream NDJSON ancora aperto, come la tiene la pagina.
type webStream struct {
	reservation
	body io.Closer
	sc   *bufio.Scanner
}

// streamLine è una riga dello stream dopo la prenotazione: un evento o l'esito.
type streamLine struct {
	Event   string `json:"event"`
	Bytes   int64  `json:"bytes"`
	Outcome string `json:"outcome"`
}

// reserveUpload prenota un upload dal browser (POST / in JSON) e legge la prima riga dello stream.
// Lo stream resta aperto fino a close o alla fine del test.
func reserveUpload(t *testing.T, client *http.Client, base, name string, size int64) *webStream {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(fmt.Sprintf(`{"name":%q,"size":%d}`, name, size)))
	req.Header.Set("Content-Type", "application/json")
	return openStream(t, client, req)
}

func openStream(t *testing.T, client *http.Client, req *http.Request) *webStream {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("prenotazione: stato %d, %q", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("prenotazione: Content-Type %q", ct)
	}
	ws := &webStream{body: resp.Body, sc: bufio.NewScanner(resp.Body)}
	first := within(t, 5*time.Second, "prenotazione", func() bool { return ws.sc.Scan() })
	if !first || json.Unmarshal(ws.sc.Bytes(), &ws.reservation) != nil {
		t.Fatalf("prenotazione: prima riga %q, err %v", ws.sc.Bytes(), ws.sc.Err())
	}
	return ws
}

func (ws *webStream) close() { ws.body.Close() }

// outcome legge lo stream fino all'esito e restituisce anche gli eventi incontrati. Fallisce se lo
// stream si chiude senza esito o se l'esito non arriva entro d.
func (ws *webStream) outcome(t *testing.T, d time.Duration) (streamLine, []streamLine) {
	t.Helper()
	type result struct {
		last   streamLine
		events []streamLine
		err    string
	}
	r := within(t, d, "esito dello stream", func() result {
		var events []streamLine
		for ws.sc.Scan() {
			var l streamLine
			if err := json.Unmarshal(ws.sc.Bytes(), &l); err != nil {
				return result{err: fmt.Sprintf("riga %q: %v", ws.sc.Bytes(), err)}
			}
			if l.Outcome != "" {
				if ws.sc.Scan() {
					return result{err: fmt.Sprintf("riga dopo l'esito: %q", ws.sc.Bytes())}
				}
				return result{last: l, events: events}
			}
			events = append(events, l)
		}
		return result{err: fmt.Sprintf("stream chiuso senza esito: %v", ws.sc.Err())}
	})
	if r.err != "" {
		t.Fatal(r.err)
	}
	return r.last, r.events
}

// webResult è la risposta a un PUT dal browser, letta per intero.
type webResult struct {
	status int
	body   string
	err    error
}

// webPut invia il body della prenotazione id con il token; l'esito JSON è nell'ultima riga della
// risposta.
func webPut(client *http.Client, base, id, token string, body io.Reader, length int64) webResult {
	req, _ := http.NewRequest(http.MethodPut, base+"/"+id, body)
	req.ContentLength = length
	req.Header.Set(tokenHeader, token)
	resp, err := client.Do(req)
	if err != nil {
		return webResult{err: err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return webResult{resp.StatusCode, string(b), err}
}

// lastOutcome decodifica l'esito JSON dell'ultima riga non vuota.
func lastOutcome(t *testing.T, body string) (outcome string, bytes int64) {
	t.Helper()
	lines := strings.Fields(body)
	if len(lines) == 0 {
		t.Fatalf("risposta senza esito: %q", body)
	}
	var o struct {
		Outcome string  `json:"outcome"`
		Bytes   int64   `json:"bytes"`
		Seconds float64 `json:"seconds"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &o); err != nil {
		t.Fatalf("esito %q: %v", body, err)
	}
	return o.Outcome, o.Bytes
}

// waitLen attende che il registry abbia n sessioni.
func waitLen(t *testing.T, reg *relay.Registry, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); reg.Len() != n; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("sessioni %d, attese %d", reg.Len(), n)
		}
	}
}

// TestReserve: la prenotazione restituisce slug, link, token e i comandi del ricevente con la riga
// direct, gli stessi della tabella di curl.
func TestReserve(t *testing.T) {
	srv, reg := newTestServer(t)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "report finale.pdf", 1234)
	u := srv.URL + "/" + res.Slug
	if !slug.Valid(res.Slug) || res.URL != u || len(res.Token) != 32 {
		t.Fatalf("prenotazione %+v", res.reservation)
	}
	want := append(receiverCommands(u), command{"direct", u})
	if fmt.Sprint(res.Commands) != fmt.Sprint(want) {
		t.Fatalf("comandi %v, attesi %v", res.Commands, want)
	}
	s, err := reg.Peek(res.Slug)
	if err != nil || s.Name != "report finale.pdf" || s.Length != 1234 {
		t.Fatalf("sessione %+v, err %v", s, err)
	}
}

// TestReserveRejects: solo JSON valido con una dimensione non negativa crea una prenotazione.
func TestReserveRejects(t *testing.T) {
	srv, reg := newTestServer(t)
	for _, c := range []struct {
		ctype, body string
		status      int
	}{
		{"text/plain", `{"name":"a","size":1}`, http.StatusUnsupportedMediaType},
		{"application/x-www-form-urlencoded", "name=a&size=1", http.StatusUnsupportedMediaType},
		{"", `{"name":"a","size":1}`, http.StatusUnsupportedMediaType},
		{"application/json", `{"name":"a"`, http.StatusBadRequest},
		{"application/json", `{"name":"a"}`, http.StatusBadRequest},
		{"application/json", `{"name":"a","size":-1}`, http.StatusBadRequest},
		{"application/json", `{"name":"a","size":"1"}`, http.StatusBadRequest},
		{"application/json", `{"name":"a","size":1} {}`, http.StatusBadRequest},
		{"application/json", `{"name":"` + strings.Repeat("a", 5000) + `","size":1}`, http.StatusBadRequest},
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(c.body))
		if c.ctype != "" {
			req.Header.Set("Content-Type", c.ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Errorf("%s %.40q: stato %d, atteso %d", c.ctype, c.body, resp.StatusCode, c.status)
		}
	}
	if reg.Len() != 0 {
		t.Fatalf("sessioni %d dopo prenotazioni rifiutate, attese 0", reg.Len())
	}
}

// TestReserveAuth: con l'autenticazione attiva la prenotazione la richiede; il PUT col token no,
// perché il token prova la prenotazione.
func TestReserveAuth(t *testing.T) {
	reg := relay.NewRegistry(10, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{Authorize: BasicAuth("u", "p")}))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/", "application/json", strings.NewReader(`{"name":"a","size":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || reg.Len() != 0 {
		t.Fatalf("senza credenziali: stato %d, sessioni %d; attesi 401, 0", resp.StatusCode, reg.Len())
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(`{"name":"a","size":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("u", "p")
	res := openStream(t, http.DefaultClient, req)
	defer res.close()
	done := make(chan webResult, 1)
	go func() { done <- webPut(http.DefaultClient, srv.URL, res.Slug, res.Token, strings.NewReader("x"), 1) }()
	dl := get(t, context.Background(), srv.URL+"/"+res.Slug)
	got, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	r := <-done
	if string(got) != "x" || r.status != http.StatusOK {
		t.Fatalf("download %q, PUT stato %d %q", got, r.status, r.body)
	}
}

// TestWebUploadRoundTrip: prenotazione, ricevente e PUT col token, su HTTP/1 e h2c (Apache). Il
// ricevente ottiene i byte e il nome della prenotazione; il mittente l'esito "ok" sia nello stream
// della prenotazione, dopo l'evento attached e l'avanzamento, sia nella risposta del PUT.
func TestWebUploadRoundTrip(t *testing.T) {
	srv := newConnServer(t, time.Hour)
	for proto, client := range outcomeClients() {
		t.Run(proto, func(t *testing.T) {
			data := randomBytes(t, 300000)
			res := reserveUpload(t, client, srv.URL, "dati.bin", int64(len(data)))
			pr, pw := io.Pipe()
			done := make(chan webResult, 1)
			go func() { done <- webPut(client, srv.URL, res.Slug, res.Token, pr, int64(len(data))) }()
			dl := get(t, context.Background(), res.URL)
			// Metà del body, una pausa per almeno un evento progress, poi il resto.
			go func() {
				_, _ = pw.Write(data[:len(data)/2])
				time.Sleep(3 * progressInterval)
				_, _ = pw.Write(data[len(data)/2:])
				pw.Close()
			}()
			got, err := io.ReadAll(dl.Body)
			dl.Body.Close()
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("download: %d byte, err %v", len(got), err)
			}
			if cd := dl.Header.Get("Content-Disposition"); cd != "attachment; filename=dati.bin" {
				t.Fatalf("Content-Disposition %q", cd)
			}
			r := within(t, 5*time.Second, "esito PUT", func() webResult { return <-done })
			if r.err != nil || r.status != http.StatusOK {
				t.Fatalf("PUT: stato %d, err %v", r.status, r.err)
			}
			if o, n := lastOutcome(t, r.body); o != outcomeOK || n != int64(len(data)) {
				t.Fatalf("esito PUT %q %d", o, n)
			}
			last, events := res.outcome(t, 5*time.Second)
			if last.Outcome != outcomeOK || last.Bytes != int64(len(data)) {
				t.Fatalf("esito stream %+v", last)
			}
			if len(events) < 2 || events[0].Event != "attached" {
				t.Fatalf("eventi %+v: attesi attached e progress", events)
			}
			var prev int64
			for _, e := range events[1:] {
				if e.Event != "progress" || e.Bytes < prev || e.Bytes > int64(len(data)) {
					t.Fatalf("eventi %+v", events)
				}
				prev = e.Bytes
			}
		})
	}
}

// TestWebUploadWrongToken: il PUT con token errato, vuoto o su uno slug sconosciuto non trova la
// prenotazione; quella giusta resta valida.
func TestWebUploadWrongToken(t *testing.T) {
	srv, _ := newTestServer(t)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "a.txt", 1)
	for _, c := range []struct {
		what, id, token string
	}{
		{"token errato", res.Slug, strings.Repeat("0", 32)},
		{"token troncato", res.Slug, res.Token[:31]},
		{"slug sconosciuto", "AbC123xY9z", res.Token},
	} {
		r := webPut(http.DefaultClient, srv.URL, c.id, c.token, strings.NewReader("x"), 1)
		if r.status != http.StatusNotFound {
			t.Errorf("%s: stato %d, atteso 404", c.what, r.status)
		}
	}
	done := make(chan webResult, 1)
	go func() { done <- webPut(http.DefaultClient, srv.URL, res.Slug, res.Token, strings.NewReader("x"), 1) }()
	dl := get(t, context.Background(), res.URL)
	got, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if r := <-done; string(got) != "x" || r.status != http.StatusOK {
		t.Fatalf("download %q, PUT stato %d", got, r.status)
	}
}

// TestWebUploadLengthMismatch: un body di lunghezza diversa da quella prenotata viene rifiutato, il
// link non è più valido e lo stream riporta length_mismatch.
func TestWebUploadLengthMismatch(t *testing.T) {
	srv, reg := newTestServer(t)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "a.txt", 10)
	r := webPut(http.DefaultClient, srv.URL, res.Slug, res.Token, strings.NewReader("12345"), 5)
	if r.status != http.StatusBadRequest {
		t.Fatalf("stato %d, atteso 400", r.status)
	}
	if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failLengthMismatch {
		t.Fatalf("esito stream %+v", last)
	}
	dl := get(t, context.Background(), res.URL)
	dl.Body.Close()
	if dl.StatusCode != http.StatusGone || reg.Len() != 0 {
		t.Fatalf("download dopo il rifiuto: stato %d, sessioni %d; attesi 410, 0", dl.StatusCode, reg.Len())
	}
}

// TestWebUploadUnbound: una prenotazione senza PUT entro bindTimeout si chiude. Il ricevente già
// collegato riceve 503, il link poi 410, lo stream l'esito sender_gone.
func TestWebUploadUnbound(t *testing.T) {
	old := bindTimeout
	bindTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bindTimeout = old })
	srv, reg := newTestServer(t)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "a.txt", 10)
	dl := within(t, 5*time.Second, "ricevente", func() *http.Response { return get(t, context.Background(), res.URL) })
	dl.Body.Close()
	if dl.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ricevente collegato: stato %d, atteso 503", dl.StatusCode)
	}
	if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failSenderGone {
		t.Fatalf("esito stream %+v", last)
	}
	dl = get(t, context.Background(), res.URL)
	dl.Body.Close()
	if dl.StatusCode != http.StatusGone || reg.Len() != 0 {
		t.Fatalf("dopo la chiusura: stato %d, sessioni %d; attesi 410, 0", dl.StatusCode, reg.Len())
	}
	if r := webPut(http.DefaultClient, srv.URL, res.Slug, res.Token, strings.NewReader("1234567890"), 10); r.status != http.StatusNotFound {
		t.Fatalf("PUT dopo la chiusura: stato %d, atteso 404", r.status)
	}
}

// TestWebUploadExpiredUnbound: senza ricevente né PUT la prenotazione scade come un upload, e lo
// stream lo dice.
func TestWebUploadExpiredUnbound(t *testing.T) {
	srv, reg := newWebServer(t, 200*time.Millisecond)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "a.txt", 10)
	if last, _ := res.outcome(t, 5*time.Second); last.Outcome != outcomeExpired {
		t.Fatalf("esito stream %+v", last)
	}
	waitLen(t, reg, 0)
}

// TestWebUploadStreamClosed: la chiusura dello stream (pagina chiusa, Cancel) chiude il link prima
// del ricevente, con o senza PUT in corso, e interrompe un download già iniziato. Il server lo vede
// anche con il body del PUT non letto, che da solo nasconderebbe la caduta del mittente.
func TestWebUploadStreamClosed(t *testing.T) {
	for proto, client := range outcomeClients() {
		t.Run(proto+" before put", func(t *testing.T) {
			srv, reg := newWebServer(t, time.Hour)
			res := reserveUpload(t, client, srv.URL, "a.bin", 10)
			res.close()
			waitLen(t, reg, 0)
			dl := get(t, context.Background(), res.URL)
			dl.Body.Close()
			if dl.StatusCode != http.StatusGone {
				t.Fatalf("download: stato %d, atteso 410", dl.StatusCode)
			}
			if r := webPut(client, srv.URL, res.Slug, res.Token, strings.NewReader("1234567890"), 10); r.status != http.StatusNotFound {
				t.Fatalf("PUT dopo la chiusura: stato %d, atteso 404", r.status)
			}
		})
		t.Run(proto+" waiting", func(t *testing.T) {
			srv, reg := newWebServer(t, time.Hour)
			pr, pw := io.Pipe()
			defer pw.Close()
			res := reserveUpload(t, client, srv.URL, "a.bin", 1<<20)
			done := make(chan webResult, 1)
			go func() { done <- webPut(client, srv.URL, res.Slug, res.Token, pr, 1<<20) }()
			go func() { _, _ = pw.Write(make([]byte, 64<<10)) }()
			time.Sleep(200 * time.Millisecond)
			res.close()
			waitLen(t, reg, 0)
			dl := get(t, context.Background(), res.URL)
			dl.Body.Close()
			if dl.StatusCode != http.StatusGone {
				t.Fatalf("download: stato %d, atteso 410", dl.StatusCode)
			}
			within(t, 5*time.Second, "fine del PUT", func() webResult { return <-done })
		})
		t.Run(proto+" sending", func(t *testing.T) {
			srv, reg := newWebServer(t, time.Hour)
			pr, pw := io.Pipe()
			defer pw.Close()
			res := reserveUpload(t, client, srv.URL, "a.bin", 1<<20)
			go func() { _ = webPut(client, srv.URL, res.Slug, res.Token, pr, 1<<20) }()
			go func() { _, _ = pw.Write(make([]byte, 4096)) }()
			dl := get(t, context.Background(), res.URL)
			defer dl.Body.Close()
			if _, err := io.ReadFull(dl.Body, make([]byte, 4096)); err != nil {
				t.Fatalf("download: %v", err)
			}
			res.close()
			err := within(t, 5*time.Second, "download interrotto", func() error {
				_, err := io.ReadAll(dl.Body)
				return err
			})
			if err == nil {
				t.Fatal("download terminato senza errore dopo la chiusura dello stream")
			}
			waitLen(t, reg, 0)
		})
	}
}

// cancelUpload invia l'annullamento della pagina: DELETE /<slug> con il token.
func cancelUpload(t *testing.T, client *http.Client, base, id, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, base+"/"+id, nil)
	if token != "" {
		req.Header.Set(tokenHeader, token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestWebUploadCancel: DELETE col token chiude subito il link, prima del PUT, con il PUT in attesa
// del ricevente e durante il download, anche con lo stream ancora aperto (Apache inoltra la sua
// chiusura in ritardo). Senza token o con un token errato non tocca la prenotazione.
func TestWebUploadCancel(t *testing.T) {
	for proto, client := range outcomeClients() {
		t.Run(proto+" before put", func(t *testing.T) {
			srv, reg := newWebServer(t, time.Hour)
			res := reserveUpload(t, client, srv.URL, "a.bin", 10)
			if st := cancelUpload(t, client, srv.URL, res.Slug, ""); st != http.StatusMethodNotAllowed {
				t.Fatalf("DELETE senza token: stato %d, atteso 405", st)
			}
			if st := cancelUpload(t, client, srv.URL, res.Slug, strings.Repeat("0", 32)); st != http.StatusNotFound || reg.Len() != 1 {
				t.Fatalf("DELETE token errato: stato %d, sessioni %d", st, reg.Len())
			}
			if st := cancelUpload(t, client, srv.URL, res.Slug, res.Token); st != http.StatusNoContent || reg.Len() != 0 {
				t.Fatalf("DELETE: stato %d, sessioni %d", st, reg.Len())
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failSenderGone {
				t.Fatalf("esito stream %+v", last)
			}
			dl := get(t, context.Background(), res.URL)
			dl.Body.Close()
			if dl.StatusCode != http.StatusGone {
				t.Fatalf("download: stato %d, atteso 410", dl.StatusCode)
			}
			if st := cancelUpload(t, client, srv.URL, res.Slug, res.Token); st != http.StatusNotFound {
				t.Fatalf("secondo DELETE: stato %d, atteso 404", st)
			}
		})
		t.Run(proto+" waiting", func(t *testing.T) {
			srv, reg := newWebServer(t, time.Hour)
			pr, pw := io.Pipe()
			defer pw.Close()
			res := reserveUpload(t, client, srv.URL, "a.bin", 1<<20)
			done := make(chan webResult, 1)
			go func() { done <- webPut(client, srv.URL, res.Slug, res.Token, pr, 1<<20) }()
			go func() { _, _ = pw.Write(make([]byte, 64<<10)) }()
			time.Sleep(200 * time.Millisecond)
			if st := cancelUpload(t, client, srv.URL, res.Slug, res.Token); st != http.StatusNoContent {
				t.Fatalf("DELETE: stato %d", st)
			}
			waitLen(t, reg, 0)
			dl := get(t, context.Background(), res.URL)
			dl.Body.Close()
			if dl.StatusCode != http.StatusGone {
				t.Fatalf("download: stato %d, atteso 410", dl.StatusCode)
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failSenderGone {
				t.Fatalf("esito stream %+v", last)
			}
			within(t, 5*time.Second, "fine del PUT", func() webResult { return <-done })
		})
		t.Run(proto+" sending", func(t *testing.T) {
			srv, reg := newWebServer(t, time.Hour)
			pr, pw := io.Pipe()
			defer pw.Close()
			res := reserveUpload(t, client, srv.URL, "a.bin", 1<<20)
			go func() { _ = webPut(client, srv.URL, res.Slug, res.Token, pr, 1<<20) }()
			go func() { _, _ = pw.Write(make([]byte, 4096)) }()
			dl := get(t, context.Background(), res.URL)
			defer dl.Body.Close()
			if _, err := io.ReadFull(dl.Body, make([]byte, 4096)); err != nil {
				t.Fatalf("download: %v", err)
			}
			if st := cancelUpload(t, client, srv.URL, res.Slug, res.Token); st != http.StatusNoContent {
				t.Fatalf("DELETE: stato %d", st)
			}
			err := within(t, 5*time.Second, "download interrotto", func() error {
				_, err := io.ReadAll(dl.Body)
				return err
			})
			if err == nil {
				t.Fatal("download terminato senza errore dopo l'annullamento")
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failSenderGone || last.Bytes != 4096 {
				t.Fatalf("esito stream %+v", last)
			}
			waitLen(t, reg, 0)
		})
	}
}

// TestWebUploadOutcomes: scadenza senza ricevente e ricevente caduto, con il PUT in corso. L'esito
// arriva nello stream e nella risposta del PUT, terminata regolarmente anche su HTTP/1
// (XMLHttpRequest non legge una risposta troncata).
func TestWebUploadOutcomes(t *testing.T) {
	for proto, client := range outcomeClients() {
		t.Run(proto+" expired", func(t *testing.T) {
			srv := newConnServer(t, 200*time.Millisecond)
			pr, pw := io.Pipe()
			defer pw.Close()
			res := reserveUpload(t, client, srv.URL, "a.bin", 1<<20)
			r := within(t, 5*time.Second, "esito", func() webResult {
				return webPut(client, srv.URL, res.Slug, res.Token, pr, 1<<20)
			})
			if r.err != nil || r.status != http.StatusOK {
				t.Fatalf("stato %d, err %v, %q", r.status, r.err, r.body)
			}
			if o, _ := lastOutcome(t, r.body); o != outcomeExpired {
				t.Fatalf("esito %q, atteso %q", o, outcomeExpired)
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != outcomeExpired {
				t.Fatalf("esito stream %+v", last)
			}
		})
		t.Run(proto+" receiver_gone", func(t *testing.T) {
			srv := newConnServer(t, time.Hour)
			pr, pw := io.Pipe()
			defer pw.Close()
			res := reserveUpload(t, client, srv.URL, "a.bin", 1<<20)
			done := make(chan webResult, 1)
			go func() { done <- webPut(client, srv.URL, res.Slug, res.Token, pr, 1<<20) }()
			dctx, dcancel := context.WithCancel(context.Background())
			go func() { _, _ = pw.Write(make([]byte, 4096)) }()
			dl := get(t, dctx, res.URL)
			if _, err := io.ReadFull(dl.Body, make([]byte, 4096)); err != nil {
				t.Fatalf("download: %v", err)
			}
			dcancel()
			dl.Body.Close()
			r := within(t, 5*time.Second, "esito", func() webResult { return <-done })
			if r.err != nil || r.status != http.StatusOK {
				t.Fatalf("stato %d, err %v, %q", r.status, r.err, r.body)
			}
			if o, n := lastOutcome(t, r.body); o != failReceiverGone || n != 4096 {
				t.Fatalf("esito %q %d, atteso %q 4096", o, n, failReceiverGone)
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failReceiverGone || last.Bytes != 4096 {
				t.Fatalf("esito stream %+v", last)
			}
		})
	}
}

// TestHomeCSP: la home può fare richieste solo al proprio server (l'upload) e nessun form.
func TestHomeCSP(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, _ := browserDo(t, http.MethodGet, srv.URL+"/")
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"connect-src 'self'", "form-action 'none'", "default-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q senza %q", csp, want)
		}
	}
}
