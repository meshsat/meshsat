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
	// sentenceGlue is a full stop typed with no space before the next
	// sentence's capitalised word ("veilig.De kinderen", "gut.Es geht"), a
	// common slip on a phone keyboard that read as a .de or .es domain. The
	// space goes in before the link check; with it in, no phone or mail
	// client turns the words into a link either. A lower-case "veilig.de"
	// is a domain as far as any phone is concerned and stays refused.
	sentenceGlue = regexp.MustCompile(`([\p{L}\p{N}])\.(\p{Lu}\p{Ll})`)
)

// plainPunct turns the punctuation a phone keyboard puts in by itself (curly
// quotes and apostrophes, dashes, the ellipsis, a no-break space) into the
// plain characters a person meant. Each of these is three bytes of a post's
// byte budget against one, and an SMS carrying one of them is sent as UCS-2,
// at fewer characters per part. Letters, accents, «» and emoji are left alone.
var plainPunct = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'",
	"\u201C", `"`, "\u201D", `"`, "\u201E", `"`, "\u201F", `"`,
	"\u2013", "-", "\u2014", "-", "\u2212", "-",
	"\u2026", "...",
	"\u00A0", " ", "\u202F", " ",
)

// tidy is a text as it goes on the wire: one line, single spaces, plain
// punctuation, and the space after a full stop put back where a sentence was
// glued to the last.
func tidy(s string) string {
	s = strings.Join(strings.Fields(plainPunct.Replace(s)), " ")
	// A glue right after another ("a.Bc.De") overlaps the first match;
	// the second pass takes it. Two passes always suffice.
	for i := 0; i < 2; i++ {
		s = sentenceGlue.ReplaceAllString(s, "$1. $2")
	}
	return s
}

// LetterBytes is what one letter of lang's alphabet costs in UTF-8: two for
// Greek, one for the Latin alphabet (an accent or an umlaut is the odd
// extra byte, not the rule).
func LetterBytes(lang string) int {
	if lang == "el" {
		return 2
	}
	return 1
}

// MaxCharsFor is the character limit the portal shows in lang: the
// character ceiling, or fewer where that many of the language's letters
// would not fit the byte budget (240 bytes hold 120 Greek letters, not
// 150). A message of that many letters is never refused as too long.
func MaxCharsFor(lang string, maxChars, maxBytes int) int {
	n := maxChars
	if maxBytes > 0 {
		if b := maxBytes / LetterBytes(lang); n <= 0 || b < n {
			n = b
		}
	}
	return n
}

// CharsLeft is the room a typed text has, measured on the text as it would
// be sent (tidy) against the same two limits CleanText enforces. Positive
// or zero: how many more characters fit, counting each as wide as the
// letters already typed take on average (at least as wide as lang's
// letters). Negative: how many characters must come off the end before
// CleanText takes it.
func CharsLeft(text, lang string, maxChars, maxBytes int) int {
	if maxChars <= 0 && maxBytes <= 0 {
		return 1 << 30
	}
	s := tidy(text)
	r := []rune(s)
	over, n, b := 0, len(r), len(s)
	for (maxChars > 0 && n > maxChars) || (maxBytes > 0 && b > maxBytes) {
		n--
		b -= utf8.RuneLen(r[n])
		over++
	}
	if over > 0 {
		return -over
	}
	left := 1 << 30
	if maxChars > 0 {
		left = maxChars - len(r)
	}
	if maxBytes > 0 {
		unit := LetterBytes(lang)
		if len(r) > 0 {
			if avg := (len(s) + len(r) - 1) / len(r); avg > unit {
				unit = avg
			}
		}
		if byBytes := (maxBytes - len(s)) / unit; byBytes < left {
			left = byBytes
		}
	}
	return left
}

// utf16Len is a text's length as a browser's maxlength counts it.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

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
// of whitespace one space, a sentence glued to the last gets its space back,
// control characters are refused, so is a link, and the result must fit
// maxChars characters and maxBytes bytes.
func CleanText(s string, maxChars, maxBytes int) (string, error) {
	for _, r := range s {
		if r != '\n' && r != '\r' && r != '\t' && unicode.IsControl(r) {
			return "", ErrControl
		}
	}
	s = tidy(s)
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
	s = tidy(s)
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
