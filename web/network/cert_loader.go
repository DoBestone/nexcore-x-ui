// Package network — TLS cert hot-reload helper.
//
// CertLoader watches the cert + key file pair on disk and re-parses them
// whenever the mtime of either file changes. The panel's tls.Config
// uses CertLoader.GetCertificate as its callback, so a freshly renewed
// cert from acme.sh (or any external tool) takes effect on the next TLS
// handshake — no `systemctl restart` required, existing connections
// stay alive.
//
// Why not fsnotify: certs typically rotate once every 60-90 days, so
// a 60-second poll is plenty and avoids the cgo / inotify capability
// surface. The check is cheap: two os.Stat calls.
package network

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// CertLoader caches a parsed cert and watches the source files.
type CertLoader struct {
	certPath string
	keyPath  string

	// cert is the most recent successfully-parsed pair. atomic.Pointer
	// so GetCertificate is lock-free on the hot path. nil only between
	// New and the first successful load.
	cert atomic.Pointer[tls.Certificate]

	mu          sync.Mutex
	certModTime time.Time
	keyModTime  time.Time
	stop        chan struct{}
}

// NewCertLoader does an eager first load. If the files don't parse,
// the constructor returns the parse error so the caller can refuse to
// start the listener — better to fail loud at boot than serve plain
// HTTP because the cert silently went bad.
func NewCertLoader(certPath, keyPath string) (*CertLoader, error) {
	if certPath == "" || keyPath == "" {
		return nil, errors.New("cert loader: cert and key paths required")
	}
	l := &CertLoader{
		certPath: certPath,
		keyPath:  keyPath,
		stop:     make(chan struct{}),
	}
	if err := l.reload(); err != nil {
		return nil, err
	}
	go l.watchLoop()
	return l, nil
}

// GetCertificate is the callback wired into tls.Config. It never
// returns nil + nil — caller (the TLS stack) treats that as a misuse.
func (l *CertLoader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := l.cert.Load()
	if c == nil {
		return nil, errors.New("cert loader: no cert loaded yet")
	}
	return c, nil
}

// Close stops the watch goroutine. Safe to call once; subsequent calls
// are no-ops.
func (l *CertLoader) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.stop:
		return // already closed
	default:
		close(l.stop)
	}
}

// reload re-stats both files and, if either changed since last time,
// re-parses the pair and atomically swaps in the new cert.
func (l *CertLoader) reload() error {
	cInfo, err := os.Stat(l.certPath)
	if err != nil {
		return fmt.Errorf("stat cert: %w", err)
	}
	kInfo, err := os.Stat(l.keyPath)
	if err != nil {
		return fmt.Errorf("stat key: %w", err)
	}
	l.mu.Lock()
	if cInfo.ModTime().Equal(l.certModTime) && kInfo.ModTime().Equal(l.keyModTime) && l.cert.Load() != nil {
		l.mu.Unlock()
		return nil // unchanged
	}
	l.mu.Unlock()

	pair, err := tls.LoadX509KeyPair(l.certPath, l.keyPath)
	if err != nil {
		return fmt.Errorf("parse pair: %w", err)
	}
	l.cert.Store(&pair)
	l.mu.Lock()
	l.certModTime = cInfo.ModTime()
	l.keyModTime = kInfo.ModTime()
	l.mu.Unlock()
	return nil
}

// watchLoop polls the cert pair every 60s. A reload error is logged via
// the package-level logger if set, but doesn't tear down the loader —
// we keep serving the old cert until either the new pair is fixable or
// the operator restarts. That's the conservative choice: a half-renewed
// cert (cert.cer updated, key.key not yet) shouldn't blow up TLS until
// the renewal completes.
func (l *CertLoader) watchLoop() {
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-tick.C:
			if err := l.reload(); err != nil {
				logReload(err)
			}
		}
	}
}

// logReload is overridable from web/web.go so the loader can plug into
// the panel's slog without importing the logger package (avoids the
// network → web circular import).
var logReload = func(err error) {}

// SetLogger lets the calling package wire in its logger. Safe to call
// multiple times; the latest setter wins.
func SetLogger(fn func(error)) {
	if fn == nil {
		fn = func(error) {}
	}
	logReload = fn
}
