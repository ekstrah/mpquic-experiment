// Package tlsconfig provides the minimal self-signed TLS setup QUIC
// requires. This is a private experiment tool between two hosts you
// control, not a public service, so a generated cert plus a client that
// skips verification is enough; swap in real certs if that ever changes.
package tlsconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"
)

// ALPN is the application protocol negotiated over the QUIC/TLS handshake.
const ALPN = "mpquic-exp/1"

// ServerConfig generates a self-signed cert and returns a server-side
// *tls.Config for it.
func ServerConfig() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tlsconfig: generating key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"mpquic-experiment"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("tlsconfig: creating certificate: %w", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{ALPN},
	}, nil
}

// ClientConfig returns a client-side *tls.Config that trusts whatever
// self-signed cert the server presents.
func ClientConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{ALPN},
	}
}
