package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// BasicAuth returns the authorization check for the PUT (D9).
// The SHA-256 digests of user and password are computed once; for each request
// both comparisons are performed, without early return.
func BasicAuth(user, password string) func(*http.Request) bool {
	wantUser := sha256.Sum256([]byte(user))
	wantPass := sha256.Sum256([]byte(password))
	return func(r *http.Request) bool {
		u, p, ok := r.BasicAuth()
		gotUser := sha256.Sum256([]byte(u))
		gotPass := sha256.Sum256([]byte(p))
		userOK := subtle.ConstantTimeCompare(gotUser[:], wantUser[:]) == 1
		passOK := subtle.ConstantTimeCompare(gotPass[:], wantPass[:]) == 1
		return ok && userOK && passOK
	}
}
