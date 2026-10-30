package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bashtransfer/internal/relay"
)

func authRequest(t *testing.T, user, pass string, withHeader bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/x.bin", nil)
	if withHeader {
		r.SetBasicAuth(user, pass)
	}
	return r
}

func TestBasicAuthAcceptsCorrect(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	if !check(authRequest(t, "alice", "s3cret", true)) {
		t.Fatal("credenziali corrette rifiutate")
	}
}

func TestBasicAuthRejectsWrongUser(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	if check(authRequest(t, "bob", "s3cret", true)) {
		t.Fatal("utente sbagliato accettato")
	}
}

func TestBasicAuthRejectsWrongPassword(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	if check(authRequest(t, "alice", "wrong", true)) {
		t.Fatal("password sbagliata accettata")
	}
}

func TestBasicAuthRejectsMissingHeader(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	if check(authRequest(t, "", "", false)) {
		t.Fatal("richiesta senza header accettata")
	}
}

// TestBasicAuthConstantWork: ogni errore di un solo campo è rifiutato da solo,
// in entrambe le direzioni. Il fatto che entrambi i confronti siano sempre eseguiti
// è verificato in revisione del diff (vedi STATE §7).
func TestBasicAuthConstantWork(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	cases := []struct {
		name, user, pass string
	}{
		{"utente giusto, password sbagliata", "alice", "s3creT"},
		{"utente sbagliato, password giusta", "alicf", "s3cret"},
		{"entrambi sbagliati", "x", "y"},
	}
	for _, c := range cases {
		if check(authRequest(t, c.user, c.pass, true)) {
			t.Errorf("%s: accettato", c.name)
		}
	}
}

func TestUploadRequiresAuth(t *testing.T) {
	reg := relay.NewRegistry(100, time.Hour)
	srv := httptest.NewServer(New(reg, relay.NewCopier(64<<10), Options{
		Authorize: BasicAuth("alice", "s3cret"),
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Senza credenziali: 401 con WWW-Authenticate, nessuna sessione creata.
	noAuth, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/x.bin", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(noAuth)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("senza credenziali: stato %d, atteso 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic") {
		t.Fatalf("WWW-Authenticate %q, atteso Basic", got)
	}
	if reg.Len() != 0 {
		t.Fatalf("sessioni create senza autorizzazione: %d", reg.Len())
	}

	// Con credenziali: comandi restituiti.
	withAuth, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/x.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	withAuth.SetBasicAuth("alice", "s3cret")
	u := openUploadRequest(t, ctx, withAuth)
	if !strings.HasPrefix(u.wgetLine, `wget --content-disposition "`) {
		t.Fatalf("con credenziali: riga wget %q", u.wgetLine)
	}
}
