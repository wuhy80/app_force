package security

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedCertificateLoadsAndIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	pin, err := GenerateCertificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(cert.Raw)
	if pin != hex.EncodeToString(hash[:]) {
		t.Fatal("fingerprint does not identify certificate")
	}
	before, _ := os.ReadFile(filepath.Join(dir, "server.key"))
	if _, err := GenerateCertificate(dir); err == nil {
		t.Fatal("existing identity replaced")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "server.key"))
	if string(before) != string(after) {
		t.Fatal("private key overwritten")
	}
}
