package service

// 验证 nodeName / 其它公开 setting 字段能不能通过
// patchSettings 那条路径(GetAllSetting → ShouldBindJSON 等价的反序列化
// → UpdateAllSetting → GetAllSetting)正确 round-trip。
//
// 之所以单独 test:用户问"节点名称是否支持 API 修改" — 代码上 NodeName
// 是 entity.AllSetting 的 json tagged 字段、不在 skipIfEmptyKeys 集合,
// 理论上 PATCH /settings 一句就够;但反射 + 默认值 map + cache 的拼装
// 容易掉坑,加个 explicit test 把这条契约钉死,以后改 setting 服务时不
// 会无声把这条 API 撞坏。

import (
	"encoding/json"
	"testing"

	"nexcore-x-ui/web/entity"
)

func TestPatchSettings_NodeName_RoundTrip(t *testing.T) {
	setUpDB(t)
	svc := SettingService{}

	// 模拟 patchSettings handler:先取当前,再把 client JSON merge 进去,
	// 最后调 UpdateAllSetting。
	current, err := svc.GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting: %v", err)
	}
	if current.NodeName != "" {
		t.Fatalf("freshly-init DB should have empty nodeName, got %q", current.NodeName)
	}

	// 模拟 c.ShouldBindJSON(current) — 客户端只发 nodeName,其它字段不动。
	body := []byte(`{"nodeName": "香港-A"}`)
	if err := json.Unmarshal(body, current); err != nil {
		t.Fatalf("unmarshal merge: %v", err)
	}
	if current.NodeName != "香港-A" {
		t.Fatalf("after merge NodeName mismatch: %q", current.NodeName)
	}

	// 持久化。
	if err := svc.UpdateAllSetting(current); err != nil {
		t.Fatalf("UpdateAllSetting: %v", err)
	}

	// 重新读取,确认确实落库了 — GetNodeName 走的是 settingsCache,
	// 但 saveSetting 已经 invalidate,所以这里取的是新值。
	if got := svc.GetNodeName(); got != "香港-A" {
		t.Fatalf("GetNodeName after PATCH: want %q got %q", "香港-A", got)
	}

	// GET /settings 也要看得到。
	again, err := svc.GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting after PATCH: %v", err)
	}
	if again.NodeName != "香港-A" {
		t.Fatalf("GET /settings nodeName: want %q got %q", "香港-A", again.NodeName)
	}

	// 改回空 — nodeName 不在 skipIfEmptyKeys 里,PATCH 空字符串应该
	// 真的清掉(跟 token 类字段不同)。
	current2, _ := svc.GetAllSetting()
	if err := json.Unmarshal([]byte(`{"nodeName": ""}`), current2); err != nil {
		t.Fatalf("unmarshal clear: %v", err)
	}
	if err := svc.UpdateAllSetting(current2); err != nil {
		t.Fatalf("UpdateAllSetting clear: %v", err)
	}
	if got := svc.GetNodeName(); got != "" {
		t.Fatalf("GetNodeName after clear: want empty got %q", got)
	}

	// 顺便回归检查 nodeAddress / onlineWebhookNodeId 这些"用户也常想 API
	// 改"的字段都能 round-trip(同样反射 path 处理,集中一次校验比每个
	// 字段单独测稳)。
	current3, _ := svc.GetAllSetting()
	if err := json.Unmarshal([]byte(`{"nodeAddress": "1.2.3.4", "onlineWebhookNodeId": "node-A"}`), current3); err != nil {
		t.Fatalf("unmarshal multi: %v", err)
	}
	if err := svc.UpdateAllSetting(current3); err != nil {
		t.Fatalf("UpdateAllSetting multi: %v", err)
	}
	got, _ := svc.GetAllSetting()
	if got.NodeAddress != "1.2.3.4" || got.OnlineWebhookNodeId != "node-A" {
		t.Fatalf("multi-field PATCH didn't persist: nodeAddress=%q onlineWebhookNodeId=%q",
			got.NodeAddress, got.OnlineWebhookNodeId)
	}

	// AllSetting struct shape 健康检查:确保未来有人删 NodeName 字段
	// 时 IDE 会撞我们这个 test。
	var a entity.AllSetting
	a.NodeName = "x"
	_ = a
}
