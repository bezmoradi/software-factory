package egress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"os"
	"sync"
	"time"
)

const maxPairFile = 16 << 10

type pairFiles struct {
	cert, key string
	fallback  *tls.Certificate
	mu        sync.Mutex
	sum       [sha256.Size]byte
	pair      *tls.Certificate
}

func ControlTLS(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("egress: the control channel needs the engine's certificate and key files")
	}
	fallback, err := throwawayPair()
	if err != nil {
		return nil, err
	}
	p := &pairFiles{cert: certFile, key: keyFile, fallback: fallback}
	return &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: p.get}, nil
}

func (p *pairFiles) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cert, errC := readPairFile(p.cert)
	key, errK := readPairFile(p.key)
	if errC != nil || errK != nil {
		return p.fallback, nil
	}
	sum := sha256.Sum256(append(append(cert, 0), key...))
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pair != nil && sum == p.sum {
		return p.pair, nil
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return p.fallback, nil
	}
	p.sum, p.pair = sum, &pair
	return p.pair, nil
}

func readPairFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxPairFile+1))
	if err != nil || len(data) > maxPairFile {
		return nil, errors.New("egress: the control channel's certificate file can't be read")
	}
	return data, nil
}

func throwawayPair() (*tls.Certificate, error) {
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
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "egress waiting for the engine's certificate"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
