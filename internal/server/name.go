package server

import (
	"strings"
	"unicode"
)

const maxNameRunes = 128

// SanitizeName makes the file name taken from the path safe (D5): no directories,
// control characters, quotes or backslashes, no leading dots; at most 128 characters; "download" if empty.
// The name ends up in double quotes in a shell line the user pastes: `$`, backtick
// and `!` (bash expansions even inside quotes) become `_`. C1 controls (CSI for some terminals),
// Unicode line separators and bidirectional controls, which hide the extension in the displayed
// text, are also discarded.
func SanitizeName(raw string) string {
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		raw = raw[i+1:]
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r == '"' || r == '\\' || unicode.IsControl(r) || isInvisibleFormat(r):
			continue
		case r == '$' || r == '`' || r == '!':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	// Leading dots removed: "." and ".." are directories (`curl -o "."` fails) and a name like
	// ".bashrc" would overwrite a hidden file of the receiver running the command in the home.
	s := strings.TrimLeftFunc(b.String(), func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
	s = strings.TrimSpace(s)
	if rs := []rune(s); len(rs) > maxNameRunes {
		// The cut may leave a trailing space: TrimSpace again.
		s = strings.TrimSpace(string(rs[:maxNameRunes]))
	}
	// "-" is stdout for curl -o and wget -O: the receiver would have no file.
	if s == "" || s == "-" {
		return "download"
	}
	return s
}

// isInvisibleFormat recognizes Unicode characters that change how text is displayed
// without appearing: line and paragraph separators, bidirectional marks and overrides.
func isInvisibleFormat(r rune) bool {
	switch {
	case r == 0x2028 || r == 0x2029:
		return true
	case r == 0x061c || r == 0x200e || r == 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
