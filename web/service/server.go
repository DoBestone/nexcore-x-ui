package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"
	"nexcore-x-ui/config"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/util/sys"
	"nexcore-x-ui/xray"
)

type ProcessState string

const (
	Running ProcessState = "running"
	Stop    ProcessState = "stop"
	Error   ProcessState = "error"
)

type Status struct {
	T   time.Time `json:"-"`
	Cpu float64   `json:"cpu"`
	Mem struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"mem"`
	Swap struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"swap"`
	Disk struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"disk"`
	Xray struct {
		State    ProcessState `json:"state"`
		ErrorMsg string       `json:"errorMsg"`
		Version  string       `json:"version"`
	} `json:"xray"`
	Uptime   uint64    `json:"uptime"`
	Loads    []float64 `json:"loads"`
	TcpCount int       `json:"tcpCount"`
	UdpCount int       `json:"udpCount"`
	NetIO    struct {
		Up   uint64 `json:"up"`
		Down uint64 `json:"down"`
	} `json:"netIO"`
	NetTraffic struct {
		Sent uint64 `json:"sent"`
		Recv uint64 `json:"recv"`
	} `json:"netTraffic"`
	// PanelVersion is the embedded build-time version string
	// (config/version). Surfacing it on /server/status lets the SPA show
	// it in the side-bar without a separate endpoint and keeps every
	// release from having to hand-edit Layout.vue's version label.
	PanelVersion string `json:"panelVersion"`
}

type Release struct {
	TagName string `json:"tag_name"`
}

type ServerService struct {
	xrayService XrayService
}

func (s *ServerService) GetStatus(lastStatus *Status) *Status {
	now := time.Now()
	status := &Status{
		T:            now,
		PanelVersion: config.GetVersion(),
	}

	percents, err := cpu.Percent(0, false)
	if err != nil {
		logger.Warning("get cpu percent failed:", err)
	} else {
		status.Cpu = percents[0]
	}

	upTime, err := host.Uptime()
	if err != nil {
		logger.Warning("get uptime failed:", err)
	} else {
		status.Uptime = upTime
	}

	memInfo, err := mem.VirtualMemory()
	if err != nil {
		logger.Warning("get virtual memory failed:", err)
	} else {
		status.Mem.Current = memInfo.Used
		status.Mem.Total = memInfo.Total
	}

	swapInfo, err := mem.SwapMemory()
	if err != nil {
		logger.Warning("get swap memory failed:", err)
	} else {
		status.Swap.Current = swapInfo.Used
		status.Swap.Total = swapInfo.Total
	}

	distInfo, err := disk.Usage("/")
	if err != nil {
		logger.Warning("get dist usage failed:", err)
	} else {
		status.Disk.Current = distInfo.Used
		status.Disk.Total = distInfo.Total
	}

	avgState, err := load.Avg()
	if err != nil {
		logger.Warning("get load avg failed:", err)
	} else {
		status.Loads = []float64{avgState.Load1, avgState.Load5, avgState.Load15}
	}

	ioStats, err := net.IOCounters(false)
	if err != nil {
		logger.Warning("get io counters failed:", err)
	} else if len(ioStats) > 0 {
		ioStat := ioStats[0]
		status.NetTraffic.Sent = ioStat.BytesSent
		status.NetTraffic.Recv = ioStat.BytesRecv

		// 速率 = 累计字节差 / 时间差。需要前一拍样本。
		// 排除两种坏情况:
		//   1. lastStatus == nil:首次采样,无差可减,速率留 0(累计字节
		//      仍正确填充 NetTraffic.Sent/Recv,前端可自己算)。
		//   2. lastStatus 太陈旧(> 5min):用它算出的"几十分钟均值"跟
		//      "瞬时速率"已经脱节,treat 等同未采样。原本 30s 太严格 ——
		//      v1 API 里 lastStatus 仅靠"上次请求"来更新,业务系统轮询
		//      间隔做 1–2 分钟很常见,每次都被判 stale → 永远 0。现在
		//      v1 也起了 @every 5s ticker 主动刷新(register 里挂 cron),
		//      所以 lastStatus.T 正常就 ≤ 5s;5min 是给"ticker 暂停 /
		//      慢轮询者落到 cache 之外"的极端边角再加一层兜底。
		//   3. 计数回卷(uint64 下溢):虚机迁移、网卡 down/up、计数器
		//      复位都会让 current < last,直接相减会下溢成天文数字。
		//      检测到反转就跳过这一拍,等下一拍再算。
		if lastStatus != nil {
			duration := now.Sub(lastStatus.T)
			seconds := duration.Seconds()
			if seconds > 0 && seconds <= 300 &&
				status.NetTraffic.Sent >= lastStatus.NetTraffic.Sent &&
				status.NetTraffic.Recv >= lastStatus.NetTraffic.Recv {
				status.NetIO.Up = uint64(float64(status.NetTraffic.Sent-lastStatus.NetTraffic.Sent) / seconds)
				status.NetIO.Down = uint64(float64(status.NetTraffic.Recv-lastStatus.NetTraffic.Recv) / seconds)
			}
		}
	} else {
		logger.Warning("can not find io counters")
	}

	status.TcpCount, err = sys.GetTCPCount()
	if err != nil {
		logger.Warning("get tcp connections failed:", err)
	}

	status.UdpCount, err = sys.GetUDPCount()
	if err != nil {
		logger.Warning("get udp connections failed:", err)
	}

	if s.xrayService.IsXrayRunning() {
		status.Xray.State = Running
		status.Xray.ErrorMsg = ""
	} else {
		err := s.xrayService.GetXrayErr()
		if err != nil {
			status.Xray.State = Error
		} else {
			status.Xray.State = Stop
		}
		status.Xray.ErrorMsg = s.xrayService.GetXrayResult()
	}
	status.Xray.Version = s.xrayService.GetXrayVersion()

	return status
}

func (s *ServerService) GetXrayVersions() ([]string, error) {
	url := "https://api.github.com/repos/XTLS/Xray-core/releases"
	// 30s ceiling: api.github.com is normally <500ms; anything past 30s
	// is GitHub broken / network broken, in which case there is no point
	// holding the request goroutine for the panel's WriteTimeout (120s).
	// Without this, a soft outage of api.github.com made the whole "新版本
	// 检查" UI hang for two minutes per click.
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	buffer := bytes.NewBuffer(make([]byte, 8192))
	buffer.Reset()
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return nil, err
	}

	releases := make([]Release, 0)
	err = json.Unmarshal(buffer.Bytes(), &releases)
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		versions = append(versions, release.TagName)
	}
	return versions, nil
}

func (s *ServerService) downloadXRay(version string) (string, error) {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch osName {
	case "darwin":
		osName = "macos"
	}

	switch arch {
	case "amd64":
		arch = "64"
	case "arm64":
		arch = "arm64-v8a"
	}

	fileName := fmt.Sprintf("Xray-%s-%s.zip", osName, arch)
	url := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/%s", version, fileName)
	// 5min ceiling: a release zip is ~30MB; on a 1Mbps link that's ~4min.
	// We don't want to be unbounded (a stalled GitHub CDN holds the panel's
	// upgrade-in-progress state forever) but the floor has to clear normal
	// downloads on a slow VPS.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	os.Remove(fileName)
	// 0o600: only the panel user can read the downloaded archive while it's
	// on disk. Default os.Create uses 0o666 which (after typical umask 022)
	// is world-readable — on a shared host a local user could read the
	// fresh download, and worse, race against the extraction by overwriting
	// the still-open file before we hash it. The archive is deleted right
	// after extraction (defer above), but the window is enough for an
	// attacker with shell access to swap in a malicious payload.
	file, err := os.OpenFile(fileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", err
	}

	return fileName, nil
}

func (s *ServerService) UpdateXray(version string) error {
	zipFileName, err := s.downloadXRay(version)
	if err != nil {
		return err
	}

	zipFile, err := os.Open(zipFileName)
	if err != nil {
		return err
	}
	defer func() {
		zipFile.Close()
		os.Remove(zipFileName)
	}()

	stat, err := zipFile.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(zipFile, stat.Size())
	if err != nil {
		return err
	}

	s.xrayService.StopXray()
	defer func() {
		err := s.xrayService.RestartXray(true)
		if err != nil {
			logger.Error("start xray failed:", err)
		}
	}()

	copyZipFile := func(zipName string, fileName string) error {
		zipFile, err := reader.Open(zipName)
		if err != nil {
			return err
		}
		os.Remove(fileName)
		// 0o700 (owner rwx only) instead of fs.ModePerm (0o777). The
		// extracted artifacts are: the xray executable (must be +x for
		// the panel user) and the geoip/geosite data files. On a shared
		// host, 0o777 lets any local user overwrite the xray binary
		// between this write and the panel's RestartXray call —
		// privilege escalation if the panel runs as root. xray/process.go
		// preflightBinary still rejects world-writable binaries, but it
		// is cheaper to never write them world-writable in the first
		// place than to rely on a post-hoc TOCTOU check.
		file, err := os.OpenFile(fileName, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(file, zipFile)
		return err
	}

	err = copyZipFile("xray", xray.GetBinaryPath())
	if err != nil {
		return err
	}
	err = copyZipFile("geosite.dat", xray.GetGeositePath())
	if err != nil {
		return err
	}
	err = copyZipFile("geoip.dat", xray.GetGeoipPath())
	if err != nil {
		return err
	}

	return nil

}
