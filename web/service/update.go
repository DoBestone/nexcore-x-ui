package service

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"nexcore-x-ui/config"
)

// maxTarballSize caps the download to avoid a malicious release filling
// /tmp. Real binaries are ~30MB; 256MB is a generous ceiling.
const maxTarballSize = 256 * 1024 * 1024

// maxChecksumsSize caps checksums.txt at 64KB. The real file is ~300B.
const maxChecksumsSize = 64 * 1024

// ErrChecksumMismatch is returned when the downloaded tarball's SHA256
// doesn't match the value in checksums.txt for that asset.
var ErrChecksumMismatch = errors.New("update: tarball checksum mismatch")

// ErrChecksumMissing is returned when checksums.txt is missing from the
// release or doesn't contain an entry for the asset we downloaded. We
// fail closed: a release without checksums is treated as untrusted.
var ErrChecksumMissing = errors.New("update: release has no checksums.txt entry for this asset")

// updateCheckCacheTTL avoids hammering the GitHub API every time the dashboard
// mounts. 5 minutes is long enough to keep panel navigation snappy and short
// enough that a freshly-cut release shows up promptly on the next refresh.
const updateCheckCacheTTL = 5 * time.Minute

// UpdateService self-updates the panel by downloading a release tarball
// from GitHub and replacing files in /usr/local/x-ui (or wherever the
// binary lives). It expects releases produced by the GitHub Actions
// workflow shipped with this repo: x-ui-linux-<arch>.tar.gz containing
// a top-level x-ui/ directory with the binary, x-ui.sh, x-ui.service,
// bin/, and so on.
type UpdateService struct {
	cacheMu    sync.Mutex
	cachedAt   time.Time
	cachedView *UpdateCheck
}

type ReleaseInfo struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

type UpdateCheck struct {
	Current        string       `json:"current"`
	Latest         string       `json:"latest"`
	UpdateAvailable bool        `json:"updateAvailable"`
	Release        *ReleaseInfo `json:"release"`
}

// CheckLatest queries GitHub releases for the latest tag and compares it to
// the running binary version. owner/repo default to the upstream repo; see
// repoCoordinates for env-var overrides.
func (s *UpdateService) CheckLatest() (*UpdateCheck, error) {
	s.cacheMu.Lock()
	if s.cachedView != nil && time.Since(s.cachedAt) < updateCheckCacheTTL {
		v := *s.cachedView
		s.cacheMu.Unlock()
		return &v, nil
	}
	s.cacheMu.Unlock()

	owner, repo := repoCoordinates()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	r, err := s.fetchRelease(url)
	if err != nil {
		return nil, err
	}
	cur := strings.TrimSpace(config.GetVersion())
	out := &UpdateCheck{
		Current: cur,
		Latest:  r.TagName,
		Release: r,
	}
	out.UpdateAvailable = r.TagName != "" && r.TagName != cur && r.TagName != "v"+cur && "v"+r.TagName != cur

	s.cacheMu.Lock()
	view := *out
	s.cachedView = &view
	s.cachedAt = time.Now()
	s.cacheMu.Unlock()
	return out, nil
}

// ApplyLatest downloads the asset matching the running architecture, swaps
// the binary in place, and asks the supervisor to restart us. Returns the
// new version on success. The replace-and-restart sequence is:
//
//  1. download tarball to /tmp
//  2. extract to /tmp/x-ui-update-<ts>/
//  3. rename current binary to <path>.old
//  4. install new binary atomically
//  5. SIGHUP self → main.go reloads the web server (panel session drops)
//
// Failure between steps 3 and 4 falls back to <path>.old so we never end up
// with no binary at all.
func (s *UpdateService) ApplyLatest(targetVersion string) (*UpdateCheck, error) {
	owner, repo := repoCoordinates()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	if targetVersion != "" {
		url = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repo, targetVersion)
	}
	r, err := s.fetchRelease(url)
	if err != nil {
		return nil, err
	}

	assetURL, assetName := s.pickAsset(r)
	if assetURL == "" {
		return nil, fmt.Errorf("no asset for arch %s in release %s", runtime.GOARCH, r.TagName)
	}

	// Download.
	tmpDir, err := os.MkdirTemp("", "x-ui-update-*")
	if err != nil {
		return nil, err
	}
	tarballPath := filepath.Join(tmpDir, "x-ui.tar.gz")
	if err := downloadFile(assetURL, tarballPath); err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}

	// Verify SHA256 against checksums.txt published in the same release.
	// We fail closed: any error here aborts the upgrade with the new
	// binary never installed. checksums.txt is mandatory — a release
	// missing it is treated as untrusted.
	if err := verifyTarballChecksum(r, assetName, tarballPath); err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}

	// Extract.
	extractedRoot := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractedRoot, 0o755); err != nil {
		return nil, err
	}
	if err := extractTarGz(tarballPath, extractedRoot); err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	// The expected layout is `nexcore-x-ui/...` inside the tarball produced
	// by .github/workflows/release.yml. Fall back to the legacy `x-ui/`
	// directory or the archive root for any forks that ship differently.
	srcDir := filepath.Join(extractedRoot, "nexcore-x-ui")
	if _, err := os.Stat(srcDir); err != nil {
		legacy := filepath.Join(extractedRoot, "x-ui")
		if _, err2 := os.Stat(legacy); err2 == nil {
			srcDir = legacy
		} else {
			srcDir = extractedRoot
		}
	}

	// Locate the running binary so we know where to install the new one.
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate self: %w", err)
	}
	installRoot := filepath.Dir(exe)

	// Atomic swap: rename existing binary to .old, install new. The binary
	// name inside the tarball matches the running executable's basename
	// (nexcore-x-ui) — falling back to legacy "x-ui" only for old archives.
	exeBase := filepath.Base(exe)
	binSrc := filepath.Join(srcDir, exeBase)
	if _, err := os.Stat(binSrc); err != nil {
		if alt := filepath.Join(srcDir, "x-ui"); fileExists(alt) {
			binSrc = alt
		}
	}
	binDst := exe
	binBackup := exe + ".old"
	if _, err := os.Stat(binSrc); err != nil {
		return nil, fmt.Errorf("new binary missing in tarball: %w", err)
	}
	_ = os.Remove(binBackup)
	if err := os.Rename(binDst, binBackup); err != nil {
		return nil, fmt.Errorf("backup current binary: %w", err)
	}
	if err := copyFile(binSrc, binDst, 0o755); err != nil {
		// rollback
		_ = os.Rename(binBackup, binDst)
		return nil, fmt.Errorf("install new binary: %w", err)
	}

	// Best-effort: refresh xray binary + scripts. Do not abort on failure
	// because the main panel binary swap already succeeded.
	scripts := []string{"bin", exeBase + ".sh", exeBase + ".service"}
	for _, sub := range scripts {
		src := filepath.Join(srcDir, sub)
		if !fileExists(src) {
			// Legacy fallbacks for forks shipping x-ui.* names.
			if sub == exeBase+".sh" {
				src = filepath.Join(srcDir, "x-ui.sh")
			} else if sub == exeBase+".service" {
				src = filepath.Join(srcDir, "x-ui.service")
			}
		}
		_ = copyTree(src, filepath.Join(installRoot, sub))
	}

	// CI runner builds the tarball as uid 1001; tar / copyTree preserve that.
	// xray.preflightBinary refuses to launch any binary not owned by root,
	// which would loop "restart xray failed: ... owned by uid 1001 ..."
	// every 30s. chown the whole install root to root after the swap so
	// preflight passes on next reload. Best-effort — running as root is
	// the only configuration where chown succeeds; non-root setups won't
	// hit preflight anyway.
	_ = chownRecursiveRoot(installRoot)

	// Remove the .old backup; we trust the new binary now.
	_ = os.Remove(binBackup)
	_ = os.RemoveAll(tmpDir)

	// Trigger panel reload.
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = sendSelfSIGHUP()
	}()

	return &UpdateCheck{
		Current:         config.GetVersion(),
		Latest:          r.TagName,
		UpdateAvailable: false,
		Release:         r,
	}, nil
}

// ---------- helpers ----------

func (s *UpdateService) fetchRelease(url string) (*ReleaseInfo, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Short timeout: dashboard mount fires this; we'd rather show "无法获取"
	// than have the request hang for 20s on a flaky link to api.github.com.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("github api %d: %s", resp.StatusCode, string(body))
	}
	r := &ReleaseInfo{}
	if err := json.NewDecoder(resp.Body).Decode(r); err != nil {
		return nil, err
	}
	return r, nil
}

// pickAsset returns the (download URL, asset filename) for the tarball
// matching the running architecture. The filename is needed to look up
// the SHA256 in checksums.txt.
func (s *UpdateService) pickAsset(r *ReleaseInfo) (url, name string) {
	// Match the workflow's archive name first; fall back to legacy x-ui
	// naming so forks that haven't renamed yet still self-update.
	preferred := []string{
		fmt.Sprintf("nexcore-x-ui-linux-%s.tar.gz", runtime.GOARCH),
		fmt.Sprintf("x-ui-linux-%s.tar.gz", runtime.GOARCH),
	}
	for _, want := range preferred {
		for _, a := range r.Assets {
			if a.Name == want {
				return a.BrowserDownloadURL, a.Name
			}
		}
	}
	// Last resort: an arch-matching tarball under any prefix.
	suffix := fmt.Sprintf("linux-%s.tar.gz", runtime.GOARCH)
	for _, a := range r.Assets {
		if strings.HasSuffix(a.Name, suffix) {
			return a.BrowserDownloadURL, a.Name
		}
	}
	return "", ""
}

// verifyTarballChecksum downloads the release's checksums.txt asset,
// looks up the entry for assetName, and compares it to the SHA256 of the
// already-downloaded tarball at tarballPath. Returns nil only on
// constant-time match.
func verifyTarballChecksum(r *ReleaseInfo, assetName, tarballPath string) error {
	var checksumsURL string
	for _, a := range r.Assets {
		if a.Name == "checksums.txt" {
			checksumsURL = a.BrowserDownloadURL
			break
		}
	}
	if checksumsURL == "" {
		return ErrChecksumMissing
	}

	resp, err := http.Get(checksumsURL)
	if err != nil {
		return fmt.Errorf("fetch checksums.txt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fetch checksums.txt: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumsSize))
	if err != nil {
		return fmt.Errorf("read checksums.txt: %w", err)
	}

	want := lookupChecksum(string(body), assetName)
	if want == "" {
		return ErrChecksumMissing
	}

	got, err := sha256File(tarballPath)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(want)), []byte(got)) != 1 {
		return ErrChecksumMismatch
	}
	return nil
}

// lookupChecksum parses the GNU `sha256sum` output format, lines of the
// form "<hex>  <name>" (two spaces, binary mode is "<hex> *<name>"),
// returning the hex digest for assetName or "" if absent. It tolerates
// different whitespace and the optional "*" mode marker.
func lookupChecksum(body, assetName string) string {
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == assetName {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// chownRecursiveRoot walks `root` and chowns everything to uid 0 / gid 0.
// Called after extracting a release tarball: GitHub Actions builds run as
// uid 1001 and tar preserves that ownership, but xray.preflightBinary
// requires the xray binary to be owned by root. Errors are swallowed —
// running as non-root is fine, in that case preflight is a no-op too.
func chownRecursiveRoot(root string) error {
	return filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Chown(path, 0, 0)
		return nil
	})
}

// updateRepoOwner / updateRepoName control the self-update source. They are
// compile-time constants by default; forks can override at build time via
//
//	go build -ldflags '-X nexcore-x-ui/web/service.updateRepoOwner=foo \
//	                   -X nexcore-x-ui/web/service.updateRepoName=bar'
//
// Earlier versions read NEXCORE_GH_OWNER / NEXCORE_GH_REPO at runtime, but
// that turned the systemd unit's Environment= into a self-update hijack
// surface: anyone able to edit the unit (already root, but a useful step
// for an attacker establishing persistence) could redirect ApplyLatest to
// a release they control. Compile-time only closes that window without
// blocking legitimate fork builds.
var (
	updateRepoOwner = "DoBestone"
	updateRepoName  = "nexcore-x-ui"
)

func repoCoordinates() (owner, repo string) {
	return updateRepoOwner, updateRepoName
}

func downloadFile(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download %d", resp.StatusCode)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	// Cap the download. A truncated body fails the SHA256 check downstream.
	_, err = io.Copy(out, io.LimitReader(resp.Body, maxTarballSize))
	return err
}

func extractTarGz(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	// Resolve dst once so HasPrefix checks work even when the caller
	// passes a non-canonical path.
	dstAbs, err := filepath.Abs(filepath.Clean(dst))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	// totalWritten caps cumulative bytes written across ALL entries in the
	// tarball. The download itself is capped at maxTarballSize, but a
	// gzip-bombed archive can decompress to many times its on-disk size:
	// without a cumulative ceiling each entry independently allowed up to
	// maxTarballSize, so an N-entry malicious tarball could fill /tmp.
	// 256MB total is more than 10× any legitimate panel build's extracted
	// footprint while still fitting on the 1H1G machines we deploy to.
	var totalWritten int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		// Reject absolute paths and any name that doesn't resolve to a
		// path strictly under dst. This catches "/etc/passwd",
		// "../escape", and anything else that filepath.Clean keeps in
		// the parent. We also drop hard links and symbolic links —
		// even an in-tree symlink can be followed during a later write
		// to escape the extraction root.
		clean := filepath.Clean(h.Name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("tar: rejected entry %q (path traversal)", h.Name)
		}
		out := filepath.Join(dstAbs, clean)
		if !strings.HasPrefix(out+string(os.PathSeparator), dstAbs+string(os.PathSeparator)) && out != dstAbs {
			return fmt.Errorf("tar: rejected entry %q (escapes destination)", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			// Per-entry limit prevents one gigantic file from filling /tmp;
			// the cumulative check below enforces the total budget across
			// all entries. We compute remaining BEFORE the copy so a single
			// large entry can still consume up to the leftover budget but
			// not exceed it.
			remaining := maxTarballSize - totalWritten
			if remaining <= 0 {
				w.Close()
				return fmt.Errorf("tar: cumulative size exceeds %d bytes", int64(maxTarballSize))
			}
			n, err := io.Copy(w, io.LimitReader(tr, remaining+1))
			w.Close()
			if err != nil {
				return err
			}
			totalWritten += n
			if totalWritten > maxTarballSize {
				return fmt.Errorf("tar: cumulative size %d exceeds limit %d bytes",
					totalWritten, int64(maxTarballSize))
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("tar: rejected entry %q (symlink/hardlink not allowed)", h.Name)
		default:
			// Skip device/fifo/etc. — release tarballs never contain these.
			continue
		}
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if err := copyTree(s, d); err != nil {
			return err
		}
	}
	return nil
}

// sendSelfSIGHUP is implemented per-platform; on windows it returns an error.
func sendSelfSIGHUP() error {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGHUP)
}
