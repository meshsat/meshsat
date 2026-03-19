package certpin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

func selfSignedCert(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestSPKIHash(t *testing.T) {
	cert := selfSignedCert(t)
	hash := SPKIHash(cert)
	if hash == "" {
		t.Error("expected non-empty hash")
	}
	if len(hash) != 44 { // base64-encoded SHA-256 = 44 chars
		t.Errorf("hash length = %d, want 44", len(hash))
	}

	// Same cert should produce same hash.
	if SPKIHash(cert) != hash {
		t.Error("same cert should produce same hash")
	}
}

func TestVerify_Match(t *testing.T) {
	cert := selfSignedCert(t)
	hash := SPKIHash(cert)
	pin := NewPin(hash)

	chains := [][]*x509.Certificate{{cert}}
	if err := pin.Verify(chains); err != nil {
		t.Errorf("expected match: %v", err)
	}
}

func TestVerify_NoMatch(t *testing.T) {
	cert := selfSignedCert(t)
	pin := NewPin("dGhpcyBpcyBub3QgYSByZWFsIGhhc2ggYXQgYWxs") // fake hash

	chains := [][]*x509.Certificate{{cert}}
	if err := pin.Verify(chains); err == nil {
		t.Error("expected no match error")
	}
}

func TestVerify_BackupPin(t *testing.T) {
	cert := selfSignedCert(t)
	hash := SPKIHash(cert)
	pin := NewPin("fakehash1234567890", hash) // primary fake, backup matches

	chains := [][]*x509.Certificate{{cert}}
	if err := pin.Verify(chains); err != nil {
		t.Errorf("backup pin should match: %v", err)
	}
}

func TestVerify_NoPins(t *testing.T) {
	cert := selfSignedCert(t)
	pin := NewPin()

	chains := [][]*x509.Certificate{{cert}}
	if err := pin.Verify(chains); err != nil {
		t.Errorf("no pins should allow all: %v", err)
	}
}

func TestPinnedClient(t *testing.T) {
	pin := NewPin("somehash")
	client := PinnedClient(pin, 10*time.Second)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Transport == nil {
		t.Fatal("expected non-nil transport")
	}
	if client.Timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", client.Timeout)
	}
}

func TestFromEnv_Empty(t *testing.T) {
	t.Setenv("TEST_PIN_PRIMARY", "")
	t.Setenv("TEST_PIN_BACKUP", "")
	pin := FromEnv("TEST_PIN_PRIMARY", "TEST_PIN_BACKUP")
	if pin != nil {
		t.Error("expected nil pin when no env vars set")
	}
}

func TestFromEnv_PrimaryOnly(t *testing.T) {
	t.Setenv("TEST_PIN_PRIMARY", "abc123")
	t.Setenv("TEST_PIN_BACKUP", "")
	pin := FromEnv("TEST_PIN_PRIMARY", "TEST_PIN_BACKUP")
	if pin == nil {
		t.Fatal("expected non-nil pin")
	}
	if len(pin.Hashes) != 1 {
		t.Errorf("expected 1 hash, got %d", len(pin.Hashes))
	}
}

func TestFromEnv_Both(t *testing.T) {
	t.Setenv("TEST_PIN_PRIMARY", "abc123")
	t.Setenv("TEST_PIN_BACKUP", "def456")
	pin := FromEnv("TEST_PIN_PRIMARY", "TEST_PIN_BACKUP")
	if pin == nil {
		t.Fatal("expected non-nil pin")
	}
	if len(pin.Hashes) != 2 {
		t.Errorf("expected 2 hashes, got %d", len(pin.Hashes))
	}
}
