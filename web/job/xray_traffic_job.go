package job

import (
	"time"

	"nexcore-x-ui/logger"
	"nexcore-x-ui/web/service"
)

type XrayTrafficJob struct {
	xrayService          service.XrayService
	inboundService       service.InboundService
	clientTrafficService service.ClientTrafficService
}

func NewXrayTrafficJob() *XrayTrafficJob {
	return new(XrayTrafficJob)
}

func (j *XrayTrafficJob) Run() {
	if !j.xrayService.IsXrayRunning() {
		return
	}
	traffics, err := j.xrayService.GetXrayTraffic()
	if err != nil {
		logger.Warning("get xray traffic failed:", err)
		return
	}
	// inbound 级聚合(老路径,保留兼容)
	if err := j.inboundService.AddTraffic(traffics); err != nil {
		logger.Warning("add inbound traffic failed:", err)
	}
	// v2.0.2:user 级流量 delta 当在线心跳。access.log 只在连接建立那一刻
	// 写"accepted"行,长连接(xtls-rprx-vision 看视频)建立后不再产生新行,
	// 单纯 TTL 过期就显示离线。这里用流量在跑当依据,只要 user 级 delta>0
	// 就刷新该 email 已知 IP 的 lastSeen,让长连接保持在线状态。
	onlineSvc := service.GetOnlineIPService()
	for _, t := range traffics {
		if t.IsInbound || t.Tag == "" {
			continue
		}
		if t.Up+t.Down > 0 {
			onlineSvc.TouchEmail(t.Tag)
		}
	}
	// v1.1.0:user 级聚合,顺手到期/触顶检查 → 自动 disable。
	// 被 disable 的 email 列表非空时让 xray reload,把客户踢下线。
	disabled, err := j.clientTrafficService.AddTrafficByEmail(traffics, time.Now().UnixMilli())
	if err != nil {
		logger.Warning("add client traffic failed:", err)
		return
	}
	if len(disabled) > 0 {
		logger.Infof("auto-disabled clients (over quota / expired): %v", disabled)
		j.xrayService.SetToNeedRestart()
	}
}
