package server

import (
	"strings"
	"unicode"
)

const maxNameRunes = 128

// SanitizeName rende sicuro il nome file preso dal path (D5): niente directory,
// caratteri di controllo, virgolette o backslash, punti iniziali; massimo 128 caratteri; "download" se vuoto.
// Il nome finisce tra virgolette doppie in una riga di shell che l'utente incolla: `$`, backtick
// e `!` (espansioni di bash anche tra virgolette) diventano `_`. Sono scartati anche i controlli
// C1 (CSI per alcuni terminali), i separatori di riga Unicode e i controlli bidirezionali, che
// mascherano l'estensione nel testo mostrato.
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
	// Punti iniziali tolti: "." e ".." sono directory (`curl -o "."` fallisce) e un nome come
	// ".bashrc" sovrascriverebbe un file nascosto del ricevente che esegue il comando nella home.
	s := strings.TrimLeftFunc(b.String(), func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
	s = strings.TrimSpace(s)
	if rs := []rune(s); len(rs) > maxNameRunes {
		// Il taglio può lasciare uno spazio in coda: TrimSpace di nuovo.
		s = strings.TrimSpace(string(rs[:maxNameRunes]))
	}
	// "-" è lo stdout per curl -o e wget -O: il ricevente non avrebbe un file.
	if s == "" || s == "-" {
		return "download"
	}
	return s
}

// isInvisibleFormat riconosce i caratteri Unicode che cambiano la visualizzazione del testo
// senza comparire: separatori di riga e paragrafo, marcatori e override bidirezionali.
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
