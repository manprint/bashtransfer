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
		{"quotes and backslash", `a"b\c.zip`, "abc.zip"},
		{"control", "\x01x", "x"},
		{"empty", "", "download"},
		{"dots", "..", "download"},
		{"single dot", ".", "download"},
		{"hidden file", ".bashrc", "bashrc"},
		{"leading dots and spaces", ". .profile", "profile"},
		{"only dots", "...", "download"},
		{"inner dots unchanged", "a..b.tar.gz", "a..b.tar.gz"},
		{"surrounding spaces", "  file.zip  ", "file.zip"},
		{"name with spaces", "strano nome.zip", "strano nome.zip"},
		{"UTF-8 unchanged", "città.zip", "città.zip"},
		{"command substitution", "x$(touch PWNED).zip", "x_(touch PWNED).zip"},
		{"backtick", "a`id`.zip", "a_id_.zip"},
		{"history expansion", "a!b.zip", "a_b.zip"},
		{"CSI C1", "a\u009b31mb.zip", "a31mb.zip"},
		{"override bidi", "doc\u202efdp.exe", "docfdp.exe"},
		{"line separator", "a\u2028b", "ab"},
		{"DEL and NUL", "a\x7fb\x00c", "abc"},
		{"dash (stdout for curl and wget)", "-", "download"},
		{"leading dash unchanged", "-x.zip", "-x.zip"},
	}
	for _, c := range cases {
		if got := SanitizeName(c.in); got != c.want {
			t.Errorf("%s: SanitizeName(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}

	long := strings.Repeat("a", 200)
	if got := SanitizeName(long); len([]rune(got)) != 128 {
		t.Errorf("200 characters: length %d, want 128", len([]rune(got)))
	}
	// Cutting at 128 runes leaves no trailing spaces.
	if got := SanitizeName(strings.Repeat("a", 127) + " b"); got != strings.Repeat("a", 127) {
		t.Errorf("space at the cut: %q", got)
	}
	longUTF := strings.Repeat("è", 200)
	if got := SanitizeName(longUTF); len([]rune(got)) != 128 {
		t.Errorf("200 UTF-8 runes: length %d, want 128", len([]rune(got)))
	}
}
