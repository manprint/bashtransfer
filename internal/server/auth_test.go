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
		t.Fatal("correct credentials rejected")
	}
}

func TestBasicAuthRejectsWrongUser(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	if check(authRequest(t, "bob", "s3cret", true)) {
		t.Fatal("wrong user accepted")
	}
}

func TestBasicAuthRejectsWrongPassword(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	if check(authRequest(t, "alice", "wrong", true)) {
		t.Fatal("wrong password accepted")
	}
}

func TestBasicAuthRejectsMissingHeader(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	if check(authRequest(t, "", "", false)) {
		t.Fatal("request without header accepted")
	}
}

// TestBasicAuthConstantWork: every error in a single field is rejected on its own,
// in both directions. That both comparisons are always executed
// is verified in diff review (see STATE §7).
func TestBasicAuthConstantWork(t *testing.T) {
	check := BasicAuth("alice", "s3cret")
	cases := []struct {
		name, user, pass string
	}{
		{"right user, wrong password", "alice", "s3creT"},
		{"wrong user, right password", "alicf", "s3cret"},
		{"both wrong", "x", "y"},
	}
	for _, c := range cases {
		if check(authRequest(t, c.user, c.pass, true)) {
			t.Errorf("%s: accepted", c.name)
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

	// Without credentials: 401 with WWW-Authenticate, no session created.
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
		t.Fatalf("without credentials: status %d, want 401", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic") {
		t.Fatalf("WWW-Authenticate %q, want Basic", got)
	}
	if reg.Len() != 0 {
		t.Fatalf("sessions created without authorization: %d", reg.Len())
	}

	// With credentials: commands returned.
	withAuth, err := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/x.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	withAuth.SetBasicAuth("alice", "s3cret")
	u := openUploadRequest(t, ctx, withAuth)
	if !strings.HasPrefix(u.wgetLine, `wget --content-disposition "`) {
		t.Fatalf("with credentials: wget line %q", u.wgetLine)
	}
}
