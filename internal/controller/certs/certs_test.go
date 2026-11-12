package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeACME creates an acme.json like Traefik's with one self-signed
// certificate valid from notBefore to notAfter.
func writeACME(t *testing.T, domain string, notBefore, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test CA"},
		DNSNames:     []string{domain, "*." + domain},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	doc := map[string]any{"letsencrypt": map[string]any{
		"Account": map[string]any{"Email": "x"},
		"Certificates": []any{map[string]any{
			"domain":      map[string]any{"main": domain, "sans": []string{"*." + domain}},
			"certificate": base64.StdEncoding.EncodeToString(chain),
			"key":         "bm90IGEga2V5",
		}},
	}}
	raw, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "acme.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRead(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	issued := now.Add(-24 * time.Hour)
	tests := []struct {
		name      string
		expires   time.Time
		wantState string
		wantDays  int
	}{
		{"fresh", now.Add(60 * 24 * time.Hour), StateValid, 60},
		{"renewal overdue", now.Add(10 * 24 * time.Hour), StateExpiring, 10},
		{"expired", now.Add(-time.Hour), StateExpired, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeACME(t, "home.example.com", issued, tt.expires)
			got, err := Read(path, "home.example.com", now)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tt.wantState || got.DaysLeft != tt.wantDays {
				t.Errorf("state %s, %d days; want %s, %d days", got.State, got.DaysLeft, tt.wantState, tt.wantDays)
			}
			if got.Issuer != "Test CA" || len(got.Domains) != 2 || !got.IssuedAt.Equal(issued) {
				t.Errorf("details: %+v", got)
			}
		})
	}
}

func TestReadNoCertificate(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.json")
	_ = os.WriteFile(empty, nil, 0o600)
	otherDomain := writeACME(t, "other.example.org", now, now.Add(time.Hour))

	for _, path := range []string{filepath.Join(dir, "missing.json"), empty, otherDomain} {
		got, err := Read(path, "home.example.com", now)
		if err != nil || got.State != StateNone {
			t.Errorf("Read(%s) = %+v, %v; want state none", filepath.Base(path), got, err)
		}
	}
}

func TestLogWatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traefik.log")
	old := `{"level":"error","providerName":"letsencrypt.acme","time":"2026-01-01T10:00:00Z","message":"Unable to obtain ACME certificate for domains","error":"cloudflare: token SECRET-TOKEN rejected"}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	log := slog.New(slog.NewTextHandler(&out, nil))
	w := NewLogWatcher(path, func() []string { return []string{"SECRET-TOKEN"} }, log)

	// Existing lines are analysed but not printed again.
	offset := w.read(0, false)
	msg, at := w.LastACMEError()
	if !strings.Contains(msg, "Unable to obtain ACME certificate") || at.Year() != 2026 {
		t.Errorf("LastACMEError() = %q, %s", msg, at)
	}
	if strings.Contains(msg, "SECRET-TOKEN") || !strings.Contains(msg, "[redacted]") {
		t.Errorf("secret was not redacted: %q", msg)
	}
	if out.Len() != 0 {
		t.Errorf("old lines were forwarded: %s", out.String())
	}

	// New lines are forwarded; an incomplete line waits for its end.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"level":"info","message":"Configuration loaded"}` + "\n" + `{"level":"warn","mess`)
	_ = f.Close()
	offset = w.read(offset, true)
	if !strings.Contains(out.String(), "traefik: Configuration loaded") || strings.Contains(out.String(), "WARN") {
		t.Errorf("forwarded output: %s", out.String())
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`age":"careful"}` + "\n")
	_ = f.Close()
	offset = w.read(offset, true)
	if !strings.Contains(out.String(), "level=WARN") || !strings.Contains(out.String(), "traefik: careful") {
		t.Errorf("completed line was not forwarded: %s", out.String())
	}

	// After a rotation the file is shorter: reading starts from the top.
	_ = os.WriteFile(path, []byte(`{"level":"info","message":"after rotation"}`+"\n"), 0o600)
	w.read(offset, true)
	if !strings.Contains(out.String(), "after rotation") {
		t.Errorf("rotation was not detected: %s", out.String())
	}

	// Run stops when the context is cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
