package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"bashtransfer/internal/relay"
)

// newGraceServer è newWebServer con la tolleranza BASHT_SENDER_GRACE di grace.
func newGraceServer(t *testing.T, grace time.Duration) (srvURL string, reg *relay.Registry) {
	t.Helper()
	srv, reg := newWebServer(t, time.Hour)
	reg.SetSenderGrace(grace)
	return srv.URL, reg
}

// resumeStream riapre lo stream di una prenotazione come la pagina dopo una caduta.
func resumeStream(t *testing.T, client *http.Client, base string, res *webStream) *webStream {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(`{"resume":"`+res.Slug+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, res.Token)
	return openStream(t, client, req)
}

// putUntilCancelled invia un PUT che resta in attesa del ricevente e restituisce la funzione che ne
// chiude la connessione, come un telefono che perde la rete.
func putUntilCancelled(t *testing.T, base string, res *webStream, size int64) (drop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, base+"/"+res.Slug, pr)
	req.ContentLength = size
	req.Header.Set(tokenHeader, res.Token)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := (&http.Client{}).Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	go func() { _, _ = pw.Write(make([]byte, 4)) }()
	return func() {
		cancel()
		pw.Close()
		within(t, 5*time.Second, "fine del PUT caduto", func() bool { <-done; return true })
	}
}

// waitAway attende che la sessione sia viva e senza mittente agganciato (in tolleranza).
func waitAway(t *testing.T, reg *relay.Registry, res *webStream) *relay.Session {
	t.Helper()
	var s *relay.Session
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if got, err := reg.Reserved(res.Slug, res.Token); err == nil && got.Away() {
			s = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("la sessione non è andata in tolleranza entro 5 s")
		}
	}
	return s
}

// TestSenderGraceReservation: la prenotazione dichiara la tolleranza in secondi; senza, il campo
// manca; la ripresa dello stream è rifiutata se la tolleranza è disattivata.
func TestSenderGraceReservation(t *testing.T) {
	url, _ := newGraceServer(t, 90*time.Second)
	res := reserveUpload(t, &http.Client{}, url, "a.bin", 10)
	if res.Grace != 90 {
		t.Errorf("grace %d, attesi 90", res.Grace)
	}

	srv, _ := newWebServer(t, time.Hour)
	off := reserveUpload(t, &http.Client{}, srv.URL, "a.bin", 10)
	if off.Grace != 0 {
		t.Errorf("senza tolleranza grace %d, atteso 0", off.Grace)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(`{"resume":"`+off.Slug+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, off.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("resume senza tolleranza: stato %d, atteso 404", resp.StatusCode)
	}
}

// TestSenderGraceRebind: il browser attende senza consumare il link; il mittente torna
// con un nuovo PUT e il download successivo arriva intero.
func TestSenderGraceRebind(t *testing.T) {
	base, reg := newGraceServer(t, time.Minute)
	client := &http.Client{}
	data := randomBytes(t, 64<<10)
	res := reserveUpload(t, client, base, "report.bin", int64(len(data)))
	drop := putUntilCancelled(t, base, res, int64(len(data)))
	time.Sleep(100 * time.Millisecond)
	drop()
	res.close()
	waitAway(t, reg, res)

	// La pagina di download avvisa che il mittente è assente.
	_, body := browserDo(t, http.MethodGet, base+"/"+res.Slug)
	wantContains(t, "pagina con mittente assente", body, "The sender is not connected right now")

	// Il pulsante non deve avviare un download né consumare il link mentre il mittente è assente.
	req, _ := http.NewRequest(http.MethodPost, base+"/"+res.Slug, strings.NewReader(""))
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	waiting := within(t, 5*time.Second, "pagina in attesa del mittente", func() *http.Response {
		r, err := client.Do(req)
		if err != nil {
			t.Error(err)
			return nil
		}
		return r
	})
	if waiting == nil {
		t.FailNow()
	}
	waiting.Body.Close()
	if waiting.StatusCode != http.StatusOK || !strings.HasPrefix(waiting.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("attesa: stato %d, Content-Type %q", waiting.StatusCode, waiting.Header.Get("Content-Type"))
	}
	if _, err := reg.Peek(res.Slug); err != nil {
		t.Fatalf("il pulsante in attesa ha consumato il link: %v", err)
	}

	// Il mittente torna: nuovo stream e nuovo PUT con lo stesso token.
	again := resumeStream(t, client, base, res)
	if again.Slug != res.Slug || again.Grace != 60 {
		t.Fatalf("stream ripreso: slug %q grace %d", again.Slug, again.Grace)
	}
	put := make(chan webResult, 1)
	go func() {
		put <- webPut(client, base, res.Slug, res.Token, strings.NewReader(string(data)), int64(len(data)))
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if s, err := reg.Reserved(res.Slug, res.Token); err == nil && !s.Away() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mittente non riagganciato")
		}
	}
	dl, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close()
	got := within(t, 10*time.Second, "download dopo il ritorno del mittente", func() []byte {
		b, _ := io.ReadAll(dl.Body)
		return b
	})
	if string(got) != string(data) {
		t.Fatalf("download: %d byte, attesi %d", len(got), len(data))
	}
	if last, _ := again.outcome(t, 5*time.Second); last.Outcome != outcomeOK {
		t.Fatalf("esito stream %+v", last)
	}
	if r := within(t, 5*time.Second, "fine del PUT", func() webResult { return <-put }); r.err != nil {
		t.Fatalf("PUT: %v", r.err)
	}
}

// TestSenderGraceTakeover: il mittente torna prima che il server veda cadere il vecchio PUT: il nuovo
// lo sostituisce e il trasferimento riesce.
func TestSenderGraceTakeover(t *testing.T) {
	base, reg := newGraceServer(t, time.Minute)
	client := &http.Client{}
	data := randomBytes(t, 32<<10)
	res := reserveUpload(t, client, base, "a.bin", int64(len(data)))
	// Il vecchio PUT resta aperto: il server lo crede ancora agganciato.
	pr, pw := io.Pipe()
	defer pw.Close()
	old := make(chan webResult, 1)
	go func() { old <- webPut(client, base, res.Slug, res.Token, pr, int64(len(data))) }()
	go func() { _, _ = pw.Write(make([]byte, 4)) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if s, err := reg.Reserved(res.Slug, res.Token); err == nil && !s.Away() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PUT non agganciato")
		}
	}

	put := make(chan webResult, 1)
	go func() {
		put <- webPut(client, base, res.Slug, res.Token, strings.NewReader(string(data)), int64(len(data)))
	}()
	time.Sleep(200 * time.Millisecond)
	dl := get(t, context.Background(), base+"/"+res.Slug)
	defer dl.Body.Close()
	got := within(t, 10*time.Second, "download dopo il subentro", func() []byte {
		b, _ := io.ReadAll(dl.Body)
		return b
	})
	if string(got) != string(data) {
		t.Fatalf("download: %d byte, attesi %d", len(got), len(data))
	}
	if r := within(t, 5*time.Second, "fine del nuovo PUT", func() webResult { return <-put }); r.err != nil {
		t.Fatalf("nuovo PUT: %v", r.err)
	}
	pw.Close()
	within(t, 5*time.Second, "fine del vecchio PUT", func() webResult { return <-old })
	waitLen(t, reg, 0)
}

// TestSenderGraceExpires: se il mittente non torna entro la tolleranza il link si chiude: un
// ricevente in attesa (curl) riceve l'errore, e dopo il link dice che il mittente si è fermato.
func TestSenderGraceExpires(t *testing.T) {
	base, reg := newGraceServer(t, 400*time.Millisecond)
	client := &http.Client{}
	res := reserveUpload(t, client, base, "a.bin", 100)
	drop := putUntilCancelled(t, base, res, 100)
	time.Sleep(100 * time.Millisecond)
	drop()
	waitAway(t, reg, res)

	resp := get(t, context.Background(), base+"/"+res.Slug)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("ricevente in attesa alla scadenza: stato %d (%q), atteso 503", resp.StatusCode, b)
	}
	waitLen(t, reg, 0)
	check, body := browserDo(t, http.MethodGet, base+"/"+res.Slug)
	if check.StatusCode != http.StatusGone {
		t.Errorf("dopo la scadenza: stato %d, atteso 410", check.StatusCode)
	}
	wantContains(t, "dopo la scadenza", body, "Sender stopped")
	// La ripresa dello stream non riapre una sessione chiusa.
	req, _ := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(`{"resume":"`+res.Slug+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, res.Token)
	r2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Errorf("resume dopo la scadenza: stato %d, atteso 404", r2.StatusCode)
	}
}

// TestSenderGraceCancelStillCloses: Cancel (DELETE) chiude subito il link anche con la tolleranza,
// da mittente agganciato e da mittente caduto.
func TestSenderGraceCancelStillCloses(t *testing.T) {
	base, reg := newGraceServer(t, time.Hour)
	client := &http.Client{}

	res := reserveUpload(t, client, base, "a.bin", 100)
	drop := putUntilCancelled(t, base, res, 100)
	time.Sleep(100 * time.Millisecond)
	if st := cancelUpload(t, client, base, res.Slug, res.Token); st != http.StatusNoContent {
		t.Fatalf("DELETE da agganciato: stato %d", st)
	}
	waitLen(t, reg, 0)
	drop()

	res = reserveUpload(t, client, base, "b.bin", 100)
	drop = putUntilCancelled(t, base, res, 100)
	time.Sleep(100 * time.Millisecond)
	drop()
	waitAway(t, reg, res)
	if st := cancelUpload(t, client, base, res.Slug, res.Token); st != http.StatusNoContent {
		t.Fatalf("DELETE da caduto: stato %d", st)
	}
	waitLen(t, reg, 0)
}

// TestSenderGraceStreamLoss: la caduta dello stream, con il PUT ancora aperto ma con il body non
// letto (la sua chiusura non si vedrebbe), mette il mittente in tolleranza: il link resta vivo
// senza mittente. Un secondo stream aperto tiene il mittente agganciato.
func TestSenderGraceStreamLoss(t *testing.T) {
	base, reg := newGraceServer(t, time.Hour)
	client := &http.Client{}
	res := reserveUpload(t, client, base, "a.bin", 100)
	drop := putUntilCancelled(t, base, res, 100)
	defer drop()
	time.Sleep(100 * time.Millisecond)

	// Con uno stream ripreso aperto, la chiusura del primo non toglie il mittente.
	again := resumeStream(t, client, base, res)
	res.close()
	time.Sleep(500 * time.Millisecond)
	if s, err := reg.Reserved(res.Slug, res.Token); err != nil || s.Away() {
		t.Fatalf("mittente tolto con uno stream ancora aperto: %v", err)
	}
	again.close()
	s := waitAway(t, reg, res)
	if reg.Len() != 1 || s.Err() != nil {
		t.Fatalf("sessioni %d, Err %v dopo la caduta dello stream, attesa sessione viva", reg.Len(), s.Err())
	}
}

func TestSenderGraceLiveStatus(t *testing.T) {
	base, reg := newGraceServer(t, time.Minute)
	res := reserveUpload(t, http.DefaultClient, base, "private.bin", 10)
	drop := putUntilCancelled(t, base, res, 10)
	defer drop()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if s, err := reg.Reserved(res.Slug, res.Token); err == nil && !s.Away() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PUT non agganciato")
		}
	}
	check := func(want string, code int) {
		t.Helper()
		r, err := http.Get(base + "/" + res.Slug + "?status=1")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		var m struct{ State string }
		if json.Unmarshal(body, &m) != nil || m.State != want || r.StatusCode != code {
			t.Fatalf("stato: %d %q, atteso %d %q", r.StatusCode, body, code, want)
		}
		if r.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(body), res.Token) || strings.Contains(string(body), "private.bin") {
			t.Fatalf("header o dati sensibili nello stato: %q", body)
		}
	}
	check("ready", http.StatusOK)
	check("ready", http.StatusOK)
	req, _ := http.NewRequest(http.MethodPost, base+"/"+res.Slug, strings.NewReader(`{"foreground":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, res.Token)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("presenza: stato %d", r.StatusCode)
	}
	check("away", http.StatusOK)
	if _, err := reg.Peek(res.Slug); err != nil {
		t.Fatalf("lo stato ha consumato il link: %v", err)
	}
	cancelUpload(t, http.DefaultClient, base, res.Slug, res.Token)
	check(reasonGoneSender, http.StatusGone)
}

func TestCancelInterruptsBlockedReceiver(t *testing.T) {
	base, reg := newGraceServer(t, time.Minute)
	const size = 16 << 20
	res := reserveUpload(t, http.DefaultClient, base, "blocked.bin", size)
	put := make(chan webResult, 1)
	go func() {
		put <- webPut(http.DefaultClient, base, res.Slug, res.Token, strings.NewReader(strings.Repeat("x", size)), size)
	}()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.(*net.TCPConn).SetReadBuffer(4096)
	fmt.Fprintf(conn, "GET /%s HTTP/1.1\r\nHost: localhost\r\n\r\n", res.Slug)
	if _, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil {
		t.Fatal(err)
	}
	s, err := reg.Reserved(res.Slug, res.Token)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		n := s.Delivered()
		time.Sleep(100 * time.Millisecond)
		if n > 0 && n == s.Delivered() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("il ricevente non è rimasto bloccato")
		}
	}
	if status := cancelUpload(t, http.DefaultClient, base, res.Slug, res.Token); status != http.StatusNoContent {
		t.Fatalf("Cancel: stato %d", status)
	}
	select {
	case <-put:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel non sblocca una scrittura verso un ricevente che non legge")
	}
	waitLen(t, reg, 0)
}

func TestSenderGraceResumeControlDuringRelay(t *testing.T) {
	base, reg := newGraceServer(t, time.Minute)
	data := "the whole file"
	res := reserveUpload(t, http.DefaultClient, base, "a.bin", int64(len(data)))
	if err := reg.Foreground(res.Slug, res.Token, true); err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	defer pw.Close()
	put := make(chan webResult, 1)
	go func() { put <- webPut(http.DefaultClient, base, res.Slug, res.Token, pr, int64(len(data))) }()
	go func() { _, _ = pw.Write([]byte(data[:1])) }()
	dl := get(t, context.Background(), base+"/"+res.Slug)
	defer dl.Body.Close()
	s, err := reg.Reserved(res.Slug, res.Token)
	if err != nil {
		t.Fatal(err)
	}
	<-s.Started()
	res.close()
	req, _ := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(`{"resume":"`+res.Slug+`","rebind":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, res.Token)
	again := openStream(t, http.DefaultClient, req)
	if !again.Started {
		t.Fatal("lo stream ripreso non segnala il relay già iniziato")
	}
	go func() { _, _ = pw.Write([]byte(data[1:])); pw.Close() }()
	got := within(t, 5*time.Second, "body dopo il recupero del controllo", func() string {
		b, _ := io.ReadAll(dl.Body)
		return string(b)
	})
	if got != data {
		t.Fatalf("body %q, atteso %q", got, data)
	}
	if last, _ := again.outcome(t, 5*time.Second); last.Outcome != outcomeOK {
		t.Fatalf("esito %+v", last)
	}
	if r := <-put; r.err != nil {
		t.Fatal(r.err)
	}
}
