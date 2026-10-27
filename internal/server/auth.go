package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// BasicAuth restituisce il controllo di autorizzazione per il PUT (D9).
// I digest SHA-256 di user e password sono calcolati una volta; per ogni richiesta
// entrambi i confronti sono eseguiti, senza ritorno anticipato.
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
