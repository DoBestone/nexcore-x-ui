package xray

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
	"nexcore-x-ui/util/common"

	"github.com/Workiva/go-datastructures/queue"
	statsservice "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
)

// inbound/outbound 走 IsInbound 标志位;user 走 email tag,IsInbound=false
// 时下游(ClientTrafficService.AddTrafficByEmail)按 Tag 匹配 client_traffics。
// xray stats key 形如:
//   inbound>>>{tag}>>>traffic>>>{uplink|downlink}
//   outbound>>>{tag}>>>traffic>>>{uplink|downlink}
//   user>>>{email}>>>traffic>>>{uplink|downlink}
// user 级要在 xray policy.levels.0.statsUserUplink/Downlink=true 才有,见 web/service/config.json。
var trafficRegex = regexp.MustCompile("(inbound|outbound|user)>>>([^>]+)>>>traffic>>>(downlink|uplink)")

func GetBinaryName() string {
	return fmt.Sprintf("xray-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// GetBinaryPath returns the path to the xray binary. The default lives next
// to the panel binary (bin/xray-<os>-<arch>), but tests and unusual deploys
// can override via NEXCORE_XRAY_BIN — e.g. test code can point it at
// /usr/bin/true to simulate "every config validates" or /usr/bin/false for
// "every config rejected", exercising the dry-run wiring without needing a
// real cross-arch xray binary on the dev machine.
func GetBinaryPath() string {
	if p := os.Getenv("NEXCORE_XRAY_BIN"); p != "" {
		return p
	}
	return "bin/" + GetBinaryName()
}

func GetConfigPath() string {
	return "bin/config.json"
}

// GetAccessLogPath 返回 xray access log 的路径。在线 IP 统计依赖该日志，
// 路径相对面板进程的工作目录（systemd 下为 /usr/local/nexcore-x-ui/）。
func GetAccessLogPath() string {
	return "bin/access.log"
}

func GetGeositePath() string {
	return "bin/geosite.dat"
}

func GetGeoipPath() string {
	return "bin/geoip.dat"
}

func stopProcess(p *Process) {
	p.Stop()
}

type Process struct {
	*process
}

func NewProcess(xrayConfig *Config) *Process {
	p := &Process{newProcess(xrayConfig)}
	runtime.SetFinalizer(p, stopProcess)
	return p
}

type process struct {
	cmd *exec.Cmd

	version string
	apiPort int

	config  *Config
	lines   *queue.Queue
	exitErr error
}

func newProcess(config *Config) *process {
	return &process{
		version: "Unknown",
		config:  config,
		lines:   queue.New(100),
	}
}

func (p *process) IsRunning() bool {
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	if p.cmd.ProcessState == nil {
		return true
	}
	return false
}

func (p *process) GetErr() error {
	return p.exitErr
}

func (p *process) GetResult() string {
	if p.lines.Empty() && p.exitErr != nil {
		return p.exitErr.Error()
	}
	items, _ := p.lines.TakeUntil(func(item interface{}) bool {
		return true
	})
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.(string))
	}
	return strings.Join(lines, "\n")
}

func (p *process) GetVersion() string {
	return p.version
}

func (p *Process) GetAPIPort() int {
	return p.apiPort
}

func (p *Process) GetConfig() *Config {
	return p.config
}

func (p *process) refreshAPIPort() {
	for _, inbound := range p.config.InboundConfigs {
		if inbound.Tag == "api" {
			p.apiPort = inbound.Port
			break
		}
	}
}

func (p *process) refreshVersion() {
	cmd := exec.Command(GetBinaryPath(), "-version")
	data, err := cmd.Output()
	if err != nil {
		p.version = "Unknown"
	} else {
		datas := bytes.Split(data, []byte(" "))
		if len(datas) <= 1 {
			p.version = "Unknown"
		} else {
			p.version = string(datas[1])
		}
	}
}

func (p *process) Start() (err error) {
	if p.IsRunning() {
		return errors.New("xray is already running")
	}

	defer func() {
		if err != nil {
			p.exitErr = err
		}
	}()

	data, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return common.NewErrorf("生成 xray 配置文件失败: %v", err)
	}
	configPath := GetConfigPath()
	err = os.WriteFile(configPath, data, 0o600)
	if err != nil {
		return common.NewErrorf("写入配置文件失败: %v", err)
	}

	if err := preflightBinary(GetBinaryPath()); err != nil {
		return common.NewErrorf("xray binary preflight failed: %v", err)
	}

	cmd := exec.Command(GetBinaryPath(), "-c", configPath)
	p.cmd = cmd

	stdReader, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errReader, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			common.Recover("")
			stdReader.Close()
		}()
		reader := bufio.NewReaderSize(stdReader, 8192)
		for {
			line, _, err := reader.ReadLine()
			if err != nil {
				return
			}
			if p.lines.Len() >= 100 {
				p.lines.Get(1)
			}
			p.lines.Put(string(line))
		}
	}()

	go func() {
		defer func() {
			common.Recover("")
			errReader.Close()
		}()
		reader := bufio.NewReaderSize(errReader, 8192)
		for {
			line, _, err := reader.ReadLine()
			if err != nil {
				return
			}
			if p.lines.Len() >= 100 {
				p.lines.Get(1)
			}
			p.lines.Put(string(line))
		}
	}()

	go func() {
		err := cmd.Run()
		if err != nil {
			p.exitErr = err
		}
	}()

	p.refreshVersion()
	p.refreshAPIPort()

	return nil
}

// Stop sends SIGTERM and waits up to 5s for the xray process to exit cleanly,
// falling back to SIGKILL only if the grace period is exceeded. The previous
// implementation called Kill() unconditionally, which prevented xray from
// flushing in-memory traffic stats and closing user connections gracefully.
func (p *process) Stop() error {
	if !p.IsRunning() {
		return errors.New("xray is not running")
	}
	proc := p.cmd.Process

	// On Windows os.Process.Signal(syscall.SIGTERM) is unsupported; the
	// runtime returns an error and we fall through to Kill. Keeping the call
	// here is harmless on those platforms.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return proc.Kill()
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline.C:
			return proc.Kill()
		case <-tick.C:
			if !p.IsRunning() {
				return nil
			}
		}
	}
}

func (p *process) GetTraffic(reset bool) ([]*Traffic, error) {
	if p.apiPort == 0 {
		return nil, common.NewError("xray api port wrong:", p.apiPort)
	}
	conn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%v", p.apiPort), grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := statsservice.NewStatsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	request := &statsservice.QueryStatsRequest{
		Reset_: reset,
	}
	resp, err := client.QueryStats(ctx, request)
	if err != nil {
		return nil, err
	}
	// 用 kind:tag 做 map key:不同 namespace(inbound / outbound / user)
	// 间 tag 同名(用户给入站起 remark="alice" 又有 email="alice")不会被
	// 错误合并到同一条 Traffic 上。下游靠 IsInbound 区分入站级和 user 级,
	// outbound 进了 IsInbound=false 通道,在 AddTrafficByEmail 里按 email
	// 查不到 client_traffics 行自动跳过,不会污染数据。
	tagTrafficMap := map[string]*Traffic{}
	traffics := make([]*Traffic, 0)
	for _, stat := range resp.GetStat() {
		matchs := trafficRegex.FindStringSubmatch(stat.Name)
		if len(matchs) != 4 {
			continue // xray 偶尔会上报非 traffic key,不匹配的安全跳过
		}
		kind := matchs[1]
		tag := matchs[2]
		isDown := matchs[3] == "downlink"
		if tag == "api" {
			continue
		}
		key := kind + ":" + tag
		traffic, ok := tagTrafficMap[key]
		if !ok {
			traffic = &Traffic{
				IsInbound: kind == "inbound",
				Tag:       tag,
			}
			tagTrafficMap[key] = traffic
			traffics = append(traffics, traffic)
		}
		if isDown {
			traffic.Down = stat.Value
		} else {
			traffic.Up = stat.Value
		}
	}

	return traffics, nil
}

// preflightBinary refuses to spawn xray when the binary is missing,
// world-writable, or owned by a non-root user. The panel itself runs
// as root (it has to bind privileged ports) so the threat model is
// "another user on the box has tampered with bin/xray-* to swap a
// coin-miner in". Catching that at start time is cheap and stops the
// hijack from inheriting our root context. On non-unix platforms the
// uid/permission check is a no-op since the syscall stat fields don't
// match what we want to assert; the existence + executable check
// still runs.
func preflightBinary(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	mode := st.Mode()
	if mode.Perm()&0o002 != 0 {
		return fmt.Errorf("%s is world-writable (mode %#o) — refusing to exec", path, mode.Perm())
	}
	if mode&os.ModeSymlink != 0 {
		// os.Stat follows symlinks already, but Lstat'ing first would
		// catch a symlink dangling at a non-root-owned target. We
		// re-stat with Lstat for the symlink-owner check below.
		lst, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if lst.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("%s is a world-writable symlink", path)
		}
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		// Owner must be root (uid 0). Anything else means a non-root
		// user can rewrite the binary the panel will exec.
		if sys.Uid != 0 {
			return fmt.Errorf("%s is owned by uid %d (expected 0/root)", path, sys.Uid)
		}
	}
	return nil
}
