// Package certs reports on the wildcard certificate that Traefik
// obtains and renews. The controller never requests certificates
// itself; it only reads what Traefik stored.
package certs

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// WarnBefore is how long before expiry the UI starts to warn. Traefik
// renews much earlier, so getting this close means renewal is failing.
const WarnBefore = 14 * 24 * time.Hour

// Possible values of Status.State.
const (
	StateNone     = "none"     // no certificate yet
	StateValid    = "valid"    //
	StateExpiring = "expiring" // less than WarnBefore left
	StateExpired  = "expired"  //
)

// Status describes the current certificate.
type Status struct {
	State   string   `json:"state"`
	Domains []string `json:"domains,omitempty"`
	Issuer  string   `json:"issuer,omitempty"`
	// IssuedAt is the start of the validity period, which is the time
	// of the last issuance or renewal.
	IssuedAt  *time.Time `json:"issued_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	DaysLeft  int        `json:"days_left"`
}

// acmeFile mirrors the parts of Traefik's acme.json that we read. The
// private keys in the same file are never decoded.
type acmeFile map[string]struct {
	Certificates []struct {
		Domain struct {
			Main string   `json:"main"`
			SANs []string `json:"sans"`
		} `json:"domain"`
		Certificate string `json:"certificate"` // base64 of a PEM chain
	} `json:"Certificates"`
}

// Read returns the status of the certificate for baseDomain stored in
// the given acme.json. A missing file is not an error: it means that no
// certificate has been issued yet.
func Read(path, baseDomain string, now time.Time) (Status, error) {
	none := Status{State: StateNone}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(raw) == 0) {
		return none, nil
	}
	if err != nil {
		return none, err
	}
	var file acmeFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return none, fmt.Errorf("parse %s: %w", path, err)
	}

	for _, resolver := range file {
		for _, entry := range resolver.Certificates {
			if entry.Domain.Main != baseDomain {
				continue
			}
			cert, err := parseLeaf(entry.Certificate)
			if err != nil {
				return none, err
			}
			status := Status{
				State:     StateValid,
				Domains:   cert.DNSNames,
				Issuer:    cert.Issuer.CommonName,
				IssuedAt:  &cert.NotBefore,
				ExpiresAt: &cert.NotAfter,
			}
			left := cert.NotAfter.Sub(now)
			status.DaysLeft = int(left.Hours() / 24)
			switch {
			case left <= 0:
				status.State = StateExpired
			case left < WarnBefore:
				status.State = StateExpiring
			}
			return status, nil
		}
	}
	return none, nil
}

// parseLeaf decodes the first certificate of a base64-encoded PEM
// chain; by convention that is the server certificate itself.
func parseLeaf(encoded string) (*x509.Certificate, error) {
	chain, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode certificate: %w", err)
	}
	block, _ := pem.Decode(chain)
	if block == nil {
		return nil, errors.New("decode certificate: no PEM data")
	}
	return x509.ParseCertificate(block.Bytes)
}
