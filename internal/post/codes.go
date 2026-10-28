// Package post is the Community Post (MESHSAT-1733): a kit's no-app front
// door. Any phone joins the kit's open WiFi, gets a captive-portal page,
// writes a short message that leaves over satellite through the Hub's "@"
// lane to a phone number or an email address, and collects the reply with a
// pickup code. The portal is plain HTML on its own listener; the pickup code
// is the only state a phone has to keep.
package post

import (
	"crypto/rand"
	"regexp"
	"strings"
)

// CodeAlphabet is the 30 characters a pickup code is made of: no I, L, O, U,
// 0 or 1, nothing a person misreads on a screenshot or a slip. The Hub's
// conversation tags use the same letters.
const CodeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// CodeLength is the length of a pickup code: six characters, 30^6 of them.
const CodeLength = 6

// codePattern finds a code in a reply, with or without the "#" the Hub puts
// in front of it. The character class is CodeAlphabet.
var codePattern = regexp.MustCompile(`#?\b([A-HJ-NP-TV-Z2-9]{6})\b`)

// NewCode makes a fresh random pickup code.
func NewCode() string {
	var b [CodeLength]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("post: no randomness: " + err.Error())
	}
	out := make([]byte, CodeLength)
	for i, v := range b {
		out[i] = CodeAlphabet[int(v)%len(CodeAlphabet)]
	}
	return string(out)
}

// NormaliseCode turns what a person typed into a code: upper case, spaces,
// dashes and a leading "#" dropped, the common misreadings mapped back (0
// read for O becomes nothing useful, so 0 and 1 are refused, but a lower
// case letter is accepted). An empty answer means it is not a code.
func NormaliseCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "-", "", "#", "", " ", "").Replace(s)
	if !ValidCode(s) {
		return ""
	}
	return s
}

// ValidCode reports whether s is exactly a pickup code.
func ValidCode(s string) bool {
	if len(s) != CodeLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(CodeAlphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// CodesIn lists every code-shaped token in text, in order, upper-cased,
// without duplicates. A six-letter word of the alphabet is a candidate too:
// the caller checks each against the codes it knows.
func CodesIn(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range codePattern.FindAllStringSubmatch(strings.ToUpper(text), -1) {
		c := m[1]
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}
