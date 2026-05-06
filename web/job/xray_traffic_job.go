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
