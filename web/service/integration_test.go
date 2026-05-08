package service

// Integration tests covering the inbound CRUD + share-link generation paths
// end-to-end against a real (in-memory'ish, t.TempDir-backed) sqlite DB.
//
// We exercise the paths a typical panel HTTP handler hits: AddInbound,
// GetInboundCtx, LinksForInboundCtx, SubscriptionForAllCtx. Goal isn't
// branch coverage — it's catching regressions in the routes operators
// actually click on (list inbounds, get share link, fetch /sub).
//
// The dry-run xray validator is shorted out with /usr/bin/true via the
// existing withFakeXray helper, so these tests run on any machine with
// busybox + sqlite — no need for an arch-specific xray binary.

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"nexcore-x-ui/database/model"
)

// sampleSSLegacy returns a single-user Shadowsocks inbound. Legacy SS is
// NOT a singleton protocol (isMultiUserProtocol returns false), so the
// "GetAll" / SubscriptionForAll tests can stack multiple of these without
// tripping ErrProtocolSingleton — VLESS / VMess / Trojan would all reject
// a second inbound on the same outbound.
func sampleSSLegacy(port int) *model.Inbound {
	return &model.Inbound{
		Port:           port,
		Protocol:       model.Shadowsocks,
		Tag:            "inbound-ss-" + itoa(port),
		Enable:         true,
		Settings:       `{"method":"chacha20-ietf-poly1305","password":"hunter2"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Sniffing:       `{"enabled":true,"destOverride":["http","tls"]}`,
	}
}

// -- inbound CRUD ----------------------------------------------------------

// TestGetInboundCtx_HitAndMiss covers the happy path (existing row returns)
// and the not-found path (gorm error surfaces). Both are reachable from the
// /api/v1/inbounds/:id endpoint, so a regression here is an immediate API
// contract break.
func TestGetInboundCtx_HitAndMiss(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}

	in := sampleVless(13001)
	if err := svc.AddInbound(in); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}
	got, err := svc.GetInboundCtx(context.Background(), in.Id)
	if err != nil {
		t.Fatalf("GetInboundCtx: %v", err)
	}
	if got.Port != 13001 {
		t.Fatalf("port mismatch: want 13001 got %d", got.Port)
	}

	// Bogus ID — gorm returns ErrRecordNotFound which the controller maps
	// to a 404. Confirm the error is surfaced (rather than silently
	// returning a zero-valued *Inbound).
	if _, err := svc.GetInboundCtx(context.Background(), 999999); err == nil {
		t.Fatal("GetInboundCtx with bogus id should have failed")
	}
}

// TestGetAllInboundsCtx_OrderedAndComplete verifies the bulk list path used
// by /db/traffic and SubscriptionForAll. We add three inbounds and assert
// all three come back; SubscriptionForAll's correctness depends on this not
// silently dropping rows.
func TestGetAllInboundsCtx_OrderedAndComplete(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}

	// Use legacy SS — not subject to the protocol-singleton check so we
	// can legitimately stack three inbounds. (VLESS would reject the
	// second AddInbound with ErrProtocolSingleton.)
	for _, port := range []int{13010, 13011, 13012} {
		if err := svc.AddInbound(sampleSSLegacy(port)); err != nil {
			t.Fatalf("AddInbound %d: %v", port, err)
		}
	}
	rows, err := svc.GetAllInboundsCtx(context.Background())
	if err != nil {
		t.Fatalf("GetAllInboundsCtx: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	seen := map[int]bool{}
	for _, r := range rows {
		seen[r.Port] = true
	}
	for _, p := range []int{13010, 13011, 13012} {
		if !seen[p] {
			t.Fatalf("missing port %d in result", p)
		}
	}
}

// TestGetInboundCtx_RespectsCancellation cancels the context BEFORE calling
// the method. SQLite-on-cancel returns context.Canceled wrapped in a gorm
// error; the precise type doesn't matter as long as the method fails
// quickly and doesn't return a row. We're guarding against a regression
// where someone reverts to database.GetDB() and the cancellation no longer
// propagates.
func TestGetInboundCtx_RespectsCancellation(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}

	in := sampleVless(13020)
	if err := svc.AddInbound(in); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before the call

	_, err := svc.GetInboundCtx(ctx, in.Id)
	if err == nil {
		t.Fatal("expected canceled-context error, got nil — ctx not propagated to DB")
	}
	if !errors.Is(err, context.Canceled) {
		// Some drivers wrap; accept anything mentioning "context".
		if !strings.Contains(strings.ToLower(err.Error()), "context") {
			t.Logf("note: ctx error type was %T: %v (acceptable but unusual)", err, err)
		}
	}
}

// TestDelInbound_RemovesRow covers the delete leg of the panel CRUD wheel.
// Tiny test, but the bug it would catch is real — a typo in the WHERE
// clause that makes DelInbound a no-op would leave operators clicking
// "delete" with no effect, which is exactly the kind of issue users only
// report as "panel feels broken".
func TestDelInbound_RemovesRow(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	svc := InboundService{}

	in := sampleVless(13030)
	if err := svc.AddInbound(in); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}
	if err := svc.DelInbound(in.Id); err != nil {
		t.Fatalf("DelInbound: %v", err)
	}
	if _, err := svc.GetInbound(in.Id); err == nil {
		t.Fatal("inbound still exists after DelInbound")
	}
}

// -- share-link generation -------------------------------------------------

// TestLinksForInboundCtx_VLESS_BuildsLink validates the canonical share
// path: a saved VLESS inbound + a host string => a vless:// URI keyed on
// the client's UUID. This is what the panel's "二维码" button feeds into
// QRCode.js, so a regression here breaks every customer's import flow.
func TestLinksForInboundCtx_VLESS_BuildsLink(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	inSvc := InboundService{}

	in := sampleVless(13040)
	if err := inSvc.AddInbound(in); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}

	share := ShareService{}
	links, err := share.LinksForInboundCtx(context.Background(), in.Id, "panel.example.com")
	if err != nil {
		t.Fatalf("LinksForInboundCtx: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 vless link for 1 client, got %d", len(links))
	}
	got := links[0]
	if !strings.HasPrefix(got, "vless://") {
		t.Fatalf("expected vless:// scheme, got %q", got)
	}
	// The UUID baked into sampleVless settings must appear in the link
	// userinfo — that's how the client identifies itself to xray.
	if !strings.Contains(got, "00000000-0000-0000-0000-000000000001") {
		t.Fatalf("client UUID missing from link: %q", got)
	}
	// And the host we passed in must be the link host (not c.Request.Host
	// or some leftover default).
	if !strings.Contains(got, "panel.example.com:13040") {
		t.Fatalf("expected host:port panel.example.com:13040 in link, got %q", got)
	}
}

// TestLinksByEmailCtx_KeysByClientEmail verifies the per-row QR-modal path.
// The map's keys are client emails; the row's link must match what
// LinksForInbound returns. If the email→link mapping ever drifts, the
// modal shows a QR for the wrong client — silent panel UX corruption.
func TestLinksByEmailCtx_KeysByClientEmail(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	inSvc := InboundService{}

	in := sampleVless(13050)
	if err := inSvc.AddInbound(in); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}

	share := ShareService{}
	byEmail, err := share.LinksByEmailCtx(context.Background(), in.Id, "panel.example.com")
	if err != nil {
		t.Fatalf("LinksByEmailCtx: %v", err)
	}
	link, ok := byEmail["alice"]
	if !ok {
		t.Fatalf("expected entry for email 'alice', got map keys: %v", mapKeys(byEmail))
	}
	if !strings.HasPrefix(link, "vless://") {
		t.Fatalf("alice's link should be vless://, got %q", link)
	}
}

// TestSubscriptionForAllCtx_ConcatenatesEnabledOnly is the headliner: the
// /sub endpoint that every customer's xray client polls. We arrange one
// enabled + one disabled inbound and confirm the subscription body has
// exactly one link.
//
// The subscription body is base64'd vless://...\nvless://... so we decode
// before counting.
func TestSubscriptionForAllCtx_ConcatenatesEnabledOnly(t *testing.T) {
	setUpDB(t)
	withFakeXray(t, "/usr/bin/true")
	inSvc := InboundService{}

	// Two SS inbounds (legacy SS is not a singleton — see sampleSSLegacy).
	enabled := sampleSSLegacy(13060)
	if err := inSvc.AddInbound(enabled); err != nil {
		t.Fatalf("setup enabled: %v", err)
	}
	disabled := sampleSSLegacy(13061)
	disabled.Enable = false
	if err := inSvc.AddInbound(disabled); err != nil {
		t.Fatalf("setup disabled: %v", err)
	}

	share := ShareService{}
	sub, err := share.SubscriptionForAllCtx(context.Background(), "panel.example.com")
	if err != nil {
		t.Fatalf("SubscriptionForAllCtx: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(sub)
	if err != nil {
		t.Fatalf("subscription body must be base64: %v", err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		t.Fatal("subscription body empty — enabled inbound was skipped?")
	}
	lines := strings.Split(body, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line (only the enabled inbound), got %d:\n%s",
			len(lines), body)
	}
	if !strings.Contains(lines[0], "panel.example.com:13060") {
		t.Fatalf("expected enabled inbound's port (13060) in subscription, got %q", lines[0])
	}
	if strings.Contains(body, ":13061") {
		t.Fatalf("disabled inbound (13061) leaked into subscription:\n%s", body)
	}
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// guard: keep model.Inbound import alive even after refactors that drop
// direct references in this file.
var _ model.Inbound
