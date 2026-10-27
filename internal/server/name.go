package server

import (
	"strings"
)

const maxNameRunes = 128

// SanitizeName rende sicuro il nome file preso dal path (D5): niente directory,
// caratteri di controllo, virgolette o backslash; massimo 128 caratteri; "download" se vuoto.
func SanitizeName(raw string) string {
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		raw = raw[i+1:]
	}
	var b strings.Builder
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimSpace(b.String())
	if s == ".." {
		s = ""
	}
	if rs := []rune(s); len(rs) > maxNameRunes {
		s = string(rs[:maxNameRunes])
	}
	if s == "" {
		return "download"
	}
	return s
}
