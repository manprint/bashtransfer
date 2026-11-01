package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bashtransfer/internal/relay"
)

// browserDo performs a request as a browser (Accept with text/html) and returns the response with
// the body read.
func browserDo(t *testing.T, method, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0 Safari/537.36")
	resp := within(t, 10*time.Second, method+" "+url, func() *http.Response {
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
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func wantContains(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s: missing %q", what, w)
		}
	}
}

// TestLandingDoesNotConsume: the browser and the previews see the download page as many times as
// they like, without consuming the link; the button (POST) downloads the whole file; afterwards the
// page says the link is used.
func TestLandingDoesNotConsume(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()
	data := randomBytes(t, 1<<20)
	u := openUpload(t, ctx, srv.URL+"/report.bin", bytes.NewReader(data))
	link := srv.URL + "/" + u.slug

	for i := 0; i < 2; i++ {
		resp, body := browserDo(t, http.MethodGet, link)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("download page: status %d", resp.StatusCode)
		}
		h := resp.Header
		if ct := h.Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("Content-Type %q", ct)
		}
		if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") || !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("CSP %q", csp)
		}
		if h.Get("X-Robots-Tag") == "" || h.Get("Cache-Control") != "no-store" || !strings.Contains(h.Get("Vary"), "Accept") {
			t.Errorf("header: X-Robots-Tag %q, Cache-Control %q, Vary %q", h.Get("X-Robots-Tag"), h.Get("Cache-Control"), h.Get("Vary"))
		}
		if cd := h.Get("Content-Disposition"); cd != defaultDisposition {
			t.Errorf("Content-Disposition %q", cd)
		}
		wantContains(t, "download page", body,
			"report.bin", "1.0 MiB (1,048,576 bytes)", `<form id="dl" method="post">`,
			`<meta name="robots" content="noindex, nofollow">`,
			`curl -f -OJ &#34;`+link+`&#34;`, `wget --content-disposition &#34;`+link+`&#34;`)
		if strings.Contains(body, "<title>report.bin") {
			t.Error("the file name is in the title: it would end up in the previews")
		}
	}

	// A chat preview with Accept */* sees the page; HEAD is not served.
	req, _ := http.NewRequest(http.MethodGet, link, nil)
	req.Header.Set("User-Agent", "Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)")
	req.Header.Set("Accept", "*/*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("preview: status %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, err := http.Head(link); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("HEAD: %v, %v", resp, err)
	}

	// The button: POST from the browser, attachment response with the whole file.
	req, _ = http.NewRequest(http.MethodPost, link, strings.NewReader(""))
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	dl, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if dl.StatusCode != http.StatusOK || !bytes.Equal(got, data) {
		t.Fatalf("download from the button: status %d, %d bytes, expected %d", dl.StatusCode, len(got), len(data))
	}
	if cd := dl.Header.Get("Content-Disposition"); cd != `attachment; filename=report.bin` {
		t.Errorf("Content-Disposition of the download %q", cd)
	}
	u.finish(t)

	for _, m := range []string{http.MethodGet, http.MethodPost} {
		resp, body := browserDo(t, m, link)
		if resp.StatusCode != http.StatusGone {
			t.Errorf("%s after the download: status %d, expected 410", m, resp.StatusCode)
		}
		wantContains(t, m+" after the download", body, "Link already used or expired", "Ask the sender to send the file again.")
	}
}

// TestLandingClaimed: with the download in progress the page says it has already started (410),
// without interrupting it.
func TestLandingClaimed(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()
	pr, pw := io.Pipe()
	u := openUpload(t, ctx, srv.URL+"/slow.bin", pr)
	link := srv.URL + "/" + u.slug

	dl := get(t, ctx, link)
	resp, body := browserDo(t, http.MethodGet, link)
	if resp.StatusCode != http.StatusGone {
		t.Errorf("landing with download in progress: status %d, expected 410", resp.StatusCode)
	}
	wantContains(t, "landing with download in progress", body, "Download already started")
	resp, body = browserDo(t, http.MethodPost, link)
	if resp.StatusCode != http.StatusGone {
		t.Errorf("second POST: status %d, expected 410", resp.StatusCode)
	}
	wantContains(t, "second POST", body, "Download already started")

	go func() {
		pw.Write([]byte("abc"))
		pw.Close()
	}()
	got, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if string(got) != "abc" {
		t.Fatalf("download in progress: %q", got)
	}
	u.finish(t)
}

// TestLandingGoneReasons: a link closed without a receiver says why. A sender that cancels or drops
// must not look like a "link already used" (the receiver downloaded nothing), and expiry has its
// own message; for curl the body stays "gone".
func TestLandingGoneReasons(t *testing.T) {
	check := func(t *testing.T, link, heading string) {
		t.Helper()
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			resp, body := browserDo(t, m, link)
			if resp.StatusCode != http.StatusGone {
				t.Errorf("%s: status %d, expected 410", m, resp.StatusCode)
			}
			wantContains(t, m, body, heading)
			if strings.Contains(body, "Link already used") {
				t.Errorf("%s: the page says the link was used: %q", m, body)
			}
		}
		resp := get(t, context.Background(), link)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusGone || strings.TrimSpace(string(b)) != "gone" {
			t.Errorf("curl: status %d, body %q", resp.StatusCode, b)
		}
	}
	t.Run("sender cancelled", func(t *testing.T) {
		srv, reg := newWebServer(t, time.Hour)
		client := &http.Client{}
		res := reserveUpload(t, client, srv.URL, "a.bin", 10)
		if st := cancelUpload(t, client, srv.URL, res.Slug, res.Token); st != http.StatusNoContent {
			t.Fatalf("DELETE: status %d", st)
		}
		waitLen(t, reg, 0)
		check(t, srv.URL+"/"+res.Slug, "Sender stopped")
	})
	t.Run("sender gone while waiting", func(t *testing.T) {
		// newWebServer: the server sees the sender's dropped connection only with ConnContext.
		srv, reg := newWebServer(t, time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		pr, pw := io.Pipe()
		defer pw.Close()
		u := openUpload(t, ctx, srv.URL+"/a.bin", pr)
		link := srv.URL + "/" + u.slug
		cancel()
		u.resp.Body.Close()
		waitLen(t, reg, 0)
		check(t, link, "Sender stopped")
	})
	t.Run("expired", func(t *testing.T) {
		srv, reg := newTestServerWait(t, 200*time.Millisecond)
		pr, pw := io.Pipe()
		defer pw.Close()
		u := openUpload(t, context.Background(), srv.URL+"/a.bin", pr)
		defer u.resp.Body.Close()
		link := srv.URL + "/" + u.slug
		waitLen(t, reg, 0)
		check(t, link, "Link expired")
	})
}

// TestLandingDetails: name with markup (escaped), unknown length (pipe), public base in the commands.
func TestLandingDetails(t *testing.T) {
	// Short wait: the sender never sends the body and Close waits for the session to expire.
	reg := relay.NewRegistry(10, time.Second)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{PublicURL: "https://pub.example"}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	pr, pw := io.Pipe()
	defer pw.Close()
	u := openUpload(t, ctx, srv.URL+"/%3Cimg%20src%3Dx%20onerror%3Dalert(1)%3E.txt", pr)
	defer u.resp.Body.Close()

	resp, body := browserDo(t, http.MethodGet, srv.URL+"/"+u.slug)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if strings.Contains(body, "<img src=x") {
		t.Error("file name not escaped in the page")
	}
	wantContains(t, "page", body, "&lt;img src=x onerror=alert(1)&gt;.txt", "unknown (streamed by the sender)",
		`curl -f -OJ &#34;https://pub.example/`+u.slug+`&#34;`)
}

// TestBrowserErrorPages: rejections are HTML pages for browsers and text for other clients.
func TestBrowserErrorPages(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, p := range []string{"/AbC123xY9z", "/x.bin"} {
		resp, body := browserDo(t, http.MethodGet, srv.URL+p)
		if resp.StatusCode != http.StatusNotFound || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("GET %s: status %d, Content-Type %q", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		wantContains(t, "GET "+p, body, "Link not found", `<a href="/">`)
	}
	plain := get(t, context.Background(), srv.URL+"/AbC123xY9z")
	b, _ := io.ReadAll(plain.Body)
	plain.Body.Close()
	if plain.StatusCode != http.StatusNotFound || string(b) != "404 page not found\n" {
		t.Errorf("GET text: status %d, %q", plain.StatusCode, b)
	}
}

// TestMethodsAllow: the 405 lists the allowed methods (POST / reserves an upload from the browser,
// POST /<slug> downloads).
func TestMethodsAllow(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, c := range []struct{ method, path, allow string }{
		{http.MethodDelete, "/", "GET, POST, PUT"},
		{http.MethodPatch, "/", "GET, POST, PUT"},
		{http.MethodDelete, "/AbC123xY9z", "GET, POST, PUT"},
		{http.MethodHead, "/AbC123xY9z", "GET, POST, PUT"},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != c.allow {
			t.Errorf("%s %s: status %d, Allow %q; expected 405, %q", c.method, c.path, resp.StatusCode, resp.Header.Get("Allow"), c.allow)
		}
	}
}

// TestHomeHTTPS: with an https base the home declares transit encryption, never end-to-end.
func TestHomeHTTPS(t *testing.T) {
	reg := relay.NewRegistry(10, time.Hour)
	h := New(reg, relay.NewCopier(64<<10), Options{PublicURL: "https://pub.example/"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	wantContains(t, "home https", body, "Encrypted in transit", "Not end-to-end encrypted", "curl https://pub.example -T FILE")
	if strings.Contains(body, "Not encrypted.") {
		t.Error("https base: the page says the server is not encrypted")
	}
}

// TestUsageText: curl on "/" receives the send command with the URL before -T.
func TestUsageText(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := get(t, context.Background(), srv.URL+"/")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	wantContains(t, "instructions", string(b), "Send:    curl "+srv.URL+" -T FILE\n", "open the sender's link in a browser")
}

func TestPreviewAgents(t *testing.T) {
	for ua, want := range map[string]bool{
		"curl/8.18.0":          false,
		"curl/8.5.0":           false,
		"Wget/1.21.4":          false,
		"Wget":                 false,
		"aria2/1.37.0":         false,
		"Go-http-client/1.1":   false,
		"python-requests/2.32": false,
		"HTTPie/3.2.4":         false,
		"":                     false,
		"Mozilla/5.0 (Windows NT; Windows NT 10.0; en-US) WindowsPowerShell/5.1.22621.4391": false,
		"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)":                        true,
		"facebookexternalhit/1.1":       true,
		"WhatsApp/2.23.20.0":            true,
		"TelegramBot (like TwitterBot)": true,
		"Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)": true,
		"Mozilla/5.0 (Windows NT 6.1; WOW64) SkypeUriPreview Preview/0.5":   true,
		"Microsoft Office Word 2014":                                        true,
		"Mattermost-Bot/1.1":                                                true,
	} {
		r := httptest.NewRequest(http.MethodGet, "/AbC123xY9z", nil)
		r.Header.Set("User-Agent", ua)
		if got := isPreviewAgent(r); got != want {
			t.Errorf("isPreviewAgent(%q) = %v, expected %v", ua, got, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{
		-1:         "unknown (streamed by the sender)",
		0:          "0 bytes",
		1:          "1 byte",
		1023:       "1023 bytes",
		1024:       "1.0 KiB (1,024 bytes)",
		1152499340: "1.1 GiB (1,152,499,340 bytes)",
	} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, expected %q", n, got, want)
		}
	}
}
