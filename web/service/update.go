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
	"nexcore-x-ui/logger"
)

// maxTarballSize caps the download to avoid a malicious release filling
// /tmp. Real binaries are ~30MB; 256MB is a generous ceiling.
const maxTarballSize = 256 * 1024 * 1024

// maxChecksumsSize caps checksums.txt at 64KB. The real file is ~300B.
const maxChecksumsSize = 64 * 1024

// ErrChecksumMismatch is returned when the downloaded tarball's SHA256
// doesn't match the value in checksums.txt for that asset.
var ErrChecksumMismatch = errors.New("update: tarball checksum mismatch")

// ErrChecksumMissing is returned when the release or doesn't contain an
// entry for the asset we downloaded. We fail closed: a release without
// checksums is treated as untrusted.
var ErrChecksumMissing = errors.New("update: release has no checksums.txt entry for this asset")

// checksumsClient is used for the small (≤64KB) checksums.txt download.
// Bounded so a stalled GitHub CDN can't park an update job for hours.
var checksumsClient = &http.Client{Timeout: 30 * time.Second}

// artifactClient is used for the actual release tarball download (≤256MB).
// 10min ceiling clears legitimate downloads on a slow link while still
// failing fast if the connection wedges. The earlier API metadata GET (
// updateMetaClient) uses a tighter 30s budget.
var artifactClient = &http.Client{Timeout: 10 * time.Minute}

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

	// progress 字段记录当前 ApplyLatest 的实时阶段,前端轮询展示。
	// applyMu 同时充当"全局只允许一个并发升级"的互斥锁。
	progressMu sync.Mutex
	progress   ApplyProgress
	applyMu    sync.Mutex
}

// ApplyProgress 描述一次 ApplyLatest 的实时状态。state 字段是一个有限状态
// 机:idle → downloading → verifying → extracting → installing → restarting
// → done(任意阶段失败转 error)。Message 给前端做人话提示,不要直接
// 翻译错误码 — 后端给的 message 就是要直接吐到 UI 的中文。
type ApplyProgress struct {
	State        string `json:"state"`
	Message      string `json:"message"`
	TargetTag    string `json:"targetTag,omitempty"`
	StartedAt    int64  `json:"startedAt,omitempty"`
	UpdatedAt    int64  `json:"updatedAt,omitempty"`
	Error        string `json:"error,omitempty"`
	CurrentVer   string `json:"currentVersion,omitempty"`
}

const (
	ApplyStateIdle        = "idle"
	ApplyStateDownloading = "downloading"
	ApplyStateVerifying   = "verifying"
	ApplyStateExtracting  = "extracting"
	ApplyStateInstalling  = "installing"
	ApplyStateRestarting  = "restarting"
	ApplyStateDone        = "done"
	ApplyStateError       = "error"
)

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
// the binary in place, and re-execs into the new binary. Returns the new
// version on success. The replace-and-restart sequence is:
//
//  1. download tarball to /tmp
//  2. verify SHA256 against checksums.txt
//  3. extract to /tmp/x-ui-update-<ts>/
//  4. rename current binary to <path>.old
//  5. install new binary atomically
//  6. syscall.Exec(newBinary) → process image is replaced in-place
//
// 关键修复(v2.5.x):此前 step 6 只发了 SIGHUP,main.go 的 SIGHUP handler
// 会重建一个 web.Server 重新 Start,但 **运行的还是旧 Go 进程**。也就是说
// 文件已经换了,内存里跑的代码没换 — 用户看到「升级成功」但版本号没变。
// 现在改为 syscall.Exec 把整个进程映像替换成新二进制,PID 不变,systemd
// MAINPID 跟踪不动,新的 main() 冷启动加载新代码。
//
// Failure between steps 4 and 5 falls back to <path>.old so we never end
// up with no binary at all.
func (s *UpdateService) ApplyLatest(targetVersion string) (*UpdateCheck, error) {
	// 互斥:同一时刻只允许一次升级。两个浏览器同时点"立即更新"不会
	// 半路打架(后到的会拿不到锁直接报错给用户)。
	if !s.applyMu.TryLock() {
		return nil, errors.New("update: 已有升级正在进行中")
	}
	defer s.applyMu.Unlock()

	s.setProgress(ApplyProgress{
		State:      ApplyStateDownloading,
		Message:    "正在解析 release 信息…",
		TargetTag:  targetVersion,
		StartedAt:  time.Now().Unix(),
		UpdatedAt:  time.Now().Unix(),
		CurrentVer: config.GetVersion(),
	})

	owner, repo := repoCoordinates()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	if targetVersion != "" {
		url = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repo, targetVersion)
	}
	r, err := s.fetchRelease(url)
	if err != nil {
		s.failProgress("查询 release 失败:" + err.Error())
		return nil, err
	}

	assetURL, assetName := s.pickAsset(r)
	if assetURL == "" {
		err := fmt.Errorf("no asset for arch %s in release %s", runtime.GOARCH, r.TagName)
		s.failProgress(err.Error())
		return nil, err
	}

	s.touchProgress(ApplyStateDownloading, fmt.Sprintf("下载 %s …", assetName), r.TagName)

	// Download.
	tmpDir, err := os.MkdirTemp("", "x-ui-update-*")
	if err != nil {
		s.failProgress(err.Error())
		return nil, err
	}
	tarballPath := filepath.Join(tmpDir, "x-ui.tar.gz")
	if err := downloadFile(assetURL, tarballPath); err != nil {
		s.failProgress("下载失败:" + err.Error())
		return nil, fmt.Errorf("download: %w", err)
	}

	// Verify SHA256 against checksums.txt published in the same release.
	// We fail closed: any error here aborts the upgrade with the new
	// binary never installed. checksums.txt is mandatory — a release
	// missing it is treated as untrusted.
	s.touchProgress(ApplyStateVerifying, "校验 SHA256…", r.TagName)
	if err := verifyTarballChecksum(r, assetName, tarballPath); err != nil {
		s.failProgress("校验失败:" + err.Error())
		return nil, fmt.Errorf("verify: %w", err)
	}

	// Extract.
	s.touchProgress(ApplyStateExtracting, "解压压缩包…", r.TagName)
	extractedRoot := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractedRoot, 0o755); err != nil {
		s.failProgress(err.Error())
		return nil, err
	}
	if err := extractTarGz(tarballPath, extractedRoot); err != nil {
		s.failProgress("解压失败:" + err.Error())
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
	s.touchProgress(ApplyStateInstalling, "替换面板二进制…", r.TagName)
	exe, err := os.Executable()
	if err != nil {
		s.failProgress(err.Error())
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
		s.failProgress("压缩包缺少新二进制")
		return nil, fmt.Errorf("new binary missing in tarball: %w", err)
	}
	_ = os.Remove(binBackup)
	if err := os.Rename(binDst, binBackup); err != nil {
		s.failProgress("备份当前二进制失败:" + err.Error())
		return nil, fmt.Errorf("backup current binary: %w", err)
	}
	if err := copyFile(binSrc, binDst, 0o755); err != nil {
		// rollback
		_ = os.Rename(binBackup, binDst)
		s.failProgress("安装新二进制失败:" + err.Error())
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

	// Trigger re-exec — see ApplyLatest doc for why this replaces the old
	// SIGHUP path. 800ms gives the HTTP response time to flush to the
	// caller before the listening socket goes away.
	//
	// 关键:必须先把 xray 子进程 SIGTERM 掉再 exec。xray 是 panel 的子进程,
	// kernel 在 execve 时不会终结子进程 — 不显式 stop 的话,exec 之后老
	// xray 还活着占着 inbound 端口,新 panel main() 启动时再去 spawn xray
	// 直接 EADDRINUSE,UI 看到的就是「xray 报错 + 版本没变」(因为 panel
	// 自己倒是新二进制,但 xray 起不来用户感知就是更新失败)。这是 v2.6.0
	// 在线更新在生产环境第一次实战暴露的 bug,补在 v2.6.2。
	s.touchProgress(ApplyStateRestarting, "升级完成,正在重启面板…", r.TagName)
	go func() {
		time.Sleep(800 * time.Millisecond)

		// XrayService 是无状态零值结构 — 内部走包级 var p *xray.Process
		// 拿到当前运行的 xray handle。Stop() 内置 SIGTERM + 5s grace +
		// SIGKILL fallback,返回时 xray 进程已 reap,端口已释放。
		// "xray is not running" 是预期的良性错误(更新前用户已手动停了
		// xray),其它错误也只能 best-effort 继续 — exec 推进比卡住强。
		xs := XrayService{}
		if err := xs.StopXray(); err != nil && !strings.Contains(err.Error(), "not running") {
			logger.Warning("update: stop xray before re-exec failed:", err)
		}

		if err := reexecSelf(); err != nil {
			// 兜底:syscall.Exec 几乎不会失败(失败往往是新二进制不可执行
			// /被 SELinux 拦了)。先把状态打成 error,再 fallback 到 SIGHUP
			// 让旧版面板继续跑 — 至少 UI 不死。
			logger.Warning("update: re-exec into new binary failed:", err)
			s.failProgress("重启失败:" + err.Error() + "(请用 systemctl restart " + filepath.Base(exe) + " 手动重启)")
			_ = sendSelfSIGHUP()
		}
	}()

	return &UpdateCheck{
		Current:         config.GetVersion(),
		Latest:          r.TagName,
		UpdateAvailable: false,
		Release:         r,
	}, nil
}

// ---------- progress / status accessors ----------

// Progress 返回当前 ApplyLatest 的实时状态。前端轮询调用,1s 间隔。
// 返回值是 snapshot,调用方拿到的对象不会被后续更新覆盖。
func (s *UpdateService) Progress() ApplyProgress {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.progress
}

func (s *UpdateService) setProgress(p ApplyProgress) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.progress = p
}

func (s *UpdateService) touchProgress(state, msg, tag string) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.progress.State = state
	s.progress.Message = msg
	if tag != "" {
		s.progress.TargetTag = tag
	}
	s.progress.UpdatedAt = time.Now().Unix()
	s.progress.Error = ""
}

func (s *UpdateService) failProgress(msg string) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.progress.State = ApplyStateError
	s.progress.Error = msg
	s.progress.Message = msg
	s.progress.UpdatedAt = time.Now().Unix()
}

// ListReleases 返回最近 N 条 GitHub release(给前端「更新日志」页用)。
// limit 取 [1, 50],默认 10。返回的 ReleaseInfo 复用 Body 字段做 markdown 渲染。
func (s *UpdateService) ListReleases(limit int) ([]*ReleaseInfo, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	owner, repo := repoCoordinates()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=%d", owner, repo, limit)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("github api %d: %s", resp.StatusCode, string(body))
	}
	var list []*ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
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

	resp, err := checksumsClient.Get(checksumsURL)
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
	resp, err := artifactClient.Get(url)
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

// sendSelfSIGHUP 给老路径(re-exec 失败兜底)用。SIGHUP 只重建 web.Server,
// 不会换二进制 — 一旦走到这条路,操作员要手动 systemctl restart。
func sendSelfSIGHUP() error {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGHUP)
}

// reexecSelf 用 syscall.Exec 把当前进程映像替换成磁盘上的新二进制。
//
// PID 不变 — systemd MAINPID 跟踪、cgroup、seccomp filter 都跟着走;
// 旧进程的 goroutine、文件描述符(net listener 标了 CLOEXEC,会自动关)
// 都被丢掉。新二进制冷启动 main(),从 sqlite 重新读 setting,重新 bind 端口。
//
// 这是「在线更新真正生效」的关键步骤。Caller 应当在调用前已把
// HTTP response 写出去并 flush(因为 exec 之后 socket 立刻断)。
func reexecSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// os.Args[0] 在 systemd 下通常是绝对路径;但 ExecStart 用相对路径
	// 时会落到 working dir。统一用 exe 替换 argv[0],保证 procfs cmdline
	// 看起来跟前一个进程一致。
	argv := append([]string{exe}, os.Args[1:]...)
	return syscall.Exec(exe, argv, os.Environ())
}
