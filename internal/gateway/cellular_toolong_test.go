package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"meshsat/internal/transport"
)

// A ciphertext or a frame that does not fit one SMS is refused, never cut:
// cut short it cannot be decrypted or verified at the far end. A person's
// words still lose their tail, as before. [MESHSAT-1581]
func TestCellular_TooLongCiphertextIsRefused(t *testing.T) {
	cell := newFakeCellTransport()
	gw := NewCellularGateway(CellularConfig{SMSPrefix: "MeshSat", MaxSMSSegments: 1}, cell, nil)
	ctx := context.Background()
	to := []string{"+31600000003"}

	for name, msg := range map[string]*transport.MeshMessage{
		"ciphertext":       {DecodedText: strings.Repeat("Q", 172), Encrypted: true, SMSDestinations: to},
		"oob frame":        {DecodedText: "MS:" + strings.Repeat("A", 170), RawText: true, SMSDestinations: to},
		"chat-key sealed":  {DecodedText: "short", SMSTexts: map[string]string{to[0]: strings.Repeat("Q", 164)}, SMSDestinations: to},
		"ciphertext, 161":  {DecodedText: strings.Repeat("Q", 161), Encrypted: true, SMSDestinations: to},
		"two segments set": {DecodedText: strings.Repeat("Q", 200), Encrypted: true, SMSDestinations: to},
	} {
		if name == "two segments set" {
			gw.config.MaxSMSSegments = 2 // the fake is no text-mode modem: 320 fit
			if err := gw.sendSMSSync(ctx, msg); err != nil {
				t.Errorf("%s: %v; want it sent whole", name, err)
			}
			gw.config.MaxSMSSegments = 1
			continue
		}
		if err := gw.sendSMSSync(ctx, msg); !errors.Is(err, transport.ErrPayloadTooLong) {
			t.Errorf("%s: %v; want ErrPayloadTooLong", name, err)
		}
	}
	cell.mu.Lock()
	sent := append([][2]string{}, cell.sent...)
	cell.mu.Unlock()
	if len(sent) != 1 || len(sent[0][1]) != 200 {
		t.Fatalf("on air %d messages (%v); want only the 200-character one, whole", len(sent), sent)
	}

	// Exactly 160 characters of ciphertext still go.
	if err := gw.sendSMSSync(ctx, &transport.MeshMessage{DecodedText: strings.Repeat("Q", 160), Encrypted: true, SMSDestinations: to}); err != nil {
		t.Fatalf("160 characters: %v", err)
	}
	// A plain text over the limit is cut, as before.
	if err := gw.sendSMSSync(ctx, &transport.MeshMessage{DecodedText: strings.Repeat("w", 170), SMSDestinations: to}); err != nil {
		t.Fatalf("plain text: %v", err)
	}
	cell.mu.Lock()
	sent = append([][2]string{}, cell.sent...)
	cell.mu.Unlock()
	if last := sent[len(sent)-1][1]; len(last) != 160 {
		t.Fatalf("plain text on air %d characters, want 160", len(last))
	}
}

// textModeCell is a kit's USB modem: one SMS of at most 160 characters.
type textModeCell struct{ *fakeCellTransport }

func (textModeCell) SMSTextMode() bool { return true }

// On a text-mode modem a ciphertext over 160 characters is refused even
// when max_sms_segments allows more: the transport sends one SMS only.
func TestCellular_TextModeModemTakesOneSMS(t *testing.T) {
	cell := textModeCell{newFakeCellTransport()}
	gw := NewCellularGateway(CellularConfig{SMSPrefix: "MeshSat", MaxSMSSegments: 2}, cell, nil)
	err := gw.sendSMSSync(context.Background(), &transport.MeshMessage{DecodedText: strings.Repeat("Q", 200), Encrypted: true,
		SMSDestinations: []string{"+31600000004"}})
	if !errors.Is(err, transport.ErrPayloadTooLong) {
		t.Fatalf("200 characters on a text-mode modem: %v; want ErrPayloadTooLong", err)
	}
	cell.mu.Lock()
	defer cell.mu.Unlock()
	if len(cell.sent) != 0 {
		t.Fatalf("on air: %v", cell.sent)
	}
}
