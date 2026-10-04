package tlscheck

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// serve starts a TLS listener presenting a certificate for dnsName valid in
// [notBefore, notAfter], signed by its own CA. It returns the address and a
// pool trusting that CA.
func serve(t *testing.T, dnsName string, notBefore, notAfter time.Time) (string, *x509.CertPool) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: dnsName}, DNSNames: []string{dnsName},
		NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return ln.Addr().String(), pool
}

func TestCheckClassifiesCertificates(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		certFor    string
		notBefore  time.Time
		notAfter   time.Time
		trust      bool
		want       string
		wantExpiry bool
	}{
		{"valid", "app.example.com", now.Add(-time.Hour), now.Add(60 * 24 * time.Hour), true, Active, true},
		{"expiring soon", "app.example.com", now.Add(-time.Hour), now.Add(5 * 24 * time.Hour), true, Expiring, true},
		{"expired", "app.example.com", now.Add(-48 * time.Hour), now.Add(-time.Hour), true, Failed, true},
		{"other names (default cert)", "TRAEFIK DEFAULT CERT", now.Add(-time.Hour), now.Add(time.Hour * 24 * 90), true, Pending, false},
		{"untrusted issuer (staging)", "app.example.com", now.Add(-time.Hour), now.Add(60 * 24 * time.Hour), false, Untrusted, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, pool := serve(t, tc.certFor, tc.notBefore, tc.notAfter)
			roots := pool
			if !tc.trust {
				roots = x509.NewCertPool() // trusts nothing
			}
			got := Check(context.Background(), "app.example.com", addr, roots, now)
			if got.Status != tc.want {
				t.Errorf("status = %q (%s), want %q", got.Status, got.Detail, tc.want)
			}
			if tc.wantExpiry && got.Expires.IsZero() {
				t.Error("expected an expiry date")
			}
			if got.Detail == "" {
				t.Error("every result explains itself")
			}
		})
	}
}

func TestCheckUnreachableIsPendingNotAnError(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens any more
	got := Check(context.Background(), "gone.example.com", addr, nil, time.Now())
	if got.Status != Pending || got.Detail == "" {
		t.Errorf("result = %+v, want pending with a reason", got)
	}
}
