package service

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"nexcore-x-ui/config"
)

// UpdateService self-updates the panel by downloading a release tarball
// from GitHub and replacing files in /usr/local/x-ui (or wherever the
// binary lives). It expects releases produced by the GitHub Actions
// workflow shipped with this repo: x-ui-linux-<arch>.tar.gz containing
// a top-level x-ui/ directory with the binary, x-ui.sh, x-ui.service,
// bin/, and so on.
type UpdateService struct{}

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

	asset := s.pickAsset(r)
	if asset == "" {
		return nil, fmt.Errorf("no asset for arch %s in release %s", runtime.GOARCH, r.TagName)
	}

	// Download.
	tmpDir, err := os.MkdirTemp("", "x-ui-update-*")
	if err != nil {
		return nil, err
	}
	tarballPath := filepath.Join(tmpDir, "x-ui.tar.gz")
	if err := downloadFile(asset, tarballPath); err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}

	// Extract.
	extractedRoot := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractedRoot, 0o755); err != nil {
		return nil, err
	}
	if err := extractTarGz(tarballPath, extractedRoot); err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	// The expected layout is `x-ui/...` inside the tarball.
	srcDir := filepath.Join(extractedRoot, "x-ui")
	if _, err := os.Stat(srcDir); err != nil {
		// Some releases drop files at the root — try that.
		srcDir = extractedRoot
	}

	// Locate the running binary so we know where to install the new one.
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate self: %w", err)
	}
	installRoot := filepath.Dir(exe)

	// Atomic swap: rename existing binary to .old, install new.
	binSrc := filepath.Join(srcDir, "x-ui")
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
	for _, sub := range []string{"bin", "x-ui.sh", "x-ui.service"} {
		_ = copyTree(filepath.Join(srcDir, sub), filepath.Join(installRoot, sub))
	}

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
	client := &http.Client{Timeout: 20 * time.Second}
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

func (s *UpdateService) pickAsset(r *ReleaseInfo) string {
	want := fmt.Sprintf("x-ui-linux-%s.tar.gz", runtime.GOARCH)
	for _, a := range r.Assets {
		if a.Name == want {
			return a.BrowserDownloadURL
		}
	}
	// fallback: any tarball
	for _, a := range r.Assets {
		if strings.HasSuffix(a.Name, ".tar.gz") {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// repoCoordinates returns the GitHub owner/repo used for self-update. The
// defaults point at the canonical NexCore x-ui repository; operators running
// a fork override via NEXCORE_GH_OWNER / NEXCORE_GH_REPO env vars on the
// systemd unit.
func repoCoordinates() (owner, repo string) {
	owner = os.Getenv("NEXCORE_GH_OWNER")
	if owner == "" {
		owner = "DoBestone"
	}
	repo = os.Getenv("NEXCORE_GH_REPO")
	if repo == "" {
		repo = "nexcore-x-ui"
	}
	return
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
	_, err = io.Copy(out, resp.Body)
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
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		// Block path traversal.
		clean := filepath.Clean(h.Name)
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
			continue
		}
		out := filepath.Join(dst, clean)
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
			if _, err := io.Copy(w, tr); err != nil {
				w.Close()
				return err
			}
			w.Close()
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
