// Package tlsutil builds TLS configurations for IMAP and submission.
package tlsutil

import (
	"context"
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
	"sync/atomic"
	"time"

	"github.com/synqronlabs/perch/internal/config"
	"golang.org/x/crypto/acme/autocert"
)

// Reloader holds a manually supplied certificate and can replace it on SIGHUP.
type Reloader struct {
	certFile string
	keyFile  string
	cert     atomic.Pointer[tls.Certificate]
}

// NewReloader loads certFile/keyFile and returns a reloadable certificate
// provider.
func NewReloader(certFile, keyFile string) (*Reloader, error) {
	r := &Reloader{certFile: certFile, keyFile: keyFile}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Reload rereads the certificate and key files atomically.
func (r *Reloader) Reload() error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	r.cert.Store(&cert)
	return nil
}

// Config returns a TLS config backed by the reloadable certificate.
func (r *Reloader) Config() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert := r.cert.Load()
			if cert == nil {
				return nil, errors.New("tls: certificate is not loaded")
			}
			return cert, nil
		},
	}
}

// Load returns a TLS config for the configured certificate source. It returns
// (nil, nil, nil, nil) when no certificate source is configured.
func Load(cfg config.TLSConfig, dataDir string) (*tls.Config, *autocert.Manager, *Reloader, error) {
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		reloader, err := NewReloader(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, nil, nil, err
		}
		return reloader.Config(), nil, reloader, nil
	}
	if !cfg.ACME.Enabled {
		return nil, nil, nil, nil
	}
	cacheDir := cfg.ACME.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(dataDir, "acme")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("create ACME cache directory: %w", err)
	}
	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cacheDir),
		Email:      cfg.ACME.Email,
		HostPolicy: autocert.HostWhitelist(cfg.ACME.Domains...),
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: manager.GetCertificate,
	}, manager, nil, nil
}

// RunACMEHTTP serves HTTP-01 challenges until ctx is cancelled.
func RunACMEHTTP(ctx context.Context, manager *autocert.Manager, addr string) error {
	if manager == nil {
		return nil
	}
	if addr == "" {
		addr = ":80"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           manager.HTTPHandler(nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// GenerateSelfSigned writes a local development certificate and key.
func GenerateSelfSigned(certPath, keyPath, hostname string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate private key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{hostname, "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("write certificate: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	return nil
}

// CertificateExpiry returns the leaf certificate's NotAfter time.
func CertificateExpiry(certPath string) (time.Time, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return time.Time{}, errors.New("tls: no PEM certificate found")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}
