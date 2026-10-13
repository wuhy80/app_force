package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// GenerateCertificate creates a device certificate once. Pairing uses its SHA-256 fingerprint.
func GenerateCertificate(dir string) (string, error) {
	for _, name := range []string{"server.crt", "server.key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			return "", fmt.Errorf("%s 已存在或不可访问，未覆盖", name)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", err
	}
	host, _ := os.Hostname()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "AppForce " + host},
		NotBefore: time.Now().Add(-5 * time.Minute), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost", host}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if addresses, e := net.InterfaceAddrs(); e == nil {
		for _, addr := range addresses {
			ip, _, e := net.ParseCIDR(addr.String())
			if e == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				template.IPAddresses = append(template.IPAddresses, ip)
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	files := []struct {
		name, typ string
		data      []byte
	}{{"server.key", "PRIVATE KEY", keyBytes}, {"server.crt", "CERTIFICATE", der}}
	for _, item := range files {
		file, e := os.OpenFile(filepath.Join(dir, item.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return "", e
		}
		e = pem.Encode(file, &pem.Block{Type: item.typ, Bytes: item.data})
		closeErr := file.Close()
		if e != nil {
			return "", e
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	fingerprint := sha256.Sum256(der)
	return hex.EncodeToString(fingerprint[:]), nil
}
