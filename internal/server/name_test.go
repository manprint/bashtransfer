package server

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"path traversal", "../../etc/passwd", "passwd"},
		{"virgolette e backslash", `a"b\c.zip`, "abc.zip"},
		{"controllo", "\x01x", "x"},
		{"vuoto", "", "download"},
		{"punti", "..", "download"},
		{"spazi attorno", "  file.zip  ", "file.zip"},
		{"nome con spazi", "strano nome.zip", "strano nome.zip"},
		{"UTF-8 invariato", "città.zip", "città.zip"},
	}
	for _, c := range cases {
		if got := SanitizeName(c.in); got != c.want {
			t.Errorf("%s: SanitizeName(%q) = %q, atteso %q", c.name, c.in, got, c.want)
		}
	}

	long := strings.Repeat("a", 200)
	if got := SanitizeName(long); len([]rune(got)) != 128 {
		t.Errorf("200 caratteri: lunghezza %d, attesa 128", len([]rune(got)))
	}
	longUTF := strings.Repeat("è", 200)
	if got := SanitizeName(longUTF); len([]rune(got)) != 128 {
		t.Errorf("200 rune UTF-8: lunghezza %d, attesa 128", len([]rune(got)))
	}
}
