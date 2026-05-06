package service

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const certBaseDir = "/root/cert"

var (
	ErrCertInvalidName = errors.New("name must be a non-empty alphanumeric/hyphen/dot string")
	ErrCertParseFailed = errors.New("certificate failed validation")
)

type CertService struct{}

type CertEntry struct {
	Name      string `json:"name"`
	CertPath  string `json:"certPath"`
	KeyPath   string `json:"keyPath"`
	NotBefore int64  `json:"notBefore,omitempty"`
	NotAfter  int64  `json:"notAfter,omitempty"`
	Subject   string `json:"subject,omitempty"`
	DnsNames  []any  `json:"dnsNames,omitempty"`
}

// CertDir returns the directory backing /api/v1/certs. It is configurable so
// tests can use a temp dir; production always uses /root/cert (matching the
// path the bundled acme.sh integration installs into).
func (s *CertService) CertDir() string {
	if v := os.Getenv("XUI_CERT_DIR"); v != "" {
		return v
	}
	return certBaseDir
}

// List returns metadata for every <name>.cer / <name>.key pair found.
func (s *CertService) List() ([]CertEntry, error) {
	dir := s.CertDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []CertEntry{}, nil
		}
		return nil, err
	}
	out := make([]CertEntry, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".cer") {
			continue
		}
		base := strings.TrimSuffix(name, ".cer")
		certPath := filepath.Join(dir, name)
		keyPath := filepath.Join(dir, base+".key")
		if _, err := os.Stat(keyPath); err != nil {
			continue
		}
		entry := CertEntry{
			Name:     base,
			CertPath: certPath,
			KeyPath:  keyPath,
		}
		if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && len(cert.Certificate) > 0 {
			if leaf, err := parseLeaf(cert.Certificate[0]); err == nil {
				entry.NotBefore = leaf.NotBefore.Unix()
				entry.NotAfter = leaf.NotAfter.Unix()
				entry.Subject = leaf.Subject.String()
				dns := make([]any, 0, len(leaf.DNSNames))
				for _, d := range leaf.DNSNames {
					dns = append(dns, d)
				}
				entry.DnsNames = dns
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// Save writes a cert + key pair atomically. If validate is true (default),
// the pair is checked with tls.X509KeyPair before touching disk so we don't
// install garbage that would break the panel's HTTPS listener at next start.
func (s *CertService) Save(name, certPEM, keyPEM string) (CertEntry, error) {
	if !validCertName(name) {
		return CertEntry{}, ErrCertInvalidName
	}
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return CertEntry{}, fmt.Errorf("%w: %v", ErrCertParseFailed, err)
	}
	dir := s.CertDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return CertEntry{}, err
	}
	certPath := filepath.Join(dir, name+".cer")
	keyPath := filepath.Join(dir, name+".key")
	if err := writeAtomic(certPath, []byte(certPEM), 0o600); err != nil {
		return CertEntry{}, err
	}
	if err := writeAtomic(keyPath, []byte(keyPEM), 0o600); err != nil {
		return CertEntry{}, err
	}
	return CertEntry{
		Name:      name,
		CertPath:  certPath,
		KeyPath:   keyPath,
		NotBefore: time.Now().Unix(),
	}, nil
}

func (s *CertService) Delete(name string) error {
	if !validCertName(name) {
		return ErrCertInvalidName
	}
	dir := s.CertDir()
	for _, ext := range []string{".cer", ".key"} {
		_ = os.Remove(filepath.Join(dir, name+ext))
	}
	return nil
}

func validCertName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || r == 0 || r == '.' && len(name) == 1 {
			return false
		}
	}
	if strings.Contains(name, "..") {
		return false
	}
	return true
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
