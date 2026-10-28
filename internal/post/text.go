package post

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a person may write, and the one text it becomes on the wire.

// Limits a post is held to. MaxTextChars is the plan's 150; the byte budget
// is what an SBD MO leaves after the address, the name and the code (a
// RockBLOCK MO is 340 bytes, and the Hub reads the whole text).
const (
	DefaultMaxTextChars = 150
	DefaultMaxBytes     = 240
	MaxNameChars        = 40
	MaxBoardChars       = 60
)

// Refusals a person is shown, in fixed words the templates translate.
var (
	ErrRecipient = errors.New("recipient")
	ErrEmpty     = errors.New("empty")
	ErrTooLong   = errors.New("too_long")
	ErrLink      = errors.New("link")
	ErrControl   = errors.New("control")
	ErrName      = errors.New("name")
	ErrQuota     = errors.New("quota")
)

var (
	emailPattern = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)
	// linkPattern is what the Hub, the person's phone or a mail client would
	// turn into something to tap: a scheme, "www.", or a word with a common
	// top-level domain. A post carries words; a link in one is refused, the
	// plan's rule (phishing through a stranger's satellite account).
	linkPattern = regexp.MustCompile(`(?i)(://|\bwww\.|\b[a-z0-9-]+\.(com|net|org|info|io|nl|gr|eu|de|be|uk|fr|it|es|co|me|xyz|top|link|site|online|app|gov|edu|ru|cn|tk|ml|ga)\b)`)
)

// Recipient is a parsed "@" address: an E.164 number or an email address.
type Recipient struct {
	Kind  string // "sms" or "email"
	Value string // +31612345678 or a lower-cased email address
}

// ParseRecipient reads what a person typed as the recipient. A number may
// carry spaces, dots, dashes or brackets and a leading 00 (international
// dialling) in place of "+"; the result is E.164, 7 to 15 digits. An email
// address is one bare ASCII address.
func ParseRecipient(s string) (Recipient, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Recipient{}, ErrRecipient
	}
	if strings.Contains(s, "@") {
		if len(s) > 254 || !emailPattern.MatchString(s) {
			return Recipient{}, ErrRecipient
		}
		return Recipient{Kind: "email", Value: strings.ToLower(s)}, nil
	}
	digits := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return r
		case r == '+':
			return r
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')' || r == ' ':
			return -1
		}
		return 'x'
	}, s)
	if strings.ContainsRune(digits, 'x') {
		return Recipient{}, ErrRecipient
	}
	switch {
	case strings.HasPrefix(digits, "+"):
		digits = digits[1:]
	case strings.HasPrefix(digits, "00"):
		digits = digits[2:]
	default:
		// A national number has no country code; the Hub needs one.
		return Recipient{}, ErrRecipient
	}
	if strings.ContainsRune(digits, '+') || len(digits) < 7 || len(digits) > 15 || digits[0] == '0' {
		return Recipient{}, ErrRecipient
	}
	return Recipient{Kind: "sms", Value: "+" + digits}, nil
}

// CleanText checks and tidies a post's words: line breaks become spaces, runs
// of whitespace one space, control characters are refused, so is a link, and
// the result must fit maxChars characters and maxBytes bytes.
func CleanText(s string, maxChars, maxBytes int) (string, error) {
	for _, r := range s {
		if r != '\n' && r != '\r' && r != '\t' && unicode.IsControl(r) {
			return "", ErrControl
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", ErrEmpty
	}
	if maxChars > 0 && utf8.RuneCountInString(s) > maxChars {
		return "", ErrTooLong
	}
	if maxBytes > 0 && len(s) > maxBytes {
		return "", ErrTooLong
	}
	if linkPattern.MatchString(s) {
		return "", ErrLink
	}
	return s, nil
}

// CleanName tidies an optional name: one line, no control characters, at
// most MaxNameChars, and never a colon (the colon is the wire's separator)
// or a "[" (the code's bracket).
func CleanName(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", nil
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == ':' || r == '[' || r == ']' || r == '@' {
			return "", ErrName
		}
	}
	if utf8.RuneCountInString(s) > MaxNameChars {
		return "", ErrName
	}
	return s, nil
}

// CleanBoardField tidies a board field (name, status, place): one line, no
// control characters, at most MaxBoardChars.
func CleanBoardField(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", ErrControl
		}
	}
	if utf8.RuneCountInString(s) > MaxBoardChars {
		return "", ErrTooLong
	}
	if linkPattern.MatchString(s) {
		return "", ErrLink
	}
	return s, nil
}

// NameKey is how a board name is searched: case folded, accents kept as
// typed, whitespace collapsed.
func NameKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Compose is the one text the kit sends to the Hub for a post, the "@" lane's
// form: the address, the name (when given) with a colon, the words, and the
// pickup code in brackets at the end so the Hub keys the conversation by it
// and the recipient can quote it. An emergency carries "SOS" in front of the
// name so the recipient reads it as one.
//
//	@+31612345678 Maria: water at the north gate [#K7QW2X]
//	@+31612345678 SOS Maria: trapped, second floor [#K7QW2X]
func Compose(r Recipient, name, text, code string, emergency bool) string {
	var b strings.Builder
	b.WriteString("@")
	b.WriteString(r.Value)
	b.WriteString(" ")
	if emergency {
		b.WriteString("SOS ")
	}
	if name != "" {
		b.WriteString(name)
		b.WriteString(": ")
	}
	b.WriteString(text)
	b.WriteString(" [#")
	b.WriteString(code)
	b.WriteString("]")
	return b.String()
}
