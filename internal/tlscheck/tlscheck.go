// Package tlscheck reports the real state of a domain's certificate by doing
// what a visitor's browser does: a TLS handshake to the hostname, then
// verifying what came back. It replaces guessing from Traefik's acme.json —
// whatever the edge actually serves is the truth.
package tlscheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"
)

// Statuses stored in domains.tls_status.
const (
	Pending   = "pending"   // no certificate for this name yet (default cert, wrong name, or unreachable)
	Active    = "active"    // valid and trusted
	Expiring  = "expiring"  // valid, but expires within ExpiringWithin
	Untrusted = "untrusted" // served for this name, but the chain is not trusted (e.g. Let's Encrypt staging)
	Failed    = "failed"    // expired or otherwise invalid
)

// ExpiringWithin is how early a certificate counts as "expiring".
const ExpiringWithin = 14 * 24 * time.Hour

// Result is what the handshake showed.
type Result struct {
	Status  string
	Expires time.Time // zero when no usable certificate was seen
	Detail  string    // one plain sentence for tooltips and logs
}

// Check handshakes with host (addr is host:443 unless a test overrides it) and
// classifies the certificate. roots nil means the system roots. It never
// returns an error: every outcome is a Result, because "unreachable" and
// "no certificate yet" are exactly the states being reported.
func Check(ctx context.Context, host, addr string, roots *x509.CertPool, now time.Time) Result {
	if addr == "" {
		addr = net.JoinHostPort(host, "443")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: host, InsecureSkipVerify: true}} // verified by hand below
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{Status: Pending, Detail: "could not complete a TLS handshake: " + err.Error()}
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return Result{Status: Pending, Detail: "the server presented no certificate"}
	}
	leaf := certs[0]
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter, CurrentTime: now})
	if verr == nil {
		if leaf.NotAfter.Sub(now) < ExpiringWithin {
			return Result{Status: Expiring, Expires: leaf.NotAfter, Detail: fmt.Sprintf("the certificate expires %s", leaf.NotAfter.Format("2 Jan 2006"))}
		}
		return Result{Status: Active, Expires: leaf.NotAfter, Detail: fmt.Sprintf("valid until %s", leaf.NotAfter.Format("2 Jan 2006"))}
	}
	var he x509.HostnameError
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	switch {
	case errors.As(verr, &he):
		return Result{Status: Pending, Detail: "the edge is serving a certificate for other names — this domain's certificate has not been issued yet"}
	case errors.As(verr, &ci) && ci.Reason == x509.Expired:
		if now.Before(leaf.NotBefore) {
			return Result{Status: Failed, Expires: leaf.NotAfter, Detail: "the certificate is not valid yet (check the server clock)"}
		}
		return Result{Status: Failed, Expires: leaf.NotAfter, Detail: fmt.Sprintf("the certificate expired %s", leaf.NotAfter.Format("2 Jan 2006"))}
	case errors.As(verr, &ua):
		return Result{Status: Untrusted, Expires: leaf.NotAfter, Detail: "the certificate is for this domain but its issuer is not trusted (Let's Encrypt staging, or a self-signed certificate)"}
	}
	return Result{Status: Failed, Expires: leaf.NotAfter, Detail: "the certificate is invalid: " + verr.Error()}
}
