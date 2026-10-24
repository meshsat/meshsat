// Package hopwire is the compact form of a Bridge's hop set (media hopping,
// slice (b)) that one Bridge tells another inside an OOB STATUS-NET frame:
// a request may carry the sender's form as its argument, and a reply appends
// the replier's form to its body. It is advisory, for the operator: a peer's
// report about its own links is never a sign against ours, since only
// contact-independent evidence may take a link out of use
// (MEDIA_HOP_DESIGN section 4.3). [MESHSAT-1650]
//
// The contract, version 1:
//
//	form     = "H1:" *link [ "+" ]
//	link     = kind instance state
//	kind     = one lowercase letter from Kinds
//	instance = 1*3DIGIT, the interface's number (mesh_0 is 0), no
//	           leading zero
//	state    = "A" admitted / "S" suspect / "X" excluded /
//	           "R" recovering / "T" retained by the floor / "D" down
//
// Links go in interface-id order, one per interface. A link whose kind has
// no letter is left out. The form is canonical: a reader refuses a second
// link for one interface and an instance with a leading zero. A form cut to fit its budget ends in "+": the links after the last
// one shown were dropped. Example: "H1:a0Ag0Ak0Rm0X+". A new kind or state
// is version 2, with its own prefix; a version 1 reader refuses what it
// does not know rather than guess.
package hopwire

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Prefix opens every version 1 form.
const Prefix = "H1:"

// Cut ends a form that was cut to fit its budget.
const Cut = "+"

// MinLen is the shortest form that says anything: the prefix and the cut mark.
const MinLen = len(Prefix) + len(Cut)

// State is a link's place in the hop set, one letter on the wire.
type State byte

// The states.
const (
	Admitted   State = 'A'
	Suspect    State = 'S'
	Excluded   State = 'X'
	Recovering State = 'R'
	Retained   State = 'T'
	Down       State = 'D'
)

var stateNames = map[State]string{
	Admitted: "admitted", Suspect: "suspect", Excluded: "excluded",
	Recovering: "recovering", Retained: "retained", Down: "down",
}

// String names the state as GET /api/hopset does.
func (s State) String() string {
	if n, ok := stateNames[s]; ok {
		return n
	}
	return "?"
}

// Kinds maps a channel type to its letter. Fixed for version 1.
var Kinds = map[string]byte{
	"mesh": 'm', "aprs": 'a', "tetra": 't', "lxmf": 'l', "cell": 'g', "zigbee": 'z', "hub": 'h',
	"cellular": 'c', "sms": 's', "iridium": 'i', "iridium_imt": 'k',
}

var kindByLetter = func() map[byte]string {
	m := map[byte]string{}
	for k, v := range Kinds {
		m[v] = k
	}
	return m
}()

// Link is one link of a form: its interface id and state.
type Link struct {
	Iface string `json:"interface_id"`
	State State  `json:"-"`
}

// Report is a parsed form.
type Report struct {
	Links []Link
	Cut   bool // the sender dropped links to fit its budget
}

// split returns an interface id's kind and instance ("iridium_imt_0" is
// iridium_imt and 0).
func split(iface string) (kind, inst string, ok bool) {
	i := strings.LastIndexByte(iface, '_')
	if i <= 0 || i == len(iface)-1 {
		return "", "", false
	}
	kind, inst = iface[:i], iface[i+1:]
	if len(inst) > 3 || (len(inst) > 1 && inst[0] == '0') {
		return "", "", false
	}
	for j := 0; j < len(inst); j++ {
		if inst[j] < '0' || inst[j] > '9' {
			return "", "", false
		}
	}
	return kind, inst, true
}

// Encode writes links as a form of at most budget bytes, dropping links
// from the end (and marking the cut) when the whole does not fit. A budget
// under MinLen gives "". Links of a kind outside Kinds, with an instance
// that is not 1 to 3 digits, or a state outside the six are left out.
func Encode(links []Link, budget int) string {
	sorted := append([]Link{}, links...)
	// Stable, so of two links given for one interface the first is kept.
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Iface < sorted[j].Iface })
	var toks []string
	seen := map[string]bool{}
	for _, l := range sorted {
		kind, inst, ok := split(l.Iface)
		letter, known := Kinds[kind]
		if !ok || !known || stateNames[l.State] == "" || seen[l.Iface] {
			continue
		}
		seen[l.Iface] = true
		toks = append(toks, string(letter)+inst+string(l.State))
	}
	full := Prefix + strings.Join(toks, "")
	if len(full) <= budget {
		return full
	}
	if budget < MinLen {
		return ""
	}
	for n := len(toks) - 1; n >= 0; n-- {
		if f := Prefix + strings.Join(toks[:n], "") + Cut; len(f) <= budget {
			return f
		}
	}
	return ""
}

// ErrForm is returned for anything that is not a version 1 form.
var ErrForm = errors.New("hopwire: not a version 1 hop form")

// Parse reads a form. Anything outside the grammar is refused whole.
func Parse(s string) (Report, error) {
	if !strings.HasPrefix(s, Prefix) {
		return Report{}, ErrForm
	}
	rest := s[len(Prefix):]
	var r Report
	if strings.HasSuffix(rest, Cut) {
		r.Cut = true
		rest = rest[:len(rest)-len(Cut)]
	}
	seen := map[string]bool{}
	for len(rest) > 0 {
		kind, ok := kindByLetter[rest[0]]
		if !ok {
			return Report{}, ErrForm
		}
		j := 1
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 1 || j > 4 || j >= len(rest) || (j > 2 && rest[1] == '0') {
			return Report{}, ErrForm
		}
		st := State(rest[j])
		if stateNames[st] == "" {
			return Report{}, ErrForm
		}
		n, _ := strconv.Atoi(rest[1:j])
		iface := kind + "_" + strconv.Itoa(n)
		if seen[iface] {
			return Report{}, ErrForm
		}
		seen[iface] = true
		r.Links = append(r.Links, Link{Iface: iface, State: st})
		rest = rest[j+1:]
	}
	return r, nil
}

// Find returns the form inside a STATUS-NET body: the last word that starts
// with the prefix. The replier appends its form last, so a word earlier in
// the body that only looks like one (an SSID, say) is not taken for it.
func Find(body string) (string, bool) {
	words := strings.Split(body, " ")
	for i := len(words) - 1; i >= 0; i-- {
		if strings.HasPrefix(words[i], Prefix) {
			return words[i], true
		}
	}
	return "", false
}
