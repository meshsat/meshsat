package gateway

// SanitizeSMSText replaces characters that are unsafe for GSM 7-bit text mode
// SMS on modems like the Huawei E220. These modems fail with CMS ERROR 305
// when the SMS body contains GSM 7-bit extension table characters.
//
// The GSM 03.38 basic set (safe) includes standard ASCII letters, digits,
// and common punctuation. The extension table characters (unsafe on some
// modems) are: [ ] { } | \ ^ ~ €
//
// Non-GSM characters (emoji, accented letters outside GSM set, CJK, etc.)
// are replaced with '?' to avoid silent encoding failures.
//
// This function is designed to be extended: add new entries to
// gsmUnsafeReplacements for any future modem-specific character issues.
func SanitizeSMSText(text string) string {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		if repl, ok := gsmUnsafeReplacements[r]; ok {
			for _, b := range repl {
				out = append(out, rune(b))
			}
		} else if isGSM7BitBasic(r) {
			out = append(out, r)
		} else {
			out = append(out, '?')
		}
	}
	return string(out)
}

// gsmUnsafeReplacements maps characters that cause CMS ERROR 305 on some
// modems to safe ASCII alternatives. Extend this map for new problem chars.
var gsmUnsafeReplacements = map[rune][]byte{
	'[':  {'('},
	']':  {')'},
	'{':  {'('},
	'}':  {')'},
	'|':  {'/'},
	'\\': {'/'},
	'^':  {'\''},
	'~':  {'-'},
	'€':  {'E', 'U', 'R'},
}

// isGSM7BitBasic returns true if the rune is in the GSM 03.38 basic character
// set (NOT the extension table). These are safe on all modems in text mode.
func isGSM7BitBasic(r rune) bool {
	if r >= 'A' && r <= 'Z' {
		return true
	}
	if r >= 'a' && r <= 'z' {
		return true
	}
	if r >= '0' && r <= '9' {
		return true
	}
	// GSM basic set punctuation and symbols
	_, ok := gsm7BitBasicSet[r]
	return ok
}

// gsm7BitBasicSet contains all non-alphanumeric characters in the GSM 03.38
// basic character set. Extension table chars are NOT included.
var gsm7BitBasicSet = map[rune]bool{
	'@': true, '£': true, '$': true, '¥': true, 'è': true, 'é': true,
	'ù': true, 'ì': true, 'ò': true, 'Ç': true, '\n': true, 'Ø': true,
	'ø': true, '\r': true, 'Å': true, 'å': true, 'Δ': true, '_': true,
	'Φ': true, 'Γ': true, 'Λ': true, 'Ω': true, 'Π': true, 'Ψ': true,
	'Σ': true, 'Θ': true, 'Ξ': true, 'Æ': true, 'æ': true, 'ß': true,
	'É': true, ' ': true, '!': true, '"': true, '#': true, '¤': true,
	'%': true, '&': true, '\'': true, '(': true, ')': true, '*': true,
	'+': true, ',': true, '-': true, '.': true, '/': true, ':': true,
	';': true, '<': true, '=': true, '>': true, '?': true, '¡': true,
	'Ä': true, 'Ö': true, 'Ñ': true, 'Ü': true, '§': true, '¿': true,
	'ä': true, 'ö': true, 'ñ': true, 'ü': true, 'à': true,
	'Ã': true, 'ã': true, 'Õ': true, 'õ': true,
}
