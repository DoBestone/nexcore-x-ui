package service

// 回归测试:validateListenAddress 必须挡住"本机不存在的 IP"这条 UX 坑。
// 历史故障:操作员把云厂商弹性公网 IP 填进 inbound.listen,xray 启动
// bind 失败整个进程退出,所有协议入站(包含完全无关的 SS / VLESS)一起
// 不可达,客户端表现为软件超时 —— 排障路径反直觉。
//
// 用例覆盖:
//   - 三类必须放行:空串(默认监听全部) / 0.0.0.0 / ::(IPv6 unspecified)
//   - 解析不出 IP 的字符串(包括域名 —— xray 也不接受域名做 listen)
//   - 解析出 IP 但本机 NIC 没这地址(典型坑:云公网 IP / NAT 地址)
//   - 任何机器都肯定有的 127.0.0.1 必须放行(回归 self-bind 路径)

import (
	"strings"
	"testing"
)

func TestValidateListenAddress_AcceptsUnspecifiedAndEmpty(t *testing.T) {
	for _, in := range []string{"", "0.0.0.0", "::", "::0", "  "} {
		if err := validateListenAddress(in); err != nil {
			t.Errorf("validateListenAddress(%q) = %v, want nil (unspecified / empty must pass)", in, err)
		}
	}
}

func TestValidateListenAddress_AcceptsLocalLoopback(t *testing.T) {
	// 127.0.0.1 在所有 Linux/macOS/Windows 内核启动后立刻就有,这是
	// 跨平台稳定的测试 fixture。如果这条挂了,说明 NIC 检索逻辑断了,
	// 而不是 IP 选错了。
	if err := validateListenAddress("127.0.0.1"); err != nil {
		t.Errorf("validateListenAddress(127.0.0.1) = %v, want nil — loopback should always be present", err)
	}
}

func TestValidateListenAddress_RejectsNonIPString(t *testing.T) {
	// xray 自己也不接受域名做 listen;面板早 catch 比 xray 启动后崩好。
	cases := []string{
		"example.com",
		"localhost", // 是名字不是 IP 字面量,xray 同样拒绝
		"not-an-ip",
		"999.999.999.999",
	}
	for _, in := range cases {
		err := validateListenAddress(in)
		if err == nil {
			t.Errorf("validateListenAddress(%q) = nil, want error", in)
			continue
		}
		// 错误文案要带原值,前端 toast 才能告诉用户填的是啥。
		if !strings.Contains(err.Error(), in) {
			t.Errorf("validateListenAddress(%q) error %q should mention the bad input", in, err.Error())
		}
	}
}

func TestValidateListenAddress_RejectsRemoteIP(t *testing.T) {
	// 198.51.100.0/24 是 RFC 5737 文档保留段,默认不会出现在任何真机
	// NIC 上,做"远端 IP"用例最稳,不依赖测试机的实际公网地址。
	in := "198.51.100.42"
	err := validateListenAddress(in)
	if err == nil {
		t.Fatalf("validateListenAddress(%q) = nil, want error (IP 不应该在本机 NIC 上)", in)
	}
	// 错误文案需要解释为什么 —— 帮用户做下一步决定。
	for _, want := range []string{"NIC", "网卡", in} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validateListenAddress(%q) error %q should mention %q so the user understands the failure mode", in, err.Error(), want)
		}
	}
}
