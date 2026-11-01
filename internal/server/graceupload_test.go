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

// newGraceServer is newWebServer with the BASHT_SENDER_GRACE tolerance set to grace.
func newGraceServer(t *testing.T, grace time.Duration) (srvURL string, reg *relay.Registry) {
	t.Helper()
	srv, reg := newWebServer(t, time.Hour)
	reg.SetSenderGrace(grace)
	return srv.URL, reg
}

// resumeStream reopens the stream of a reservation, as the page does after a drop.
func resumeStream(t *testing.T, client *http.Client, base string, res *webStream) *webStream {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(`{"resume":"`+res.Slug+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, res.Token)
	return openStream(t, client, req)
}

// putUntilCancelled sends a PUT that keeps waiting for the receiver and returns the function that
// closes its connection, like a phone losing the network.
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
		within(t, 5*time.Second, "end of dropped PUT", func() bool { <-done; return true })
	}
}

// waitAway waits until the session is alive and has no sender attached (within the tolerance).
func waitAway(t *testing.T, reg *relay.Registry, res *webStream) *relay.Session {
	t.Helper()
	var s *relay.Session
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if got, err := reg.Reserved(res.Slug, res.Token); err == nil && got.Away() {
			s = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session did not enter the tolerance within 5 s")
		}
	}
	return s
}

// TestSenderGraceReservation: the reservation declares the tolerance in seconds; without it the field
// is missing; resuming the stream is rejected if the tolerance is disabled.
func TestSenderGraceReservation(t *testing.T) {
	url, _ := newGraceServer(t, 90*time.Second)
	res := reserveUpload(t, &http.Client{}, url, "a.bin", 10)
	if res.Grace != 90 {
		t.Errorf("grace %d, want 90", res.Grace)
	}

	srv, _ := newWebServer(t, time.Hour)
	off := reserveUpload(t, &http.Client{}, srv.URL, "a.bin", 10)
	if off.Grace != 0 {
		t.Errorf("without tolerance grace %d, want 0", off.Grace)
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
		t.Errorf("resume without tolerance: status %d, want 404", resp.StatusCode)
	}
}

// TestSenderGraceRebind: the browser waits without consuming the link; the sender returns with a new
// PUT and the following download arrives whole.
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

	// The download page warns that the sender is absent.
	_, body := browserDo(t, http.MethodGet, base+"/"+res.Slug)
	wantContains(t, "page with absent sender", body, "The sender is not connected right now")

	// The button must neither start a download nor consume the link while the sender is absent.
	req, _ := http.NewRequest(http.MethodPost, base+"/"+res.Slug, strings.NewReader(""))
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	waiting := within(t, 5*time.Second, "page waiting for the sender", func() *http.Response {
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
		t.Fatalf("wait: status %d, Content-Type %q", waiting.StatusCode, waiting.Header.Get("Content-Type"))
	}
	if _, err := reg.Peek(res.Slug); err != nil {
		t.Fatalf("the waiting button consumed the link: %v", err)
	}

	// The sender returns: new stream and new PUT with the same token.
	again := resumeStream(t, client, base, res)
	if again.Slug != res.Slug || again.Grace != 60 {
		t.Fatalf("resumed stream: slug %q grace %d", again.Slug, again.Grace)
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
			t.Fatal("sender not reattached")
		}
	}
	dl, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close()
	got := within(t, 10*time.Second, "download after the sender returned", func() []byte {
		b, _ := io.ReadAll(dl.Body)
		return b
	})
	if string(got) != string(data) {
		t.Fatalf("download: %d bytes, want %d", len(got), len(data))
	}
	if last, _ := again.outcome(t, 5*time.Second); last.Outcome != outcomeOK {
		t.Fatalf("stream outcome %+v", last)
	}
	if r := within(t, 5*time.Second, "end of PUT", func() webResult { return <-put }); r.err != nil {
		t.Fatalf("PUT: %v", r.err)
	}
}

// TestSenderGraceTakeover: the sender returns before the server sees the old PUT drop: the new one
// replaces it and the transfer succeeds.
func TestSenderGraceTakeover(t *testing.T) {
	base, reg := newGraceServer(t, time.Minute)
	client := &http.Client{}
	data := randomBytes(t, 32<<10)
	res := reserveUpload(t, client, base, "a.bin", int64(len(data)))
	// The old PUT stays open: the server still considers it attached.
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
			t.Fatal("PUT not reattached")
		}
	}

	put := make(chan webResult, 1)
	go func() {
		put <- webPut(client, base, res.Slug, res.Token, strings.NewReader(string(data)), int64(len(data)))
	}()
	time.Sleep(200 * time.Millisecond)
	dl := get(t, context.Background(), base+"/"+res.Slug)
	defer dl.Body.Close()
	got := within(t, 10*time.Second, "download after takeover", func() []byte {
		b, _ := io.ReadAll(dl.Body)
		return b
	})
	if string(got) != string(data) {
		t.Fatalf("download: %d bytes, want %d", len(got), len(data))
	}
	if r := within(t, 5*time.Second, "end of new PUT", func() webResult { return <-put }); r.err != nil {
		t.Fatalf("new PUT: %v", r.err)
	}
	pw.Close()
	within(t, 5*time.Second, "end of old PUT", func() webResult { return <-old })
	waitLen(t, reg, 0)
}

// TestSenderGraceExpires: if the sender does not return within the tolerance, the link closes: a
// receiver waiting (curl) gets the error, and afterwards the link says the sender has stopped.
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
		t.Errorf("receiver waiting at expiry: status %d (%q), want 503", resp.StatusCode, b)
	}
	waitLen(t, reg, 0)
	check, body := browserDo(t, http.MethodGet, base+"/"+res.Slug)
	if check.StatusCode != http.StatusGone {
		t.Errorf("after expiry: status %d, want 410", check.StatusCode)
	}
	wantContains(t, "after expiry", body, "Sender stopped")
	// Resuming the stream does not reopen a closed session.
	req, _ := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(`{"resume":"`+res.Slug+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, res.Token)
	r2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Errorf("resume after expiry: status %d, want 404", r2.StatusCode)
	}
}

// TestSenderGraceCancelStillCloses: Cancel (DELETE) closes the link immediately even within the
// grace period, both from an attached sender and from a dropped one.
func TestSenderGraceCancelStillCloses(t *testing.T) {
	base, reg := newGraceServer(t, time.Hour)
	client := &http.Client{}

	res := reserveUpload(t, client, base, "a.bin", 100)
	drop := putUntilCancelled(t, base, res, 100)
	time.Sleep(100 * time.Millisecond)
	if st := cancelUpload(t, client, base, res.Slug, res.Token); st != http.StatusNoContent {
		t.Fatalf("DELETE from attached sender: status %d", st)
	}
	waitLen(t, reg, 0)
	drop()

	res = reserveUpload(t, client, base, "b.bin", 100)
	drop = putUntilCancelled(t, base, res, 100)
	time.Sleep(100 * time.Millisecond)
	drop()
	waitAway(t, reg, res)
	if st := cancelUpload(t, client, base, res.Slug, res.Token); st != http.StatusNoContent {
		t.Fatalf("DELETE from dropped sender: status %d", st)
	}
	waitLen(t, reg, 0)
}

// TestSenderGraceStreamLoss: the stream drops while the PUT is still open but its body is unread
// (its closure would not be visible), so the sender enters the grace period: the link stays alive
// without a sender. A second open stream keeps the sender attached.
func TestSenderGraceStreamLoss(t *testing.T) {
	base, reg := newGraceServer(t, time.Hour)
	client := &http.Client{}
	res := reserveUpload(t, client, base, "a.bin", 100)
	drop := putUntilCancelled(t, base, res, 100)
	defer drop()
	time.Sleep(100 * time.Millisecond)

	// With a resumed stream still open, closing the first one does not remove the sender.
	again := resumeStream(t, client, base, res)
	res.close()
	time.Sleep(500 * time.Millisecond)
	if s, err := reg.Reserved(res.Slug, res.Token); err != nil || s.Away() {
		t.Fatalf("sender removed while a stream is still open: %v", err)
	}
	again.close()
	s := waitAway(t, reg, res)
	if reg.Len() != 1 || s.Err() != nil {
		t.Fatalf("sessions %d, Err %v after stream loss, expected a live session", reg.Len(), s.Err())
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
			t.Fatal("PUT not attached")
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
			t.Fatalf("status: %d %q, expected %d %q", r.StatusCode, body, code, want)
		}
		if r.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(body), res.Token) || strings.Contains(string(body), "private.bin") {
			t.Fatalf("header or sensitive data in status: %q", body)
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
		t.Fatalf("presence: status %d", r.StatusCode)
	}
	check("away", http.StatusOK)
	if _, err := reg.Peek(res.Slug); err != nil {
		t.Fatalf("status check consumed the link: %v", err)
	}
	cancelUpload(t, http.DefaultClient, base, res.Slug, res.Token)
	check(reasonGoneSender, http.StatusGone)
}

func TestBrowserPeerReadiness(t *testing.T) {
	base, reg := newGraceServer(t, time.Minute)
	req, _ := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(`{"name":"private.bin","size":10,"foreground":true,"readiness":true}`))
	req.Header.Set("Content-Type", "application/json")
	res := openStream(t, http.DefaultClient, req)
	defer res.close()
	if res.Ready == nil || *res.Ready || res.ReadyTTL != 5 {
		t.Fatalf("reservation without receiver: %+v", res.reservation)
	}
	check := func(enhanced bool, state string, code int, ready bool) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/"+res.Slug+"?status=1", nil)
		if enhanced {
			req.Header.Set("X-Bashtransfer-Readiness", "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var m struct {
			State string `json:"state"`
			Ready *bool  `json:"ready"`
		}
		if json.Unmarshal(body, &m) != nil || resp.StatusCode != code || m.State != state ||
			(enhanced && (m.Ready == nil || *m.Ready != ready)) || (!enhanced && m.Ready != nil) {
			t.Fatalf("status enhanced=%v: %d %q", enhanced, resp.StatusCode, body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(body), res.Token) || strings.Contains(string(body), "private.bin") {
			t.Fatalf("unsafe status: %q", body)
		}
	}
	next := func(want bool) {
		t.Helper()
		got := within(t, 5*time.Second, "confirmation in the stream", func() *bool {
			for res.sc.Scan() {
				var m struct {
					Ready *bool `json:"ready"`
				}
				if json.Unmarshal(res.sc.Bytes(), &m) == nil && m.Ready != nil {
					return m.Ready
				}
			}
			return nil
		})
		if got == nil {
			t.Fatalf("stream closed without confirmation: %v", res.sc.Err())
		}
		if *got != want {
			t.Fatalf("stream READY=%v, expected %v", *got, want)
		}
	}
	check(true, "away", http.StatusOK, false)
	drop := putUntilCancelled(t, base, res, 10)
	defer drop()
	var s *relay.Session
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		s, _ = reg.Reserved(res.Slug, res.Token)
		if s != nil && !s.Away() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PUT not attached")
		}
	}
	// The first observation happened while the body was absent.
	next(true)
	check(false, "ready", http.StatusOK, false)
	check(true, "ready", http.StatusOK, true)
	if _, err := reg.Peek(res.Slug); err != nil {
		t.Fatalf("control check consumed the link: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.Get(base + "/" + res.Slug); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	within(t, 5*time.Second, "relay start", func() bool { <-s.Started(); return true })
	check(true, "active", http.StatusOK, true)
	check(false, "claimed", http.StatusGone, false)
	if err := reg.Foreground(res.Slug, res.Token, false); err != nil {
		t.Fatal(err)
	}
	check(true, "active", http.StatusOK, false)
	cancelUpload(t, http.DefaultClient, base, res.Slug, res.Token)
	within(t, 5*time.Second, "receiver end", func() bool { <-done; return true })
	if s.PeerReady() {
		t.Fatal("cancelled session still READY")
	}
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
			t.Fatal("the receiver did not stay blocked")
		}
	}
	if status := cancelUpload(t, http.DefaultClient, base, res.Slug, res.Token); status != http.StatusNoContent {
		t.Fatalf("Cancel: status %d", status)
	}
	select {
	case <-put:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel does not unblock a write to a receiver that does not read")
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
		t.Fatal("the resumed stream does not report that the relay has already started")
	}
	go func() { _, _ = pw.Write([]byte(data[1:])); pw.Close() }()
	got := within(t, 5*time.Second, "body after control recovery", func() string {
		b, _ := io.ReadAll(dl.Body)
		return string(b)
	})
	if got != data {
		t.Fatalf("body %q, expected %q", got, data)
	}
	if last, _ := again.outcome(t, 5*time.Second); last.Outcome != outcomeOK {
		t.Fatalf("outcome %+v", last)
	}
	if r := <-put; r.err != nil {
		t.Fatal(r.err)
	}
}
