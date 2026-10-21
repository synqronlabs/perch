package tlsutil

import (
	"crypto/x509"
	"path/filepath"
	"testing"
)

func TestReloader(t *testing.T) {
	dir := t.TempDir()
	cert1 := filepath.Join(dir, "cert1.pem")
	key1 := filepath.Join(dir, "key1.pem")
	if err := GenerateSelfSigned(cert1, key1, "localhost"); err != nil {
		t.Fatal(err)
	}
	reloader, err := NewReloader(cert1, key1)
	if err != nil {
		t.Fatal(err)
	}
	first := serial(t, reloader)
	cert2 := filepath.Join(dir, "cert2.pem")
	key2 := filepath.Join(dir, "key2.pem")
	if err := GenerateSelfSigned(cert2, key2, "localhost"); err != nil {
		t.Fatal(err)
	}
	reloader.certFile = cert2
	reloader.keyFile = key2
	if err := reloader.Reload(); err != nil {
		t.Fatal(err)
	}
	second := serial(t, reloader)
	if first == second {
		t.Fatal("certificate serial did not change after reload")
	}
}

func serial(t *testing.T, reloader *Reloader) string {
	t.Helper()
	cert, err := reloader.Config().GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.String()
}
