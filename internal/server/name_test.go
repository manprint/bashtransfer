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
		{"punto singolo", ".", "download"},
		{"file nascosto", ".bashrc", "bashrc"},
		{"punti e spazi iniziali", ". .profile", "profile"},
		{"solo punti", "...", "download"},
		{"punti interni invariati", "a..b.tar.gz", "a..b.tar.gz"},
		{"spazi attorno", "  file.zip  ", "file.zip"},
		{"nome con spazi", "strano nome.zip", "strano nome.zip"},
		{"UTF-8 invariato", "città.zip", "città.zip"},
		{"sostituzione comando", "x$(touch PWNED).zip", "x_(touch PWNED).zip"},
		{"backtick", "a`id`.zip", "a_id_.zip"},
		{"history expansion", "a!b.zip", "a_b.zip"},
		{"CSI C1", "a\u009b31mb.zip", "a31mb.zip"},
		{"override bidi", "doc\u202efdp.exe", "docfdp.exe"},
		{"separatore di riga", "a\u2028b", "ab"},
		{"DEL e NUL", "a\x7fb\x00c", "abc"},
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
	// Il taglio a 128 rune non lascia spazi in coda.
	if got := SanitizeName(strings.Repeat("a", 127) + " b"); got != strings.Repeat("a", 127) {
		t.Errorf("spazio al taglio: %q", got)
	}
	longUTF := strings.Repeat("è", 200)
	if got := SanitizeName(longUTF); len([]rune(got)) != 128 {
		t.Errorf("200 rune UTF-8: lunghezza %d, attesa 128", len([]rune(got)))
	}
}
