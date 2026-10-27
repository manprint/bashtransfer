// Package slug genera gli identificativi di sessione (D3).
package slug

import (
	"crypto/rand"
	"io"
	"strings"
)

// Length è il numero di caratteri di uno slug.
const Length = 10

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// limit è 62*4: i byte sotto questa soglia si distribuiscono uniformemente
// sull'alfabeto con `b % 62`, senza bias modulo.
const limit = 248

// New genera uno slug con crypto/rand.
func New() (string, error) {
	return generate(rand.Reader)
}

// Valid riporta se s ha lunghezza e alfabeto di uno slug generato da New.
func Valid(s string) bool {
	if len(s) != Length {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

func generate(r io.Reader) (string, error) {
	out := make([]byte, 0, Length)
	buf := make([]byte, 2*Length)
	for len(out) < Length {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < Length {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}
