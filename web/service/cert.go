package service

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// validCertNameRe is a strict allow-list: letters, digits, dot, hyphen,
// underscore, between 1 and 64 chars. Anything else is rejected outright.
// This is tighter than the previous "no /, no \, no ..." check — that
// approach has historically allowed e.g. NUL bytes, leading dots, and
// other surprises on different filesystems.
var validCertNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

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
	// AcmeManaged signals the panel UI that this cert came from the
	// shipped acme.sh integration (issuance + auto-renewal). Detection
	// is heuristic: acme.sh leaves a peer <name>.fullchain.cer file
	// next to the cer/key pair when it installs via --fullchain-file.
	AcmeManaged bool `json:"acmeManaged"`
	// PanelBound is true when this cert is the one referenced by the
	// webCertFile / webKeyFile setting and is therefore what the panel
	// actually serves over HTTPS. Useful for the UI to badge "in use".
	PanelBound bool `json:"panelBound"`
}

// CertDir returns the directory backing /api/v1/certs. It is configurable so
// tests can use a temp dir; production always uses /root/cert (matching the
// path the bundled acme.sh integration installs into).
func (s *CertService) CertDir() string {
	if v := os.Getenv("NEXCORE_CERT_DIR"); v != "" {
		return v
	}
	return certBaseDir
}

// List returns metadata for every <name>.cer / <name>.key pair found.
// The fullchain peer file is filtered out so the UI doesn't see a
// phantom "<domain>.fullchain" entry; if its presence indicates an
// acme.sh-managed cert we just flip AcmeManaged on the corresponding
// row instead.
func (s *CertService) List() ([]CertEntry, error) {
	dir := s.CertDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []CertEntry{}, nil
		}
		return nil, err
	}
	// Index of `<base>.fullchain.cer` presence — used to mark acme.sh
	// rows. acme.sh writes both <domain>.cer and <domain>.fullchain.cer
	// when called with --fullchain-file (see nexcore-x-ui.sh's
	// install_cert).
	fullchain := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".fullchain.cer") {
			fullchain[strings.TrimSuffix(n, ".fullchain.cer")] = true
		}
	}
	// Peek at panel binding to flag PanelBound. Errors fall through
	// silently — the cert listing should still work without a setting
	// service hooked up.
	boundCertPath, _ := (&SettingService{}).getString("webCertFile")
	boundKeyPath, _ := (&SettingService{}).getString("webKeyFile")
	out := make([]CertEntry, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".cer") || strings.HasSuffix(name, ".fullchain.cer") {
			continue
		}
		base := strings.TrimSuffix(name, ".cer")
		certPath := filepath.Join(dir, name)
		keyPath := filepath.Join(dir, base+".key")
		if _, err := os.Stat(keyPath); err != nil {
			continue
		}
		entry := CertEntry{
			Name:        base,
			CertPath:    certPath,
			KeyPath:     keyPath,
			AcmeManaged: fullchain[base],
			PanelBound:  certPath == boundCertPath && keyPath == boundKeyPath,
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

// Save writes a cert + key pair atomically. The pair is checked with
// tls.X509KeyPair before touching disk so we don't install garbage
// that would break the panel's HTTPS listener at next start.
func (s *CertService) Save(name, certPEM, keyPEM string) (CertEntry, error) {
	certPath, err := s.resolveCertPath(name, ".cer")
	if err != nil {
		return CertEntry{}, err
	}
	keyPath, err := s.resolveCertPath(name, ".key")
	if err != nil {
		return CertEntry{}, err
	}
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return CertEntry{}, fmt.Errorf("%w: %v", ErrCertParseFailed, err)
	}
	if err := os.MkdirAll(s.CertDir(), 0o700); err != nil {
		return CertEntry{}, err
	}
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
	for _, ext := range []string{".cer", ".key"} {
		p, err := s.resolveCertPath(name, ext)
		if err != nil {
			return err
		}
		_ = os.Remove(p)
	}
	return nil
}

func validCertName(name string) bool {
	if !validCertNameRe.MatchString(name) {
		return false
	}
	// Defense in depth against filesystems that do special-case dot
	// names. Only a single leading or trailing dot would actually slip
	// through the regex (since it allows dots), but reject those too.
	if name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return false
	}
	return true
}

// resolveCertPath joins dir with <name>.<ext> and verifies the result
// stays inside dir even after symlink/cleaning. validCertName already
// blocks slashes and ".." but the final filepath.EvalSymlinks check
// catches the case where dir itself contains a symlink an attacker
// pointed at /etc.
func (s *CertService) resolveCertPath(name, ext string) (string, error) {
	if !validCertName(name) {
		return "", ErrCertInvalidName
	}
	dir := s.CertDir()
	cleaned := filepath.Clean(filepath.Join(dir, name+ext))
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(cleaned+string(os.PathSeparator), dirAbs+string(os.PathSeparator)) && cleaned != dirAbs {
		return "", ErrCertInvalidName
	}
	return cleaned, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
