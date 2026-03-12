package routing

import (
	"testing"
)

func TestLinkEstablishment_FullHandshake(t *testing.T) {
	// Two identities: initiator (Alice) and responder (Bob)
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	// Step 1: Alice initiates a link to Bob
	reqData, pendingLink, err := aliceLM.InitiateLink(bob.DestHash())
	if err != nil {
		t.Fatalf("initiate link: %v", err)
	}
	if pendingLink.State != LinkStatePending {
		t.Errorf("pending link state: got %d, want %d", pendingLink.State, LinkStatePending)
	}
	if len(reqData) != LinkRequestLen {
		t.Errorf("request length: got %d, want %d", len(reqData), LinkRequestLen)
	}

	// Step 2: Bob receives the request and sends a response
	respData, err := bobLM.HandleLinkRequest(reqData)
	if err != nil {
		t.Fatalf("handle link request: %v", err)
	}
	if len(respData) != LinkResponseLen {
		t.Errorf("response length: got %d, want %d", len(respData), LinkResponseLen)
	}

	// Step 3: Alice receives the response and sends a confirm
	confirmData, err := aliceLM.HandleLinkResponse(respData, bob.SigningPublicKey())
	if err != nil {
		t.Fatalf("handle link response: %v", err)
	}
	if len(confirmData) != LinkConfirmLen {
		t.Errorf("confirm length: got %d, want %d", len(confirmData), LinkConfirmLen)
	}

	// Step 4: Bob receives the confirm
	if err := bobLM.HandleLinkConfirm(confirmData); err != nil {
		t.Fatalf("handle link confirm: %v", err)
	}

	// Verify both sides have established links
	aliceLinks := aliceLM.ActiveLinks()
	bobLinks := bobLM.ActiveLinks()
	if len(aliceLinks) != 1 {
		t.Fatalf("alice should have 1 active link, got %d", len(aliceLinks))
	}
	if len(bobLinks) != 1 {
		t.Fatalf("bob should have 1 active link, got %d", len(bobLinks))
	}

	// Verify link IDs match
	if aliceLinks[0].ID != bobLinks[0].ID {
		t.Error("link IDs should match")
	}

	// Total wire bytes: 65 + 129 + 65 = 259 bytes
	totalBytes := len(reqData) + len(respData) + len(confirmData)
	if totalBytes > 340 {
		t.Errorf("total handshake %d bytes exceeds single Iridium SBD (340)", totalBytes)
	}
	t.Logf("total handshake: %d bytes (fits SBD: %v)", totalBytes, totalBytes <= 340)
}

func TestLinkEstablishment_EncryptDecrypt(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	// Full handshake
	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	respData, _ := bobLM.HandleLinkRequest(reqData)
	confirmData, _ := aliceLM.HandleLinkResponse(respData, bob.SigningPublicKey())
	bobLM.HandleLinkConfirm(confirmData)

	aliceLink := aliceLM.ActiveLinks()[0]
	bobLink := bobLM.ActiveLinks()[0]

	// Alice encrypts, Bob decrypts
	plaintext := []byte("hello from alice")
	ciphertext, err := aliceLink.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := bobLink.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Errorf("decrypted: got %q, want %q", decrypted, plaintext)
	}

	// Bob encrypts, Alice decrypts
	plaintext2 := []byte("hello from bob")
	ciphertext2, err := bobLink.Encrypt(plaintext2)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted2, err := aliceLink.Decrypt(ciphertext2)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(decrypted2) != string(plaintext2) {
		t.Errorf("decrypted: got %q, want %q", decrypted2, plaintext2)
	}
}

func TestLinkEstablishment_WrongDestination(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)
	charlie := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	charlieLM := NewLinkManager(charlie)

	// Alice sends request to Bob, but Charlie tries to handle it
	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	_, err := charlieLM.HandleLinkRequest(reqData)
	if err == nil {
		t.Fatal("charlie should reject request addressed to bob")
	}
}

func TestLinkEstablishment_BadSignature(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)
	charlie := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	respData, _ := bobLM.HandleLinkRequest(reqData)

	// Alice verifies with Charlie's key instead of Bob's
	_, err := aliceLM.HandleLinkResponse(respData, charlie.SigningPublicKey())
	if err == nil {
		t.Fatal("should reject response with wrong signing key")
	}
}

func TestLinkEstablishment_BadConfirmProof(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	respData, _ := bobLM.HandleLinkRequest(reqData)
	confirmData, _ := aliceLM.HandleLinkResponse(respData, bob.SigningPublicKey())

	// Tamper with the proof
	confirmData[len(confirmData)-1] ^= 0xff
	err := bobLM.HandleLinkConfirm(confirmData)
	if err == nil {
		t.Fatal("should reject tampered confirm proof")
	}
}

func TestLinkEstablishment_NoPendingLink(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)
	bobLM := NewLinkManager(bob)

	// Craft a valid-looking response with a random link ID
	aliceLM := NewLinkManager(alice)
	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	respData, _ := bobLM.HandleLinkRequest(reqData)

	// A fresh link manager has no pending links
	freshLM := NewLinkManager(alice)
	_, err := freshLM.HandleLinkResponse(respData, bob.SigningPublicKey())
	if err == nil {
		t.Fatal("should reject response with no pending link")
	}
}

func TestLinkManager_CloseLink(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	respData, _ := bobLM.HandleLinkRequest(reqData)
	aliceLM.HandleLinkResponse(respData, bob.SigningPublicKey())

	links := aliceLM.ActiveLinks()
	if len(links) != 1 {
		t.Fatal("should have 1 active link")
	}

	aliceLM.CloseLink(links[0].ID)
	if len(aliceLM.ActiveLinks()) != 0 {
		t.Fatal("should have 0 active links after close")
	}
}

func TestLinkRequest_Roundtrip(t *testing.T) {
	alice := testIdentity(t)
	aliceLM := NewLinkManager(alice)

	bob := testIdentity(t)
	data, _, _ := aliceLM.InitiateLink(bob.DestHash())

	parsed, err := UnmarshalLinkRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.DestHash != bob.DestHash() {
		t.Error("dest hash mismatch")
	}
}

func TestLinkResponse_Roundtrip(t *testing.T) {
	alice := testIdentity(t)
	bob := testIdentity(t)

	aliceLM := NewLinkManager(alice)
	bobLM := NewLinkManager(bob)

	reqData, _, _ := aliceLM.InitiateLink(bob.DestHash())
	respData, _ := bobLM.HandleLinkRequest(reqData)

	parsed, err := UnmarshalLinkResponse(respData)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.EphemeralPub == nil {
		t.Error("ephemeral pub should not be nil")
	}
}

func TestUnmarshalLinkRequest_TooShort(t *testing.T) {
	_, err := UnmarshalLinkRequest([]byte{PacketLinkRequest, 0x00})
	if err == nil {
		t.Fatal("should fail on short data")
	}
}

func TestUnmarshalLinkResponse_TooShort(t *testing.T) {
	_, err := UnmarshalLinkResponse([]byte{PacketLinkResponse, 0x00})
	if err == nil {
		t.Fatal("should fail on short data")
	}
}

func TestUnmarshalLinkConfirm_TooShort(t *testing.T) {
	_, err := UnmarshalLinkConfirm([]byte{PacketLinkConfirm, 0x00})
	if err == nil {
		t.Fatal("should fail on short data")
	}
}

func TestLink_EncryptWithoutEstablished(t *testing.T) {
	link := &Link{State: LinkStatePending}
	_, err := link.Encrypt([]byte("test"))
	if err == nil {
		t.Fatal("should fail on non-established link")
	}
}

func TestLink_DecryptWithoutEstablished(t *testing.T) {
	link := &Link{State: LinkStatePending}
	_, err := link.Decrypt([]byte("test"))
	if err == nil {
		t.Fatal("should fail on non-established link")
	}
}
