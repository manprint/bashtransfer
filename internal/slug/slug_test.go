package slug

import (
	"strings"
	"testing"
)

func TestGenerateLength(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 10 {
		t.Fatalf("lunghezza %d, attesa 10: %q", len(s), s)
	}
	if !Valid(s) || Valid("abc") || Valid(s+"!") {
		t.Fatalf("Valid non riconosce %q", s)
	}
}

func TestGenerateAlphabet(t *testing.T) {
	for i := 0; i < 1000; i++ {
		s, err := New()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range s {
			if !strings.ContainsRune(alphabet, c) {
				t.Fatalf("carattere fuori alfabeto %q in %q", c, s)
			}
		}
	}
}

func TestGenerateUnique(t *testing.T) {
	seen := make(map[string]struct{}, 20000)
	for i := 0; i < 20000; i++ {
		s, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[s]; dup {
			t.Fatalf("duplicato %q dopo %d campioni", s, i)
		}
		seen[s] = struct{}{}
	}
}
