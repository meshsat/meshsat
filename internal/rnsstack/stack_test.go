package rnsstack

import (
	"encoding/hex"
	"testing"

	"meshsat/internal/lxmf"
)

// Every accepted inbound LXMF message reaches the hook with its sender and
// its text, title first, so the access rules can carry it on. [MESHSAT-1554]
func TestInboundHookGetsSenderAndText(t *testing.T) {
	st := &Stack{}
	var gotSource, gotText string
	calls := 0
	st.SetInboundHook(func(source, text string, _ []lxmf.Attachment) {
		calls++
		gotSource, gotText = source, text
	})

	var src [lxmf.DestLen]byte
	for i := range src {
		src[i] = byte(i + 1)
	}
	st.storeInbound(&lxmf.Message{Source: src, Title: []byte("Trail"), Content: []byte("all good")})
	if calls != 1 {
		t.Fatalf("hook called %d times, want 1", calls)
	}
	if gotSource != hex.EncodeToString(src[:]) {
		t.Fatalf("source %q, want %q", gotSource, hex.EncodeToString(src[:]))
	}
	if gotText != "Trail\nall good" {
		t.Fatalf("text %q, want the title then the content", gotText)
	}

	// An empty message is filed but not handed on: nothing to forward.
	st.storeInbound(&lxmf.Message{Source: src})
	if calls != 1 {
		t.Fatalf("an empty message reached the hook")
	}
}
