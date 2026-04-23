package ImmuDB_CA

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// EnsureTLSAssets makes sure you have a CA and server cert/key in dir.
// If any are missing or expired, it regenerates them.
func EnsureTLSAssets(dir string) error {
	// paths
	caKeyFile := filepath.Join(dir, "ca.key.pem")
	caCertFile := filepath.Join(dir, "ca.cert.pem")
	srvKeyFile := filepath.Join(dir, "server.key.pem")
	srvCertFile := filepath.Join(dir, "server.cert.pem")

	// 1) Make dir
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	now := time.Now().UTC()

	// 2) CA: regen if missing or expired
	caCert, caKey, err := loadOrCreateCA(caCertFile, caKeyFile, now)
	if err != nil {
		return fmt.Errorf("CA setup: %w", err)
	}

	// 3) Server: regen if missing or expired
	if err := loadOrCreateServerCert(caCert, caKey, srvCertFile, srvKeyFile, now); err != nil {
		return fmt.Errorf("server cert setup: %w", err)
	}

	return nil
}

func loadOrCreateCA(certPath, keyPath string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	// try load existing
	if certPEM, keyPEM, err := readPEMFiles(certPath, keyPath); err == nil {
		if cert, key, ok := parseCA(certPEM, keyPEM); ok && cert.NotAfter.After(now) {
			return cert, key, nil
		}
	}
	// else generate new
	return generateCA(certPath, keyPath, now)
}

func loadOrCreateServerCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, certPath, keyPath string, now time.Time) error {
	// try load existing
	if certPEM, keyPEM, err := readPEMFiles(certPath, keyPath); err == nil {
		if srvCert, _, ok := parseCert(certPEM, keyPEM); ok && srvCert.NotAfter.After(now) {
			return nil
		}
	}
	// else generate new
	return generateServerCert(caCert, caKey, certPath, keyPath, now)
}

func readPEMFiles(certPath, keyPath string) ([]byte, []byte, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, bool) {
	// parse cert
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return nil, nil, false
	}
	// parse key
	block, _ = pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, nil, false
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, false
	}
	return cert, key, true
}

func parseCert(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, bool) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, false
	}
	block, _ = pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, nil, false
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, false
	}
	return cert, key, true
}

func generateCA(certPath, keyPath string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	// create ECDSA P256 CA key
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA key: %w", err)
	}

	// cert template
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "ImmuDB-Local-CA",
			Organization: []string{"Your Org"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA cert: %w", err)
	}

	// write files
	if err := writePEM(certPath, "CERTIFICATE", der); err != nil {
		return nil, nil, err
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER); err != nil {
		return nil, nil, err
	}

	cert, _ := x509.ParseCertificate(der)
	return cert, key, nil
}

func generateServerCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, certPath, keyPath string, now time.Time) error {
	// server key
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate server key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "localhost",
			Organization: []string{"Your Org"},
		},
		NotBefore:   now.Add(-1 * time.Hour),
		NotAfter:    now.Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create server cert: %w", err)
	}

	// write files
	if err := writePEM(certPath, "CERTIFICATE", der); err != nil {
		return err
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER); err != nil {
		return err
	}
	return nil
}

func writePEM(path, blockType string, derBytes []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: derBytes})
}
