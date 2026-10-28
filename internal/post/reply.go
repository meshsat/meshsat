package post

import (
	"regexp"
	"strings"
)

// The Hub answers a post with an MT in the "@" lane's reply form,
//
//	[from <address>] #<code> <text>      (a post device: the code in front)
//	[from <address>] <text>              (every other device)
//
// and the Bridge had no parser for it (MESHSAT-1692). This is the parser:
// strict on the head, so a sealed frame, a notice or anything else never
// reads as a reply.

var fromPattern = regexp.MustCompile(`^\[from ([^\]\s]{3,320})\] ?(.*)$`)

// Reply is a person's answer as the Hub delivered it.
type Reply struct {
	From string // the address the Hub named: +E164 or an email address
	Code string // the code the reply names, "" when it names none
	Text string // the words, the code token removed when it led the text
}

// ParseReply reads an MT text. ok is false for anything that is not in the
// reply form. The code is the one the Hub put in front ("#K7QW2X"), else the
// first code-shaped token anywhere in the text that is one of known (the
// person quoted it); with no known set, the first token in front only.
func ParseReply(text string, known func(code string) bool) (Reply, bool) {
	m := fromPattern.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return Reply{}, false
	}
	r := Reply{From: m[1], Text: strings.TrimSpace(m[2])}
	if r.Text == "" {
		return r, true
	}
	// The Hub's own form: the code first, with its "#".
	if strings.HasPrefix(r.Text, "#") {
		head := r.Text
		if i := strings.IndexAny(head, " \t"); i > 0 {
			head = head[:i]
		}
		if c := NormaliseCode(head); c != "" {
			r.Code = c
			r.Text = strings.TrimSpace(r.Text[len(head):])
			return r, true
		}
	}
	if known == nil {
		return r, true
	}
	// The person quoted the code somewhere in their words.
	for _, c := range CodesIn(r.Text) {
		if known(c) {
			r.Code = c
			return r, true
		}
	}
	return r, true
}

// NoticeFromHub reports whether an MT is the Hub's own notice that a text
// was not sent ("[MeshSat Hub] not sent to <address>: <reason>"), and gives
// the address and the reason.
func NoticeFromHub(text string) (address, reason string, ok bool) {
	const head = "[MeshSat Hub] not sent to "
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, head) {
		return "", "", false
	}
	rest := text[len(head):]
	i := strings.Index(rest, ": ")
	if i <= 0 {
		return "", "", false
	}
	return rest[:i], rest[i+2:], true
}
