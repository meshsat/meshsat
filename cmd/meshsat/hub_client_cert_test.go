package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"meshsat/internal/database"
)

// The certificate the tcp_rns interfaces present: the Hub's, only when both
// the certificate and the key are stored, read again at every call.
func TestHubClientCertificate(t *testing.T) {
	db, err := database.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	get := hubClientCertificate(db)
	save := func(v map[string]string) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := db.SetSystemConfig("hub_connection", string(b)); err != nil {
			t.Fatal(err)
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bridge-e2e"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))

	if c, err := get(); c != nil || err != nil {
		t.Fatalf("no Hub settings: %v %v", c, err)
	}
	save(map[string]string{"url": "wss://hub.example/mqtt", "tls_cert_pem": certPEM})
	if c, err := get(); c != nil || err != nil {
		t.Fatalf("certificate without its key: %v %v", c, err)
	}
	save(map[string]string{"url": "wss://hub.example/mqtt", "tls_cert_pem": certPEM, "tls_key_pem": keyPEM})
	c, err := get()
	if err != nil || c == nil || len(c.Certificate) != 1 {
		t.Fatalf("certificate and key: %v %v", c, err)
	}
	if leaf, err := x509.ParseCertificate(c.Certificate[0]); err != nil || leaf.Subject.CommonName != "bridge-e2e" {
		t.Fatalf("leaf %v %v", leaf, err)
	}
	save(map[string]string{"tls_cert_pem": certPEM, "tls_key_pem": "not a key"})
	if c, err := get(); c != nil || err == nil || !strings.HasPrefix(err.Error(), "hub_connection: ") {
		t.Fatalf("a key that is not one: %v %v", c, err)
	}
}
