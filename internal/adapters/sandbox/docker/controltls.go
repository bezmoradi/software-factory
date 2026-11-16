package docker

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const controlCertLife = 10 * 365 * 24 * time.Hour

func statusURL(tc *tls.Config, addr, path string) string {
	if tc != nil {
		return "https://" + addr + path
	}
	return "http://" + addr + path
}

func statusClient(tc *tls.Config) *http.Client {
	t := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, DisableKeepAlives: true}
	if tc != nil {
		t.TLSClientConfig = tc.Clone()
		t.TLSHandshakeTimeout = 5 * time.Second
	}
	return &http.Client{Timeout: statusFor, Transport: t}
}

func WriteControlTLS(certPath, keyPath, status string) (*tls.Config, error) {
	if !filepath.IsAbs(certPath) || !filepath.IsAbs(keyPath) {
		return nil, errors.New("egress control: the certificate and key files must be absolute paths")
	}
	host, _, err := net.SplitHostPort(status)
	if err != nil || host == "" {
		return nil, fmt.Errorf("egress control: %q must be host:port", status)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "software-factory egress control"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(controlCertLife),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := writeShared(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o640); err != nil {
		return nil, fmt.Errorf("egress control: write the TLS key: %w", err)
	}
	if err := writeShared(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, fmt.Errorf("egress control: write the TLS certificate: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	pinned := bytes.Clone(der)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
		ServerName: host,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, pinned) {
				return errors.New("egress control: egress didn't show the certificate this engine wrote at its start")
			}
			return nil
		},
	}, nil
}

func writeShared(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}
