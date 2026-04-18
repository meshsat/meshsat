package directory

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// vCard 4.0 import / export (RFC 6350) with X-MESHSAT-* extensions
// for the bearer kinds the standard does not cover (MESHTASTIC,
// APRS, IRIDIUM_SBD, IRIDIUM_IMT, CELLULAR, TAK, RETICULUM, ZIGBEE,
// BLE, WEBHOOK, MQTT) plus TEAM / ROLE / SIDC / TRUST-LEVEL. This
// package supports the common subset: no line folding in excess of
// 75 octets, no QUOTED-PRINTABLE, no multi-value parameter lists.
// Suitable for operator import/export via the REST endpoint and
// for QR-bundle fallback. [MESHSAT-541]

// vCard → MeshSat Kind mapping for the X-MESHSAT-* extension family.
// Standard TEL becomes KindSMS (with preference for TYPE=cell/text);
// EMAIL becomes KindEmail. The rest of the bearer kinds are covered
// by explicit X-MESHSAT-<UPPER> extensions below.
var vcardXMeshsatKinds = map[string]Kind{
	"X-MESHSAT-MESHTASTIC":  KindMeshtastic,
	"X-MESHSAT-APRS":        KindAPRS,
	"X-MESHSAT-IRIDIUM-SBD": KindIridiumSBD,
	"X-MESHSAT-IRIDIUM-IMT": KindIridiumIMT,
	"X-MESHSAT-CELLULAR":    KindCellular,
	"X-MESHSAT-TAK":         KindTAK,
	"X-MESHSAT-RETICULUM":   KindReticulum,
	"X-MESHSAT-ZIGBEE":      KindZigBee,
	"X-MESHSAT-BLE":         KindBLE,
	"X-MESHSAT-WEBHOOK":     KindWebhook,
	"X-MESHSAT-MQTT":        KindMQTT,
}

// ParseVCards reads one or more vCard 4.0 BEGIN:VCARD .. END:VCARD
// blocks from r and returns the corresponding Contact records.
// Unrecognised properties are silently skipped. Returns the first
// syntactic error encountered.
func ParseVCards(r io.Reader) ([]Contact, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		contacts []Contact
		current  *Contact
		inCard   bool
	)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case strings.EqualFold(trimmed, "BEGIN:VCARD"):
			if inCard {
				return nil, fmt.Errorf("line %d: nested BEGIN:VCARD", lineNo)
			}
			inCard = true
			current = &Contact{Origin: OriginImported}
		case strings.EqualFold(trimmed, "END:VCARD"):
			if !inCard {
				return nil, fmt.Errorf("line %d: END:VCARD without BEGIN", lineNo)
			}
			// Populate DisplayName from N if FN was absent.
			if current.DisplayName == "" && (current.GivenName != "" || current.FamilyName != "") {
				current.DisplayName = strings.TrimSpace(current.GivenName + " " + current.FamilyName)
			}
			if current.DisplayName == "" {
				return nil, fmt.Errorf("line %d: vCard has no FN or N", lineNo)
			}
			contacts = append(contacts, *current)
			current = nil
			inCard = false
		default:
			if !inCard {
				// Ignore content outside of VCARD blocks (comments,
				// shell output, etc.) so operators can paste output
				// from tools that wrap the vCard in prologue/epilogue.
				continue
			}
			if err := applyVCardLine(current, line); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("vcard scan: %w", err)
	}
	if inCard {
		return nil, fmt.Errorf("unterminated VCARD block")
	}
	return contacts, nil
}

// applyVCardLine parses one content line and mutates the contact.
// Property format: NAME[;param=value[;...]]:VALUE
func applyVCardLine(c *Contact, line string) error {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return nil // tolerate pre-property comments
	}
	head := line[:colon]
	value := unescapeVCardValue(line[colon+1:])

	// Split head into name + parameters.
	nameParts := strings.Split(head, ";")
	name := strings.ToUpper(strings.TrimSpace(nameParts[0]))
	params := map[string]string{}
	for _, p := range nameParts[1:] {
		if eq := strings.IndexByte(p, '='); eq > 0 {
			params[strings.ToUpper(p[:eq])] = strings.TrimSpace(p[eq+1:])
		}
	}

	switch name {
	case "VERSION":
		// informational; we accept 3.0 and 4.0 silently
	case "FN":
		c.DisplayName = value
	case "N":
		// N:Family;Given;Additional;Prefix;Suffix
		nfields := strings.SplitN(value, ";", 5)
		if len(nfields) > 0 {
			c.FamilyName = nfields[0]
		}
		if len(nfields) > 1 {
			c.GivenName = nfields[1]
		}
	case "ORG":
		// ORG:Company;Department
		c.Org = strings.SplitN(value, ";", 2)[0]
	case "TITLE":
		if c.Role == "" {
			c.Role = value
		}
	case "NOTE":
		c.Notes = value
	case "UID":
		if c.ID == "" {
			c.ID = value
		}
	case "TEL":
		// TEL — best-effort map to SMS when TYPE suggests mobile /
		// SMS-capable; otherwise fall through.
		kind := telKindFromTypeParam(params["TYPE"])
		appendAddress(c, kind, value)
	case "EMAIL":
		appendAddress(c, KindEmail, value)
	case "X-MESHSAT-TEAM":
		c.Team = value
	case "X-MESHSAT-ROLE":
		c.Role = value
	case "X-MESHSAT-SIDC":
		c.SIDC = value
	case "X-MESHSAT-TRUST-LEVEL":
		if n, err := strconv.Atoi(value); err == nil && n >= 0 && n <= 3 {
			c.TrustLevel = TrustLevel(n)
		}
	default:
		if kind, ok := vcardXMeshsatKinds[name]; ok {
			appendAddress(c, kind, value)
		}
		// other unknown keys silently dropped
	}
	return nil
}

func telKindFromTypeParam(t string) Kind {
	t = strings.ToLower(t)
	// Anything that looks like "cell" or "text" is treated as SMS;
	// standard landline voice numbers also go through as SMS because
	// the bridge has no "voice" bearer concept.
	if t == "" || strings.Contains(t, "cell") || strings.Contains(t, "text") || strings.Contains(t, "mobile") || strings.Contains(t, "voice") || strings.Contains(t, "home") || strings.Contains(t, "work") {
		return KindSMS
	}
	return KindSMS
}

func appendAddress(c *Contact, kind Kind, value string) {
	if !kind.Valid() || value == "" {
		return
	}
	c.Addresses = append(c.Addresses, Address{
		Kind:        kind,
		Value:       value,
		PrimaryRank: primaryRankFor(c, kind),
	})
}

// primaryRankFor returns 0 for the first address of a given kind on
// the contact; 1 for every subsequent address of that kind. Mirrors
// the directory_addresses.primary_rank semantics.
func primaryRankFor(c *Contact, kind Kind) int {
	for _, a := range c.Addresses {
		if a.Kind == kind {
			return 1
		}
	}
	return 0
}

// WriteVCards emits the contacts as vCard 4.0 BEGIN/END blocks
// separated by CRLF, suitable for an operator .vcf download.
func WriteVCards(w io.Writer, contacts []Contact) error {
	bw := bufio.NewWriter(w)
	for i := range contacts {
		if err := writeOneVCard(bw, &contacts[i]); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func writeOneVCard(w *bufio.Writer, c *Contact) error {
	put := func(name, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(w, "%s:%s\r\n", name, escapeVCardValue(value))
	}
	fmt.Fprintln(w, "BEGIN:VCARD\r")
	fmt.Fprintln(w, "VERSION:4.0\r")
	put("FN", c.DisplayName)
	if c.FamilyName != "" || c.GivenName != "" {
		fmt.Fprintf(w, "N:%s;%s;;;\r\n",
			escapeVCardValue(c.FamilyName), escapeVCardValue(c.GivenName))
	}
	put("UID", c.ID)
	put("ORG", c.Org)
	put("TITLE", c.Role)
	put("NOTE", c.Notes)
	put("X-MESHSAT-TEAM", c.Team)
	put("X-MESHSAT-ROLE", c.Role)
	put("X-MESHSAT-SIDC", c.SIDC)
	if c.TrustLevel > 0 {
		fmt.Fprintf(w, "X-MESHSAT-TRUST-LEVEL:%d\r\n", int(c.TrustLevel))
	}
	for _, a := range c.Addresses {
		switch a.Kind {
		case KindSMS:
			fmt.Fprintf(w, "TEL;TYPE=cell:%s\r\n", escapeVCardValue(a.Value))
		case KindEmail:
			put("EMAIL", a.Value)
		default:
			// Find the extension key for this Kind.
			for key, k := range vcardXMeshsatKinds {
				if k == a.Kind {
					put(key, a.Value)
					break
				}
			}
		}
	}
	fmt.Fprintln(w, "END:VCARD\r")
	return nil
}

// escapeVCardValue escapes the characters RFC 6350 §3.4 requires
// within property values: comma, semicolon, backslash, newline.
func escapeVCardValue(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		";", `\;`,
		",", `\,`,
		"\n", `\n`,
		"\r", "",
	)
	return r.Replace(s)
}

// unescapeVCardValue reverses [escapeVCardValue] for common cases.
func unescapeVCardValue(s string) string {
	// Process in a single pass to avoid re-expanding escaped
	// backslashes. Simple state machine over the input.
	b := strings.Builder{}
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
				i++
				continue
			case ';', ',', '\\':
				b.WriteByte(s[i+1])
				i++
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// ImportContacts writes the parsed contacts through the Store,
// creating new records. Caller controls the tenant and origin via
// the Contact fields before calling; this helper only handles the
// batched Put pattern and collects the resulting IDs.
func ImportContacts(ctx context.Context, store interface {
	CreateContact(context.Context, *Contact) error
	AddAddress(context.Context, *Address) error
}, contacts []Contact) (imported int, err error) {
	for i := range contacts {
		c := &contacts[i]
		if c.Origin == "" {
			c.Origin = OriginImported
		}
		if err := store.CreateContact(ctx, c); err != nil {
			return imported, fmt.Errorf("import contact %q: %w", c.DisplayName, err)
		}
		for j := range c.Addresses {
			a := &c.Addresses[j]
			a.ContactID = c.ID
			if err := store.AddAddress(ctx, a); err != nil {
				return imported, fmt.Errorf("import address %s for %q: %w", a.Value, c.DisplayName, err)
			}
		}
		imported++
	}
	return imported, nil
}
