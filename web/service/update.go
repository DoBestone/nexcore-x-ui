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
	"os/exec"
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

// ApplyLatest 把升级动作派给已经在生产证明过的 update.sh 脚本执行 —
// Go 层只负责"拽脚本下来 + 用 systemd-run 在独立 cgroup 里把脚本启起
// 来",剩下的下载 / SHA256 / 解压 / 文件替换 / systemctl stop+start 全
// 部交还给脚本。
//
// 这条路径**取代了**之前 v2.5.x ~ v2.6.2 在 Go 层 inline 实现的:
// 「下载 tarball → SHA256 → tar 解压 → 备份旧二进制 → 替换 → syscall.Exec」
// 的一整套手写 ~200 行逻辑。改架构的动机:
//
//   - 升级路径**收敛到一条**:install.sh / 操作员手动 / 面板按钮 现在
//     全用同一份 update.sh。免去 "Go 层重新实现的逻辑跟脚本漂移" 的风险
//     —— 这正是 v2.6.0 漏掉 xray 子进程清理(后来 v2.6.2 补)的根因。
//   - **systemd 帮我们处理 cgroup teardown**:transient service 在它自己
//     的 cgroup 里跑,update.sh 调 `systemctl stop nexcore-x-ui` 把当前
//     panel 进程 SIGTERM,KillMode=control-group 默认顺手把 xray 子进程
//     一起干掉。换二进制以后 `systemctl start` 拉起来全新进程 + 全新 xray,
//     端口已经空着,啥都不会撞。
//   - **更新逻辑可以独立迭代**:update.sh 改完推 main 分支立刻生效,不
//     需要等下一次 panel 编译发版。
//
// 关键参数(systemd-run):
//
//   --unit=nexcore-x-ui-updater-<ts>  唯一名,避免连点造成 unit 冲突
//   --collect                         transient unit 退出后自动清理
//   --no-block                        systemd-run 立刻返回,不等脚本跑完
//   service mode (默认,非 --scope)   关键!scope 在调用方 cgroup 内开
//                                     新子组,我们被 stop 时它跟着死;
//                                     service 模式作为 systemd pid 1 的
//                                     子进程,完全独立 cgroup。
func (s *UpdateService) ApplyLatest(targetVersion string) (*UpdateCheck, error) {
	// 互斥:同一时刻只允许一次升级。两个浏览器同时点"立即更新"不会
	// 半路打架(后到的会拿不到锁直接报错给用户)。
	if !s.applyMu.TryLock() {
		return nil, errors.New("update: 已有升级正在进行中")
	}
	// **不要** defer Unlock — 调度成功后几秒内 systemctl stop 会把当前
	// 进程 SIGTERM,锁随进程消失。这里只在错误路径上手动 Unlock,成功
	// 路径让进程死掉就行。defer 会让"调度失败"分支正确释放锁,但同时也
	// 让"调度成功 → 等被 kill"的窗口里别的请求误以为可以再调一次,因为
	// goroutine return 走 defer 立刻 Unlock,reentrancy 风险。
	unlocked := false
	unlock := func() {
		if !unlocked {
			s.applyMu.Unlock()
			unlocked = true
		}
	}

	s.setProgress(ApplyProgress{
		State:      ApplyStateDownloading,
		Message:    "查询 release 元数据…",
		TargetTag:  targetVersion,
		StartedAt:  time.Now().Unix(),
		UpdatedAt:  time.Now().Unix(),
		CurrentVer: config.GetVersion(),
	})

	// 先做 sanity:拉一下 release 元数据确认目标 tag 真存在。比起把无效
	// 版本号一路传给脚本然后让脚本下载 404,这里 fail-fast 一句给用户更
	// 直接的错误。
	owner, repo := repoCoordinates()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	if targetVersion != "" {
		url = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repo, targetVersion)
	}
	r, err := s.fetchRelease(url)
	if err != nil {
		unlock()
		s.failProgress("查询 release 失败:" + err.Error())
		return nil, err
	}

	// 把 update.sh 拽到 install dir 下(systemd unit 的 ReadWritePaths 之一)。
	// **不要写 /tmp** —— PrivateTmp=true 让我们的 /tmp 跟 systemd-run
	// transient service 看到的是隔离 namespace,写到 /tmp 那边读不到。
	exe, err := os.Executable()
	if err != nil {
		unlock()
		s.failProgress("locate self: " + err.Error())
		return nil, fmt.Errorf("locate self: %w", err)
	}
	installRoot := filepath.Dir(exe)
	scriptPath := filepath.Join(installRoot, ".update-dispatch.sh")

	scriptURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/update.sh", owner, repo)
	s.touchProgress(ApplyStateDownloading, "下载 update.sh …", r.TagName)
	if err := downloadFile(scriptURL, scriptPath); err != nil {
		unlock()
		s.failProgress("下载 update.sh 失败:" + err.Error())
		return nil, fmt.Errorf("download update.sh: %w", err)
	}
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		unlock()
		s.failProgress("chmod update.sh 失败:" + err.Error())
		return nil, fmt.Errorf("chmod: %w", err)
	}

	// 调度 transient service。args[0] 是 systemd-run 自己的 flag,后面跟
	// 真正要跑的命令(bash + 脚本路径 + 可选 target 参数)。
	args := []string{
		fmt.Sprintf("--unit=nexcore-x-ui-updater-%d", time.Now().Unix()),
		"--description=nexcore-x-ui in-panel updater",
		"--quiet",
		"--collect",
		"--no-block",
		"bash", scriptPath,
	}
	if r.TagName != "" {
		args = append(args, r.TagName)
	}
	s.touchProgress(ApplyStateRestarting, "已交给后台 update.sh,面板即将重启…", r.TagName)
	if err := exec.Command("systemd-run", args...).Run(); err != nil {
		unlock()
		s.failProgress("调度 systemd-run 失败:" + err.Error())
		logger.Warning("update: systemd-run dispatch failed:", err)
		return nil, fmt.Errorf("dispatch: %w", err)
	}

	// 调度成功 — 锁不释放,等 systemctl stop 把整个进程收掉。从用户视角:
	// HTTP 响应即将发出 → 几秒后面板断连 → 前端轮询 progress 收到 connect
	// refused → 当成"已重启"提示刷新 → 新二进制接住请求。
	logger.Info("update: dispatched update.sh via systemd-run for", r.TagName)
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
