package service

// Automated coverage for v1.0.4 — the dry-run + protocol-singleton guards
// that prevent the "one bad inbound takes down all protocols" failure mode.
//
// We exercise the real InboundService against an in-memory sqlite DB. The
// xray binary is replaced with /usr/bin/true (always-pass) or /usr/bin/false
// (always-reject) via NEXCORE_XRAY_BIN — enough to verify the wiring without
// needing a cross-arch xray binary on the dev machine. The protocol-
// singleton check is pure-Go and runs unconditionally.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
)

// setUpDB initializes a fresh sqlite DB in t.TempDir() and arranges teardown.
// We can't use ":memory:" because InitDB() chmods the data dir to 0700, which
// has no meaning for the in-memory mode. A throwaway temp dir is closer to
// production behavior anyway.
func setUpDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
}

// withFakeXray points NEXCORE_XRAY_BIN at the given absolute path for the
// duration of the test, restoring the previous value on cleanup.
func withFakeXray(t *testing.T, path string) {
	t.Helper()
	prev := os.Getenv("NEXCORE_XRAY_BIN")
	t.Setenv("NEXCORE_XRAY_BIN", path)
	t.Cleanup(func() { _ = os.Setenv("NEXCORE_XRAY_BIN", prev) })
}

func sampleVless(port int) *model.Inbound {
	return &model.Inbound{
		Port:           port,
		Protocol:       model.VLESS,
		Tag:            "inbound-" + itoa(port),
		Enable:         true,
		Settings:       `{"clients":[{"id":"00000000-0000-0000-0000-000000000001","email":"alice"}],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Sniffing:       `{"enabled":true,"destOverride":["http","tls"]}`,
	}
}

func itoa(n int) string {
	// avoid pulling strconv into one place; the values are tiny.
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 6)
	if n < 0 {
		buf = append(buf, '-')
		n = -n
	}
	digits := make([]byte, 0, 6)
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	for i := len(digits) - 1; i >= 0; i-- {
		buf = append(buf, digits[i])
	}
	return string(buf)
}

// TestAddInbound_DryRunPasses confirms that a valid candidate (fake xray
// always-pass) lands in the DB and assigns a non-zero ID.
func TestAddInbound_DryRunPasses(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")

	svc := InboundService{}
	in := sampleVless(11001)
	if err := svc.AddInbound(in); err != nil {
		t.Fatalf("AddInbound rejected a valid candidate: %v", err)
	}
	if in.Id == 0 {
		t.Fatal("AddInbound returned without assigning an ID")
	}
}

// TestAddInbound_DryRunRejects confirms that when xray test exits non-zero
// (fake binary returns 1), the inbound is NOT persisted and the caller gets
// ErrXrayConfigInvalid. This is the entire point of v1.0.4: a busted config
// can never reach the running xray process via panel writes.
func TestAddInbound_DryRunRejects(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/false")

	svc := InboundService{}
	in := sampleVless(11002)
	err := svc.AddInbound(in)
	if err == nil {
		t.Fatal("AddInbound should have rejected the candidate when xray test fails")
	}
	if !errors.Is(err, ErrXrayConfigInvalid) {
		t.Fatalf("expected ErrXrayConfigInvalid, got: %v", err)
	}
	// And critically: the row should NOT be in the DB.
	rows, _ := svc.GetAllInbounds()
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after dry-run rejection, found %d", len(rows))
	}
}

// TestUpdateInbound_DryRunRejects confirms the protection extends to edits:
// a previously-valid inbound that the operator tries to break (e.g. paste a
// malformed reality config) gets rejected and the old row stays intact.
func TestUpdateInbound_DryRunRejects(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true") // create succeeds
	svc := InboundService{}
	original := sampleVless(11003)
	if err := svc.AddInbound(original); err != nil {
		t.Fatalf("setup AddInbound: %v", err)
	}

	withFakeXray(t, "/usr/bin/false") // now reject all updates
	bad := sampleVless(11003)
	bad.Id = original.Id
	bad.Remark = "edited"
	err := svc.UpdateInbound(bad)
	if !errors.Is(err, ErrXrayConfigInvalid) {
		t.Fatalf("expected ErrXrayConfigInvalid on update, got: %v", err)
	}
	// Old row must survive untouched.
	cur, err := svc.GetInbound(original.Id)
	if err != nil {
		t.Fatalf("GetInbound: %v", err)
	}
	if cur.Remark == "edited" {
		t.Fatal("update was persisted despite xray rejection")
	}
}

// TestSetEnableMany_DryRunRejects covers the highest-value path: a previously-
// disabled, broken inbound must be blocked from being enabled, because flipping
// it would crash xray on next reload and take every other inbound down with
// it. This is the exact scenario 3x-ui historically falls down on.
func TestSetEnableMany_DryRunRejects(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}
	disabled := sampleVless(11004)
	disabled.Enable = false
	if err := svc.AddInbound(disabled); err != nil {
		t.Fatalf("setup AddInbound: %v", err)
	}

	withFakeXray(t, "/usr/bin/false")
	_, err := svc.SetEnableMany([]int{disabled.Id}, true)
	if !errors.Is(err, ErrXrayConfigInvalid) {
		t.Fatalf("expected ErrXrayConfigInvalid on enable, got: %v", err)
	}
	// Row should still be disabled.
	cur, _ := svc.GetInbound(disabled.Id)
	if cur.Enable {
		t.Fatal("inbound was enabled despite dry-run rejection")
	}
}

// TestSetEnableMany_DisableSkipsDryRun verifies the optimization: turning OFF
// inbounds can never increase what xray sees, so we don't waste a binary
// invocation. We point NEXCORE_XRAY_BIN at /usr/bin/false to PROVE the path
// doesn't run xray — if it did, the test would fail.
func TestSetEnableMany_DisableSkipsDryRun(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}
	enabled := sampleVless(11005)
	if err := svc.AddInbound(enabled); err != nil {
		t.Fatalf("setup AddInbound: %v", err)
	}

	withFakeXray(t, "/usr/bin/false") // xray would reject — but we shouldn't call it
	if _, err := svc.SetEnableMany([]int{enabled.Id}, false); err != nil {
		t.Fatalf("disable path called xray when it shouldn't: %v", err)
	}
	cur, _ := svc.GetInbound(enabled.Id)
	if cur.Enable {
		t.Fatal("expected inbound to be disabled")
	}
}

// TestProtocolSingleton_VLESS verifies that adding a second VLESS inbound is
// rejected with ErrProtocolSingleton — VLESS supports settings.clients[] so
// two separate inbounds for it would just fight over the same protocol path.
func TestProtocolSingleton_VLESS(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}
	first := sampleVless(11006)
	if err := svc.AddInbound(first); err != nil {
		t.Fatalf("setup: %v", err)
	}
	second := sampleVless(11007)
	err := svc.AddInbound(second)
	if !errors.Is(err, ErrProtocolSingleton) {
		t.Fatalf("expected ErrProtocolSingleton on second VLESS, got: %v", err)
	}
}

// TestProtocolSingleton_LegacyShadowsocksAllowsMany asserts that legacy AEAD
// shadowsocks (single password = single user) can have multiple inbounds.
// Only 2022-blake3-* methods support clients[] and therefore singleton.
func TestProtocolSingleton_LegacyShadowsocksAllowsMany(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}
	mk := func(port int) *model.Inbound {
		return &model.Inbound{
			Port:           port,
			Protocol:       model.Shadowsocks,
			Tag:            "inbound-" + itoa(port),
			Enable:         true,
			Settings:       `{"method":"chacha20-ietf-poly1305","password":"abc"}`,
			StreamSettings: `{"network":"tcp","security":"none"}`,
			Sniffing:       `{"enabled":false}`,
		}
	}
	if err := svc.AddInbound(mk(11008)); err != nil {
		t.Fatalf("first SS: %v", err)
	}
	if err := svc.AddInbound(mk(11009)); err != nil {
		t.Fatalf("second legacy SS should be allowed, got: %v", err)
	}
}

// TestProtocolSingleton_SS2022 asserts that 2022-blake3-* shadowsocks DOES
// enforce the singleton, since that mode supports settings.clients[].
func TestProtocolSingleton_SS2022(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}
	mk := func(port int) *model.Inbound {
		return &model.Inbound{
			Port:           port,
			Protocol:       model.Shadowsocks,
			Tag:            "inbound-" + itoa(port),
			Enable:         true,
			Settings:       `{"method":"2022-blake3-aes-128-gcm","password":"abcdef==","clients":[]}`,
			StreamSettings: `{"network":"tcp","security":"none"}`,
			Sniffing:       `{"enabled":false}`,
		}
	}
	if err := svc.AddInbound(mk(11010)); err != nil {
		t.Fatalf("first 2022 SS: %v", err)
	}
	err := svc.AddInbound(mk(11011))
	if !errors.Is(err, ErrProtocolSingleton) {
		t.Fatalf("expected ErrProtocolSingleton on second 2022-blake3 SS, got: %v", err)
	}
}

// TestProtocolSingleton_DifferentOutboundsAllowed pins the v2.0.15 relaxation:
// 同协议 + 不同 OutboundTag 放行,因为按区域中转拆桶是常见架构(一台 vmess 直连
// 本机出口、一台走 us-relay、一台走 jp-relay)。三条监听不同端口、走不同出口,
// 互不冲突。同 OutboundTag 仍然冲突 — 两条相同 (protocol, outbound) = 自己跟
// 自己抢出口,除了误操作没有合理用途。
func TestProtocolSingleton_DifferentOutboundsAllowed(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}

	first := sampleVless(11020)
	first.OutboundTag = "" // 直连
	if err := svc.AddInbound(first); err != nil {
		t.Fatalf("setup first: %v", err)
	}

	second := sampleVless(11021)
	second.OutboundTag = "us-relay"
	if err := svc.AddInbound(second); err != nil {
		t.Fatalf("second VLESS with different outbound should be allowed, got: %v", err)
	}

	third := sampleVless(11022)
	third.OutboundTag = "jp-relay"
	if err := svc.AddInbound(third); err != nil {
		t.Fatalf("third VLESS with another outbound should be allowed, got: %v", err)
	}

	// 同 OutboundTag 还是不让加 — bucket "us-relay" 已经有第二条了
	dup := sampleVless(11023)
	dup.OutboundTag = "us-relay"
	if err := svc.AddInbound(dup); !errors.Is(err, ErrProtocolSingleton) {
		t.Fatalf("expected ErrProtocolSingleton on duplicate (vless, us-relay), got: %v", err)
	}

	// 直连那条已经存在,再加直连同样冲突
	dupDirect := sampleVless(11024)
	dupDirect.OutboundTag = ""
	if err := svc.AddInbound(dupDirect); !errors.Is(err, ErrProtocolSingleton) {
		t.Fatalf("expected ErrProtocolSingleton on duplicate (vless, direct), got: %v", err)
	}
}

// TestIsMultiUserProtocol covers the protocol-classification logic directly,
// independent of any DB state.
func TestIsMultiUserProtocol(t *testing.T) {
	cases := []struct {
		name     string
		protocol model.Protocol
		settings string
		want     bool
	}{
		{"vless", model.VLESS, `{}`, true},
		{"vmess", model.VMess, `{}`, true},
		{"trojan", model.Trojan, `{}`, true},
		{"ss-2022-blake3-aes-128", model.Shadowsocks, `{"method":"2022-blake3-aes-128-gcm"}`, true},
		{"ss-2022-blake3-aes-256", model.Shadowsocks, `{"method":"2022-blake3-aes-256-gcm"}`, true},
		{"ss-legacy-chacha20", model.Shadowsocks, `{"method":"chacha20-ietf-poly1305"}`, false},
		{"ss-empty-method", model.Shadowsocks, `{}`, false},
		{"socks", model.Socks, `{}`, false},
		{"http", model.Http, `{}`, false},
		{"dokodemo", model.Dokodemo, `{}`, false},
		{"wireguard", model.Wireguard, `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &model.Inbound{Protocol: tc.protocol, Settings: tc.settings}
			if got := isMultiUserProtocol(in); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
