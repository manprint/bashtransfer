package server

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Pages are compressed only for clients that accept gzip; the content is the same.
func TestPagesGzip(t *testing.T) {
	srv, _ := newTestServer(t)
	fetch := func(enc string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
		req.Header.Set("Accept", "text/html")
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		// DisableCompression: the transport must not add or undo the encoding.
		resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	plainResp, plain := fetch("")
	if plainResp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("encoding without Accept-Encoding: %q", plainResp.Header.Get("Content-Encoding"))
	}
	zResp, z := fetch("gzip, br")
	if zResp.Header.Get("Content-Encoding") != "gzip" || !strings.Contains(strings.Join(zResp.Header.Values("Vary"), ","), "Accept-Encoding") {
		t.Fatalf("Content-Encoding %q, Vary %q", zResp.Header.Get("Content-Encoding"), zResp.Header.Values("Vary"))
	}
	zr, err := gzip.NewReader(strings.NewReader(string(z)))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if string(got) != string(plain) || len(z)*2 > len(plain) {
		t.Fatalf("gzip page: %d bytes compressed, %d plain, equal %v", len(z), len(plain), string(got) == string(plain))
	}
}

type recordingWriter struct {
	http.ResponseWriter
	sizes []int
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.sizes = append(w.sizes, len(p))
	return w.ResponseWriter.Write(p)
}

func (w *recordingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// A large buffer reaches the receiver in writes of at most writeChunk, each with its own deadline.
func TestReceiverWritesInChunks(t *testing.T) {
	w := &recordingWriter{ResponseWriter: httptest.NewRecorder()}
	r := newResponseReceiver(w, time.Minute)
	n, err := r.Write(make([]byte, 3*writeChunk+10))
	if err != nil || n != 3*writeChunk+10 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(w.sizes) != 4 || w.sizes[0] != writeChunk || w.sizes[3] != 10 {
		t.Fatalf("writes %v", w.sizes)
	}
	r.stop()
	if n, err := r.Write(make([]byte, 10)); n != 0 || err == nil {
		t.Fatalf("Write after stop = %d, %v", n, err)
	}
}

// With readiness negotiated the stream carries the confirmation on the keepalive line: no
// separate readiness line per tick.
func TestReserveStreamOneLinePerTick(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(`{"name":"a.bin","size":10,"readiness":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		line := within(t, 5*time.Second, "stream line", func() string { l, _ := br.ReadString('\n'); return l })
		var ev struct {
			Event string `json:"event"`
			Ready *bool  `json:"ready"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Event != "waiting" || ev.Ready == nil || *ev.Ready {
			t.Fatalf("line %d: %q", i, line)
		}
	}
}
