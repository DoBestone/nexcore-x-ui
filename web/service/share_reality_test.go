package service

// Automated coverage for the REALITY / Vision share-link path. The backend
// already extracts realitySettings.{publicKey, shortIds, serverNames,
// fingerprint} into the URL params (see mergeStreamQuery in share.go) — these
// tests pin that contract so a future refactor can't silently regress to
// emitting an unusable client URI.

import (
	"net/url"
	"strings"
	"testing"

	"nexcore-x-ui/database/model"
)

// realityInbound is a representative VLESS+REALITY+Vision inbound fixture.
// Values are ones a real operator would paste from the xray docs / Telegram
// channel — UUIDs/keys here are obviously fake but the SHAPE is what client
// apps care about.
func realityInbound() *model.Inbound {
	return &model.Inbound{
		Id:       1,
		Port:     443,
		Protocol: model.VLESS,
		Tag:      "inbound-443",
		Enable:   true,
		Remark:   "node-A",
		Settings: `{"clients":[{"id":"00000000-0000-0000-0000-000000000abc","email":"alice","flow":"xtls-rprx-vision"}],"decryption":"none"}`,
		StreamSettings: `{
			"network": "tcp",
			"security": "reality",
			"realitySettings": {
				"show": false,
				"dest": "www.cloudflare.com:443",
				"xver": 0,
				"serverNames": ["www.cloudflare.com"],
				"privateKey": "FAKE_PRIVATE_KEY_FOR_TESTS",
				"publicKey":  "FAKE_PUBLIC_KEY_FOR_TESTS",
				"shortIds": ["abc123ee"],
				"fingerprint": "chrome"
			}
		}`,
		Sniffing: `{"enabled":true,"destOverride":["http","tls"]}`,
	}
}

// TestShareLink_VlessReality_AllParams verifies that every reality-relevant
// query parameter a modern client (V2RayN, NekoBox, Shadowrocket) needs is
// emitted in the vless:// URI: security, sni, pbk, sid, fp, flow, type.
// Missing any one of these makes the link unusable.
func TestShareLink_VlessReality_AllParams(t *testing.T) {
	in := realityInbound()
	links, err := (&ShareService{}).linksForLoadedInbound(in, "node.example.com")
	if err != nil {
		t.Fatalf("linksForLoadedInbound: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	link := links[0]

	if !strings.HasPrefix(link, "vless://") {
		t.Fatalf("expected vless:// scheme, got: %s", link)
	}

	// Parse the query params for assertions.
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("malformed URI: %v", err)
	}
	q := parsed.Query()

	mustHave := map[string]string{
		"security": "reality",
		"sni":      "www.cloudflare.com",
		"pbk":      "FAKE_PUBLIC_KEY_FOR_TESTS", // share link gets publicKey, NOT privateKey
		"sid":      "abc123ee",
		"fp":       "chrome",
		"flow":     "xtls-rprx-vision",
		"type":     "tcp",
	}
	for k, want := range mustHave {
		got := q.Get(k)
		if got != want {
			t.Errorf("query %s = %q, want %q", k, got, want)
		}
	}

	// Sanity: the path part is the inbound port.
	if parsed.Port() != "443" {
		t.Errorf("port = %s, want 443", parsed.Port())
	}
}

// TestShareLink_VlessNoReality verifies that a plain VLESS+TCP+TLS inbound
// (no reality block) emits a clean URI without phantom reality params. This
// pins the negative case so we don't accidentally leak `pbk=` / `sid=` from
// stale fixtures.
func TestShareLink_VlessNoReality(t *testing.T) {
	in := &model.Inbound{
		Id:       1,
		Port:     8443,
		Protocol: model.VLESS,
		Tag:      "inbound-8443",
		Enable:   true,
		Settings: `{"clients":[{"id":"00000000-0000-0000-0000-000000000abc","email":"bob","flow":"xtls-rprx-vision"}],"decryption":"none"}`,
		StreamSettings: `{
			"network": "tcp",
			"security": "tls",
			"tlsSettings": { "serverName": "www.example.com" }
		}`,
		Sniffing: `{"enabled":true}`,
	}
	links, err := (&ShareService{}).linksForLoadedInbound(in, "node.example.com")
	if err != nil {
		t.Fatalf("linksForLoadedInbound: %v", err)
	}
	if len(links) != 1 {
		t.Fatal("expected 1 link")
	}
	q, _ := url.Parse(links[0])
	got := q.Query()

	if got.Get("security") != "tls" {
		t.Errorf("security = %q, want tls", got.Get("security"))
	}
	if got.Get("sni") != "www.example.com" {
		t.Errorf("sni = %q, want www.example.com", got.Get("sni"))
	}
	if got.Get("flow") != "xtls-rprx-vision" {
		t.Errorf("flow = %q, want xtls-rprx-vision", got.Get("flow"))
	}
	for _, ghost := range []string{"pbk", "sid", "fp"} {
		if v := got.Get(ghost); v != "" {
			t.Errorf("unexpected %s = %q on a non-reality inbound", ghost, v)
		}
	}
}

// TestShareLink_VlessReality_MultiClient asserts each client in the inbound
// produces its own URI — and they all carry identical reality params (one
// reality block serves N clients).
func TestShareLink_VlessReality_MultiClient(t *testing.T) {
	in := realityInbound()
	in.Settings = `{"clients":[
		{"id":"00000000-0000-0000-0000-000000000111","email":"alice","flow":"xtls-rprx-vision"},
		{"id":"00000000-0000-0000-0000-000000000222","email":"bob","flow":"xtls-rprx-vision"}
	],"decryption":"none"}`

	links, err := (&ShareService{}).linksForLoadedInbound(in, "node.example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}

	for i, link := range links {
		parsed, _ := url.Parse(link)
		q := parsed.Query()
		if q.Get("pbk") == "" || q.Get("sni") == "" {
			t.Errorf("link[%d] missing reality params: %s", i, link)
		}
	}

	// Different UUIDs in the user-info section.
	a, _ := url.Parse(links[0])
	b, _ := url.Parse(links[1])
	if a.User.String() == b.User.String() {
		t.Errorf("two clients should produce distinct UUIDs; both got %s", a.User.String())
	}
}
