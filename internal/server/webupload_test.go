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

// newWebServer is newConnServer with the registry: HTTP/1 and h2c, receiver wait wait.
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

// webStream is a reservation with its NDJSON stream still open, as the page keeps it.
type webStream struct {
	reservation
	body io.Closer
	sc   *bufio.Scanner
}

// streamLine is a line of the stream after the reservation: an event or the outcome.
type streamLine struct {
	Event   string `json:"event"`
	Bytes   int64  `json:"bytes"`
	Outcome string `json:"outcome"`
}

// reserveUpload reserves an upload from the browser (POST / in JSON) and reads the first line of the
// stream. The stream stays open until close or the end of the test.
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
		t.Fatalf("reservation: status %d, %q", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("reservation: Content-Type %q", ct)
	}
	ws := &webStream{body: resp.Body, sc: bufio.NewScanner(resp.Body)}
	first := within(t, 5*time.Second, "reservation", func() bool { return ws.sc.Scan() })
	if !first || json.Unmarshal(ws.sc.Bytes(), &ws.reservation) != nil {
		t.Fatalf("reservation: first line %q, err %v", ws.sc.Bytes(), ws.sc.Err())
	}
	return ws
}

func (ws *webStream) close() { ws.body.Close() }

// outcome reads the stream up to the outcome and also returns the events met. It fails if the
// stream closes without an outcome or if the outcome does not arrive within d.
func (ws *webStream) outcome(t *testing.T, d time.Duration) (streamLine, []streamLine) {
	t.Helper()
	type result struct {
		last   streamLine
		events []streamLine
		err    string
	}
	r := within(t, d, "stream outcome", func() result {
		var events []streamLine
		for ws.sc.Scan() {
			var l streamLine
			if err := json.Unmarshal(ws.sc.Bytes(), &l); err != nil {
				return result{err: fmt.Sprintf("line %q: %v", ws.sc.Bytes(), err)}
			}
			if l.Outcome != "" {
				if ws.sc.Scan() {
					return result{err: fmt.Sprintf("line after the outcome: %q", ws.sc.Bytes())}
				}
				return result{last: l, events: events}
			}
			events = append(events, l)
		}
		return result{err: fmt.Sprintf("stream closed without outcome: %v", ws.sc.Err())}
	})
	if r.err != "" {
		t.Fatal(r.err)
	}
	return r.last, r.events
}

// webResult is the response to a PUT from the browser, read in full.
type webResult struct {
	status int
	body   string
	err    error
}

// webPut sends the body of reservation id with the token; the JSON outcome is on the last line of
// the response.
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

// lastOutcome decodes the JSON outcome from the last non-empty line.
func lastOutcome(t *testing.T, body string) (outcome string, bytes int64) {
	t.Helper()
	lines := strings.Fields(body)
	if len(lines) == 0 {
		t.Fatalf("response without outcome: %q", body)
	}
	var o struct {
		Outcome string  `json:"outcome"`
		Bytes   int64   `json:"bytes"`
		Seconds float64 `json:"seconds"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &o); err != nil {
		t.Fatalf("outcome %q: %v", body, err)
	}
	return o.Outcome, o.Bytes
}

// waitLen waits until the registry has n sessions.
func waitLen(t *testing.T, reg *relay.Registry, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); reg.Len() != n; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("sessions %d, expected %d", reg.Len(), n)
		}
	}
}

// TestReserve: the reservation returns slug, link, token and the receiver commands with the direct
// line, the same as the curl table.
func TestReserve(t *testing.T) {
	srv, reg := newTestServer(t)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "report finale.pdf", 1234)
	u := srv.URL + "/" + res.Slug
	if !slug.Valid(res.Slug) || res.URL != u || len(res.Token) != 32 {
		t.Fatalf("reservation %+v", res.reservation)
	}
	want := append(receiverCommands(u), command{"direct", u})
	if fmt.Sprint(res.Commands) != fmt.Sprint(want) {
		t.Fatalf("commands %v, expected %v", res.Commands, want)
	}
	s, err := reg.Peek(res.Slug)
	if err != nil || s.Name != "report finale.pdf" || s.Length != 1234 {
		t.Fatalf("session %+v, err %v", s, err)
	}
}

// TestReserveRejects: only valid JSON with a non-negative size creates a reservation.
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
			t.Errorf("%s %.40q: status %d, want %d", c.ctype, c.body, resp.StatusCode, c.status)
		}
	}
	if reg.Len() != 0 {
		t.Fatalf("sessions %d after rejected reservations, want 0", reg.Len())
	}
}

// TestReserveAuth: with authentication enabled the reservation requires it; the PUT with the token
// does not, because the token proves the reservation.
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
		t.Fatalf("no credentials: status %d, sessions %d; want 401, 0", resp.StatusCode, reg.Len())
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
		t.Fatalf("download %q, PUT status %d %q", got, r.status, r.body)
	}
}

// TestWebUploadRoundTrip: reservation, receiver and PUT with the token, over HTTP/1 and h2c (Apache).
// The receiver gets the bytes and the reservation name; the sender gets the "ok" outcome both in the
// reservation stream, after the attached event and the progress, and in the PUT response.
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
			// Half the body, a pause for at least one progress event, then the rest.
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
			r := within(t, 5*time.Second, "outcome PUT", func() webResult { return <-done })
			if r.err != nil || r.status != http.StatusOK {
				t.Fatalf("PUT: status %d, err %v", r.status, r.err)
			}
			if o, n := lastOutcome(t, r.body); o != outcomeOK || n != int64(len(data)) {
				t.Fatalf("outcome PUT %q %d", o, n)
			}
			last, events := res.outcome(t, 5*time.Second)
			if last.Outcome != outcomeOK || last.Bytes != int64(len(data)) {
				t.Fatalf("stream outcome %+v", last)
			}
			if len(events) < 2 || events[0].Event != "attached" {
				t.Fatalf("events %+v: want attached and progress", events)
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

// TestWebUploadWrongToken: a PUT with a wrong or empty token, or on an unknown slug, does not find the
// reservation; the correct one stays valid.
func TestWebUploadWrongToken(t *testing.T) {
	srv, _ := newTestServer(t)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "a.txt", 1)
	for _, c := range []struct {
		what, id, token string
	}{
		{"wrong token", res.Slug, strings.Repeat("0", 32)},
		{"token troncato", res.Slug, res.Token[:31]},
		{"unknown slug", "AbC123xY9z", res.Token},
	} {
		r := webPut(http.DefaultClient, srv.URL, c.id, c.token, strings.NewReader("x"), 1)
		if r.status != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", c.what, r.status)
		}
	}
	done := make(chan webResult, 1)
	go func() { done <- webPut(http.DefaultClient, srv.URL, res.Slug, res.Token, strings.NewReader("x"), 1) }()
	dl := get(t, context.Background(), res.URL)
	got, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if r := <-done; string(got) != "x" || r.status != http.StatusOK {
		t.Fatalf("download %q, PUT status %d", got, r.status)
	}
}

// TestWebUploadLengthMismatch: a body whose length differs from the reserved one is rejected, the
// link is no longer valid, and the stream reports length_mismatch.
func TestWebUploadLengthMismatch(t *testing.T) {
	srv, reg := newTestServer(t)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "a.txt", 10)
	r := webPut(http.DefaultClient, srv.URL, res.Slug, res.Token, strings.NewReader("12345"), 5)
	if r.status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", r.status)
	}
	if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failLengthMismatch {
		t.Fatalf("stream outcome %+v", last)
	}
	dl := get(t, context.Background(), res.URL)
	dl.Body.Close()
	if dl.StatusCode != http.StatusGone || reg.Len() != 0 {
		t.Fatalf("download after the rejection: status %d, sessions %d; want 410, 0", dl.StatusCode, reg.Len())
	}
}

// TestWebUploadUnbound: a reservation without a PUT within bindTimeout closes. The receiver that is
// already connected gets 503, the link then 410, and the stream the sender_gone outcome.
func TestWebUploadUnbound(t *testing.T) {
	old := bindTimeout
	bindTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bindTimeout = old })
	srv, reg := newTestServer(t)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "a.txt", 10)
	dl := within(t, 5*time.Second, "receiver", func() *http.Response { return get(t, context.Background(), res.URL) })
	dl.Body.Close()
	if dl.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("connected receiver: status %d, want 503", dl.StatusCode)
	}
	if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failSenderGone {
		t.Fatalf("stream outcome %+v", last)
	}
	dl = get(t, context.Background(), res.URL)
	dl.Body.Close()
	if dl.StatusCode != http.StatusGone || reg.Len() != 0 {
		t.Fatalf("after closing: status %d, sessions %d; want 410, 0", dl.StatusCode, reg.Len())
	}
	if r := webPut(http.DefaultClient, srv.URL, res.Slug, res.Token, strings.NewReader("1234567890"), 10); r.status != http.StatusNotFound {
		t.Fatalf("PUT after closing: status %d, want 404", r.status)
	}
}

// TestWebUploadExpiredUnbound: with neither receiver nor PUT the reservation expires like an upload,
// and the stream says so.
func TestWebUploadExpiredUnbound(t *testing.T) {
	srv, reg := newWebServer(t, 200*time.Millisecond)
	res := reserveUpload(t, http.DefaultClient, srv.URL, "a.txt", 10)
	if last, _ := res.outcome(t, 5*time.Second); last.Outcome != outcomeExpired {
		t.Fatalf("stream outcome %+v", last)
	}
	waitLen(t, reg, 0)
}

// TestWebUploadStreamClosed: closing the stream (page closed, Cancel) closes the link before the
// receiver does, with or without a PUT in progress, and interrupts a download already started. The
// server sees it even with the PUT body unread, which alone would hide the sender dropping.
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
				t.Fatalf("download: status %d, want 410", dl.StatusCode)
			}
			if r := webPut(client, srv.URL, res.Slug, res.Token, strings.NewReader("1234567890"), 10); r.status != http.StatusNotFound {
				t.Fatalf("PUT after closing: status %d, want 404", r.status)
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
				t.Fatalf("download: status %d, want 410", dl.StatusCode)
			}
			within(t, 5*time.Second, "end of PUT", func() webResult { return <-done })
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
			err := within(t, 5*time.Second, "interrupted download", func() error {
				_, err := io.ReadAll(dl.Body)
				return err
			})
			if err == nil {
				t.Fatal("download ended without error after the stream was closed")
			}
			waitLen(t, reg, 0)
		})
	}
}

// cancelUpload sends the page's cancellation: DELETE /<slug> with the token.
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

// TestWebUploadCancel: DELETE with the token closes the link at once, before the PUT, with the PUT
// waiting for the receiver and during the download, even with the stream still open (Apache forwards
// its close late). Without a token or with a wrong one it does not touch the reservation.
func TestWebUploadCancel(t *testing.T) {
	for proto, client := range outcomeClients() {
		t.Run(proto+" before put", func(t *testing.T) {
			srv, reg := newWebServer(t, time.Hour)
			res := reserveUpload(t, client, srv.URL, "a.bin", 10)
			if st := cancelUpload(t, client, srv.URL, res.Slug, ""); st != http.StatusMethodNotAllowed {
				t.Fatalf("DELETE without token: status %d, want 405", st)
			}
			if st := cancelUpload(t, client, srv.URL, res.Slug, strings.Repeat("0", 32)); st != http.StatusNotFound || reg.Len() != 1 {
				t.Fatalf("DELETE wrong token: status %d, sessions %d", st, reg.Len())
			}
			if st := cancelUpload(t, client, srv.URL, res.Slug, res.Token); st != http.StatusNoContent || reg.Len() != 0 {
				t.Fatalf("DELETE: status %d, sessions %d", st, reg.Len())
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failSenderGone {
				t.Fatalf("stream outcome %+v", last)
			}
			dl := get(t, context.Background(), res.URL)
			dl.Body.Close()
			if dl.StatusCode != http.StatusGone {
				t.Fatalf("download: status %d, want 410", dl.StatusCode)
			}
			if st := cancelUpload(t, client, srv.URL, res.Slug, res.Token); st != http.StatusNotFound {
				t.Fatalf("second DELETE: status %d, want 404", st)
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
				t.Fatalf("DELETE: status %d", st)
			}
			waitLen(t, reg, 0)
			dl := get(t, context.Background(), res.URL)
			dl.Body.Close()
			if dl.StatusCode != http.StatusGone {
				t.Fatalf("download: status %d, want 410", dl.StatusCode)
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failSenderGone {
				t.Fatalf("stream outcome %+v", last)
			}
			within(t, 5*time.Second, "end of PUT", func() webResult { return <-done })
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
				t.Fatalf("DELETE: status %d", st)
			}
			err := within(t, 5*time.Second, "interrupted download", func() error {
				_, err := io.ReadAll(dl.Body)
				return err
			})
			if err == nil {
				t.Fatal("download ended without error after the cancellation")
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failSenderGone || last.Bytes != 4096 {
				t.Fatalf("stream outcome %+v", last)
			}
			waitLen(t, reg, 0)
		})
	}
}

// TestWebUploadOutcomes: expiry without a receiver, and a receiver that dropped, with the PUT in
// progress. The outcome arrives in the stream and in the PUT response, which also ends cleanly over
// HTTP/1 (XMLHttpRequest does not read a truncated response).
func TestWebUploadOutcomes(t *testing.T) {
	for proto, client := range outcomeClients() {
		t.Run(proto+" expired", func(t *testing.T) {
			srv := newConnServer(t, 200*time.Millisecond)
			pr, pw := io.Pipe()
			defer pw.Close()
			res := reserveUpload(t, client, srv.URL, "a.bin", 1<<20)
			r := within(t, 5*time.Second, "outcome", func() webResult {
				return webPut(client, srv.URL, res.Slug, res.Token, pr, 1<<20)
			})
			if r.err != nil || r.status != http.StatusOK {
				t.Fatalf("status %d, err %v, %q", r.status, r.err, r.body)
			}
			if o, _ := lastOutcome(t, r.body); o != outcomeExpired {
				t.Fatalf("outcome %q, want %q", o, outcomeExpired)
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != outcomeExpired {
				t.Fatalf("stream outcome %+v", last)
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
			r := within(t, 5*time.Second, "outcome", func() webResult { return <-done })
			if r.err != nil || r.status != http.StatusOK {
				t.Fatalf("status %d, err %v, %q", r.status, r.err, r.body)
			}
			if o, n := lastOutcome(t, r.body); o != failReceiverGone || n != 4096 {
				t.Fatalf("outcome %q %d, want %q 4096", o, n, failReceiverGone)
			}
			if last, _ := res.outcome(t, 5*time.Second); last.Outcome != failReceiverGone || last.Bytes != 4096 {
				t.Fatalf("stream outcome %+v", last)
			}
		})
	}
}

// TestHomeCSP: the home page can only make requests to its own server (the upload) and has no form.
func TestHomeCSP(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, _ := browserDo(t, http.MethodGet, srv.URL+"/")
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"connect-src 'self'", "form-action 'none'", "default-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q without %q", csp, want)
		}
	}
}
