package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigHidesMcpToken(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "secret"}, t.TempDir())
	s.port = 8976
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{}})

	resp := s.configResponse(*s.cfg)
	if _, ok := resp["mcpToken"]; ok {
		t.Fatal("token leaked in config GET")
	}
	if resp["mcpEnabled"] != true {
		t.Fatal("mcpEnabled missing")
	}
	if _, ok := resp["mcpUrl"]; !ok {
		t.Fatal("mcpUrl missing")
	}
}

func TestMcpRotateRouteMintsTokenAndEnables(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := newServerDir(&config{LAN: true}, t.TempDir())
	s.port = 8976
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{}})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://localhost/api/mcp/rotate", strings.NewReader(`{"enabled":true}`))
	r.Header.Set("X-Owldrop-Token", s.token)
	s.routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	token, _ := resp["mcpToken"].(string)
	if token == "" || token != s.cfg.McpToken {
		t.Fatalf("mcpToken = %q, config token = %q", token, s.cfg.McpToken)
	}
	if resp["mcpEnabled"] != true || !s.cfg.McpEnabled {
		t.Fatalf("MCP not enabled: response = %#v, config = %#v", resp, s.cfg)
	}
	if _, ok := resp["mcpUrl"]; !ok {
		t.Fatalf("mcpUrl missing: %#v", resp)
	}
}

func TestMcpStatusRouteEnablesAndMintsToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := newServerDir(&config{LAN: true}, t.TempDir())
	s.port = 8976
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{}})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://localhost/api/mcp", strings.NewReader(`{"enabled":true}`))
	r.Header.Set("X-Owldrop-Token", s.token)
	s.routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status POST = %d, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["enabled"] != true || resp["token"] == "" || resp["token"] != s.cfg.McpToken {
		t.Fatalf("status response = %#v, config = %#v", resp, s.cfg)
	}
	if _, ok := resp["url"]; !ok {
		t.Fatalf("url missing: %#v", resp)
	}
}

func TestMcpEnableRequiresLANOrServe(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := newServerDir(&config{}, t.TempDir())
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{}})

	for _, path := range []string{"/api/mcp", "/api/config"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://localhost"+path, strings.NewReader(`{"enabled":true,"mcpEnabled":true}`))
		r.Header.Set("X-Owldrop-Token", s.token)
		s.routes().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, body = %s", path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "turn on LAN mode or HTTPS access first") {
			t.Errorf("%s body = %q", path, w.Body.String())
		}
	}
	if s.cfg.McpEnabled || s.cfg.McpToken != "" {
		t.Fatalf("rejected enable changed config: %#v", s.cfg)
	}
}

func TestMcpRouteUsesMcpTokenNotSessionToken(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "agent-token"}, t.TempDir())
	s.port = 8976
	h := s.routes()
	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(body))
	r.Header.Set("X-Owldrop-Token", s.token)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("session token status = %d, want 401", w.Code)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer agent-token")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"result"`) {
		t.Fatalf("MCP token response = %d %s", w.Code, w.Body.String())
	}
}

func TestMcpSendDenyReason(t *testing.T) {
	if got := mcpSendDenyReason("owned by another user"); !strings.Contains(got, "tagged") {
		t.Fatalf("got %q", got)
	}
	if mcpSendDenyReason("available") != "" {
		t.Fatal("available should send")
	}
	if mcpSendDenyReason("offline") == "" {
		t.Fatal("offline should deny")
	}
	if mcpSendDenyReason("") == "" {
		t.Fatal("missing availability should deny")
	}
}

func TestMcpPeerArgsAcceptsOptionalPeers(t *testing.T) {
	got, err := mcpPeerArgs(map[string]any{
		"peer":  "node-1",
		"peers": []string{"node-2", "node-3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[node-1 node-2 node-3]" {
		t.Fatalf("peers = %v", got)
	}
}

func TestMcpDecodeSendDataEnforcesDecodedLimit(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(make([]byte, mcpSendFileMax+1))
	if _, err := mcpDecodeSendData(encoded); err == nil || !strings.Contains(err.Error(), "too_large") {
		t.Fatalf("oversize decoded payload error = %v", err)
	}
}

func TestMcpGetFileTooLarge(t *testing.T) {
	if mcpGetFileMax != 1<<20 {
		t.Fatalf("cap %d", mcpGetFileMax)
	}
	err := mcpTooLarge(mcpGetFileMax + 1)
	if err == nil || !strings.Contains(err.Error(), "too_large") {
		t.Fatalf("got %v", err)
	}
}

func TestMcpCallListInboxEmpty(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	out, err := s.mcpCallTool(context.Background(), "list_inbox", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	files, _ := m["files"].([]any)
	if files == nil {
		// also accept []waitingFile encoded later — require a files key
		if _, ok := m["files"]; !ok {
			t.Fatalf("no files key: %#v", out)
		}
	}
}

func TestMcpCreateDropLinkDoesNotEnableFunnel(t *testing.T) {
	s := newServerDir(&config{McpEnabled: true, McpToken: "tok", LAN: true}, t.TempDir())
	s.selfDNSName()
	s.selfDNS = "desk.tail.ts.net"
	fs := &fakeServeStore{cfg: &serveConfigWire{}}
	s.serving = newServingManager(fs)
	out, err := s.mcpCallTool(context.Background(), "create_drop_link", map[string]any{"name": "agent", "ttl_minutes": 60.0})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if _, ok := m["share_url"].(string); !ok {
		t.Fatalf("missing share_url: %#v", out)
	}
	if s.funnelActive() {
		t.Fatal("create_drop_link enabled Funnel")
	}
	if m["public_url"] != nil && m["public_url"] != "" {
		t.Fatal("public_url set without Funnel")
	}
}

func TestMcpCreateDropLinkMaxUsesOverflow(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	before := len(s.drops.list())
	_, err := s.mcpCallTool(t.Context(), "create_drop_link", map[string]any{
		"name":     "overflow",
		"max_uses": 9.223372036854776e+18, // 2^63; int() would overflow negative on amd64
	})
	if err == nil {
		t.Fatal("expected error for overflowing max_uses")
	}
	if len(s.drops.list()) != before {
		t.Fatal("overflowing max_uses created a link")
	}
}

func TestMcpCreateDropLinkOptionsAndExistingFunnel(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	s.port = 8976
	s.selfDNSName()
	s.selfDNS = "desk.tail.ts.net"
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{
		AllowFunnel: map[string]bool{"desk.tail.ts.net:443": true},
	}})

	before := time.Now()
	out, err := s.mcpCallTool(t.Context(), "create_drop_link", map[string]any{
		"name":        "single",
		"ttl_minutes": float64(8 * 24 * 60),
		"max_uses":    4.0,
		"single":      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)
	link := got["link"].(*dropLink)
	if link.MaxUses != 1 {
		t.Fatalf("max uses = %d, want 1", link.MaxUses)
	}
	wantExpiry := before.Add(7 * 24 * time.Hour)
	if link.Expires.Before(wantExpiry) || link.Expires.After(wantExpiry.Add(time.Second)) {
		t.Fatalf("expiry = %v, want about %v", link.Expires, wantExpiry)
	}
	wantPublic := "https://desk.tail.ts.net/drop/" + link.Token
	if got["public_url"] != wantPublic || got["share_url"] != wantPublic {
		t.Fatalf("drop-link URLs = %#v, want public %q", got, wantPublic)
	}
	if !s.funnelActive() {
		t.Fatal("create_drop_link disabled existing Funnel")
	}
}

func TestMcpListDropLinksSkipsRevokedAndExpired(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	active := s.drops.create("active", time.Hour, 0, 0)
	revoked := s.drops.create("revoked", time.Hour, 0, 0)
	s.drops.revoke(revoked.Token)
	s.drops.create("expired", -time.Minute, 0, 0)

	out, err := s.mcpCallTool(t.Context(), "list_drop_links", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	links := out.(map[string]any)["links"].([]map[string]any)
	if len(links) != 1 {
		t.Fatalf("links = %#v, want only active link", links)
	}
	if links[0]["link"].(*dropLink).Token != active.Token {
		t.Fatalf("active link = %#v, want token %q", links[0], active.Token)
	}
	if links[0]["share_url"] == "" {
		t.Fatalf("missing share_url: %#v", links[0])
	}
}

func TestMcpCallPostAndListSyncText(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	if _, err := s.mcpCallTool(t.Context(), "post_sync", map[string]any{"text": "hello"}); err != nil {
		t.Fatal(err)
	}

	out, err := s.mcpCallTool(t.Context(), "list_sync", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	items := out.(map[string]any)["items"].([]syncItem)
	if len(items) != 1 || items[0].Text != "hello" {
		t.Fatalf("list_sync items = %+v", items)
	}
}

func TestMcpCallListSyncTruncatesTextByRunes(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	text := strings.Repeat("界", 2049)
	if _, err := s.sync.addText(text); err != nil {
		t.Fatal(err)
	}

	out, err := s.mcpCallTool(t.Context(), "list_sync", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	item := out.(map[string]any)["items"].([]syncItem)[0]
	if got := len([]rune(item.Text)); got != 2048 {
		t.Fatalf("list_sync text runes = %d, want 2048", got)
	}
}

func TestMcpCallPostSyncFile(t *testing.T) {
	dir := t.TempDir()
	s := newServerDir(&config{}, dir)
	payload := []byte("sync file payload")
	out, err := s.mcpCallTool(t.Context(), "post_sync", map[string]any{
		"name": "note.txt",
		"data": base64.StdEncoding.EncodeToString(payload),
	})
	if err != nil {
		t.Fatal(err)
	}

	item := out.(syncItem)
	if item.Kind != "file" || item.Name != "note.txt" || item.Size != int64(len(payload)) {
		t.Fatalf("post_sync item = %+v", item)
	}
	path := filepath.Join(dir, syncDirName, item.ID+"-"+item.Name)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("stored data = %q, err = %v", got, err)
	}
}

func TestMcpCallPostSyncFileLimitAndArguments(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	oversized := base64.StdEncoding.EncodeToString(make([]byte, (4<<20)+1))
	for _, args := range []map[string]any{
		{},
		{"name": "note.txt"},
		{"data": base64.StdEncoding.EncodeToString([]byte("data"))},
		{"name": "../note.txt", "data": base64.StdEncoding.EncodeToString([]byte("data"))},
		{"name": "large.bin", "data": oversized},
	} {
		if _, err := s.mcpCallTool(t.Context(), "post_sync", args); err == nil {
			t.Errorf("post_sync(%#v) succeeded", args)
		}
	}
	if len(s.sync.list()) != 0 {
		t.Fatal("invalid post_sync added an item")
	}
}

func mcpStoreLinkFile(t *testing.T, s *server, name, content string) {
	t.Helper()
	link := s.drops.create("test sender", time.Hour, 0, 0)
	if _, err := s.drops.storeFile(link.Token, name, int64(len(content)), strings.NewReader(content), func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
}

func TestMcpCallGetLinkFile(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	mcpStoreLinkFile(t, s, "hello.txt", "hello")

	out, err := s.mcpCallTool(t.Context(), "get_file", map[string]any{"name": "hello.txt", "source": "link"})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)
	if got["name"] != "hello.txt" || got["size"] != int64(5) || got["data"] != base64.StdEncoding.EncodeToString([]byte("hello")) {
		t.Fatalf("get_file = %#v", out)
	}
	if s.drops.file("hello.txt") == nil {
		t.Fatal("get_file removed inbox file")
	}
}

func TestMcpCallGetLinkFileTooLarge(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	mcpStoreLinkFile(t, s, "large.bin", strings.Repeat("x", mcpGetFileMax+1))

	if _, err := s.mcpCallTool(t.Context(), "get_file", map[string]any{"name": "large.bin", "source": "link"}); err == nil || !strings.Contains(err.Error(), "too_large") {
		t.Fatalf("get_file error = %v", err)
	}
}

func TestMcpCallSaveLinkFile(t *testing.T) {
	saveDir := t.TempDir()
	s := newServerDir(&config{SaveDir: saveDir}, t.TempDir())
	mcpStoreLinkFile(t, s, "save.txt", "saved")

	out, err := s.mcpCallTool(t.Context(), "save_file", map[string]any{"name": "save.txt", "source": "link"})
	if err != nil {
		t.Fatal(err)
	}
	path := out.(map[string]any)["path"]
	if path != filepath.Join(saveDir, "save.txt") {
		t.Fatalf("path = %#v", path)
	}
	data, err := os.ReadFile(path.(string))
	if err != nil || string(data) != "saved" {
		t.Fatalf("saved data = %q, err = %v", data, err)
	}
	if s.drops.file("save.txt") != nil {
		t.Fatal("save_file left inbox file")
	}
}

func TestMcpCallDeleteLinkFile(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	mcpStoreLinkFile(t, s, "delete.txt", "delete me")
	path := s.drops.file("delete.txt").Path

	out, err := s.mcpCallTool(t.Context(), "delete_file", map[string]any{"name": "delete.txt", "source": "link"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["ok"] != true {
		t.Fatalf("delete_file = %#v", out)
	}
	if s.drops.file("delete.txt") != nil {
		t.Fatal("delete_file left inbox file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted path still exists: %v", err)
	}
}

func TestMcpCallToolValidatesArguments(t *testing.T) {
	s := newServerDir(&config{}, t.TempDir())
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "save_file", args: map[string]any{}},
		{name: "delete_file", args: map[string]any{"name": "../bad"}},
		{name: "get_file", args: map[string]any{"name": 12}},
	} {
		if _, err := s.mcpCallTool(t.Context(), tc.name, tc.args); err == nil {
			t.Errorf("%s(%#v) succeeded", tc.name, tc.args)
		}
	}
}

func TestMcpToolsCallContentResult(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	mcpStoreLinkFile(t, s, "rpc.txt", "rpc")
	h := s.mcpGuard(s.handleMCP)

	post := func(body string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
		r.Host = "100.64.0.1:8976"
		r.RemoteAddr = "100.1.2.3:9"
		r.Header.Set("Authorization", "Bearer tok")
		h(w, r)
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	response := post(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_file","arguments":{"name":"rpc.txt","source":"link"}}}`)
	result := response["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, base64.StdEncoding.EncodeToString([]byte("rpc"))) {
		t.Fatalf("tools/call result = %#v", response)
	}

	response = post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"send_file","arguments":{}}}`)
	result = response["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("tools/call error = %#v", response)
	}
}

func TestMcpURLNeverFunnelHost(t *testing.T) {
	serve := "https://desktop.taila4569.ts.net/"
	ip := "http://100.112.233.3:8976/"
	got := mcpPublicURL(pickPhoneAccessURL(serve, true, []string{
		"http://desktop.taila4569.ts.net:8976/", ip,
	}))
	if got != "http://100.112.233.3:8976/mcp" {
		t.Fatalf("got %q", got)
	}
	if strings.HasPrefix(got, "https://") && strings.Contains(got, ".ts.net/") {
		t.Fatal("Funnel hostname advertised for MCP")
	}
}

func TestMcpGuard(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: false, McpToken: "aabbcc"}, t.TempDir())
	s.port = 8976
	ok := s.mcpGuard(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	hit := func(enabled bool, header, token, host, remote, path string) int {
		s.cfg.McpEnabled = enabled
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://"+host+path, nil)
		r.Host = host
		r.RemoteAddr = remote
		if token != "" {
			if header == "Authorization" {
				token = "Bearer " + token
			}
			r.Header.Set(header, token)
		}
		ok(w, r)
		return w.Code
	}

	if code := hit(false, "Authorization", "aabbcc", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusNotFound {
		t.Errorf("disabled: got %d, want 404", code)
	}
	s.cfg.McpEnabled = true
	s.selfDNSName()
	s.selfDNS = "desktop.taila4569.ts.net"
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{
		AllowFunnel: map[string]bool{"desktop.taila4569.ts.net:443": true},
	}})
	if code := hit(true, "Authorization", "aabbcc", "desktop.taila4569.ts.net", "127.0.0.1:1", "/mcp"); code != http.StatusNotFound {
		t.Errorf("funnel /mcp: got %d, want 404", code)
	}
	if code := hit(true, "Authorization", "aabbcc", "example.com", "100.1.2.3:9", "/mcp"); code != http.StatusForbidden {
		t.Errorf("invalid host: got %d, want 403", code)
	}
	s.cfg.McpToken = ""
	if code := hit(true, "", "", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusNotFound {
		t.Errorf("empty configured token: got %d, want 404", code)
	}
	s.cfg.McpToken = "aabbcc"
	if code := hit(true, "Authorization", "wrong", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusUnauthorized {
		t.Errorf("bad token: got %d, want 401", code)
	}
	if code := hit(true, "Authorization", "aabbcc", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusOK {
		t.Errorf("good: got %d, want 200", code)
	}
	if code := hit(true, "X-Owldrop-Token", "aabbcc", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusOK {
		t.Errorf("X-Owldrop-Token: got %d, want 200", code)
	}
}

func TestMcpInitializeAndListTools(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	h := s.mcpGuard(s.handleMCP)

	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
		r.Host = "100.64.0.1:8976"
		r.RemoteAddr = "100.1.2.3:9"
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("Content-Type", "application/json")
		h(w, r)
		return w.Code, w.Body.String()
	}

	code, body := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	if code != 200 || !strings.Contains(body, `"protocolVersion":"2025-03-26"`) || !strings.Contains(body, `"owldrop"`) {
		t.Fatalf("initialize: %d %s", code, body)
	}
	_, body = post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	for _, name := range []string{
		"list_inbox", "save_file", "delete_file", "get_file",
		"list_devices", "send_file", "list_sync", "post_sync",
		"create_drop_link", "list_drop_links",
	} {
		if !strings.Contains(body, `"`+name+`"`) {
			t.Errorf("tools/list missing %s: %s", name, body)
		}
	}
}

func TestMcpInvalidJSONRPC(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	h := s.mcpGuard(s.handleMCP)

	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
		r.Host = "100.64.0.1:8976"
		r.RemoteAddr = "100.1.2.3:9"
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("Content-Type", "application/json")
		h(w, r)
		return w.Code, w.Body.String()
	}

	for _, body := range []string{
		`{"jsonrpc":"1.0","id":1,"method":"ping"}`,
		`{"id":1,"method":"ping"}`,
	} {
		code, resp := post(body)
		if code != http.StatusOK || !strings.Contains(resp, "-32600") {
			t.Fatalf("invalid jsonrpc %q: %d %s", body, code, resp)
		}
	}
}

func TestMcpMalformedWithoutID(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	h := s.mcpGuard(s.handleMCP)

	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
		r.Host = "100.64.0.1:8976"
		r.RemoteAddr = "100.1.2.3:9"
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("Content-Type", "application/json")
		h(w, r)
		return w.Code, w.Body.String()
	}

	for _, body := range []string{
		`{"jsonrpc":"1.0","method":"ping"}`,
		`{"jsonrpc":"2.0"}`,
	} {
		code, resp := post(body)
		if code != http.StatusOK {
			t.Fatalf("malformed without id %q: got HTTP %d, want 200", body, code)
		}
		if !strings.Contains(resp, "-32600") {
			t.Fatalf("malformed without id %q: missing -32600: %s", body, resp)
		}
		if strings.Contains(resp, `"id":1`) {
			t.Fatalf("malformed without id %q: spurious id in response: %s", body, resp)
		}
	}
}

func TestMcpParseErrorVsInvalidRequest(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	h := s.mcpGuard(s.handleMCP)

	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
		r.Host = "100.64.0.1:8976"
		r.RemoteAddr = "100.1.2.3:9"
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("Content-Type", "application/json")
		h(w, r)
		return w.Code, w.Body.String()
	}

	code, resp := post("{")
	if code != http.StatusOK || !strings.Contains(resp, "-32700") {
		t.Fatalf("invalid JSON: %d %s", code, resp)
	}

	for _, tc := range []struct {
		body string
		code int
	}{
		{body: "[]", code: -32600},
		{body: `{"jsonrpc":"2.0","method":1}`, code: -32600},
	} {
		code, resp := post(tc.body)
		if code != http.StatusOK || !strings.Contains(resp, fmt.Sprintf("%d", tc.code)) {
			t.Fatalf("invalid request %q: %d %s", tc.body, code, resp)
		}
		if strings.Contains(resp, "-32700") {
			t.Fatalf("invalid request %q returned parse error: %s", tc.body, resp)
		}
	}
}

func TestMcpRejectsOversizedRequestBody(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	h := s.mcpGuard(s.handleMCP)
	body := `{"jsonrpc":"2.0","id":1,"method":"ping","padding":"` +
		strings.Repeat("x", 48<<20) + `"}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
	r.Host = "100.64.0.1:8976"
	r.RemoteAddr = "100.1.2.3:9"
	r.Header.Set("Authorization", "Bearer tok")
	r.Header.Set("Content-Type", "application/json")
	h(w, r)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "-32700") {
		t.Fatalf("oversized request: %d %s", w.Code, w.Body.String())
	}
}

func TestMcpTrailingGarbage(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	h := s.mcpGuard(s.handleMCP)

	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
		r.Host = "100.64.0.1:8976"
		r.RemoteAddr = "100.1.2.3:9"
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("Content-Type", "application/json")
		h(w, r)
		return w.Code, w.Body.String()
	}

	code, resp := post(`{"jsonrpc":"2.0","id":1,"method":"ping"} garbage`)
	if code != http.StatusOK || !strings.Contains(resp, "-32700") {
		t.Fatalf("trailing garbage: %d %s", code, resp)
	}
	if strings.Contains(resp, `"result"`) {
		t.Fatalf("trailing garbage dispatched ping: %s", resp)
	}
}

func TestMcpRequestFieldTypes(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	h := s.mcpGuard(s.handleMCP)

	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
		r.Host = "100.64.0.1:8976"
		r.RemoteAddr = "100.1.2.3:9"
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("Content-Type", "application/json")
		h(w, r)
		return w.Code, w.Body.String()
	}

	code, resp := post(`{"jsonrpc":"2.0","id":true,"method":"ping"}`)
	if code != http.StatusOK || !strings.Contains(resp, "-32600") {
		t.Fatalf("boolean id: %d %s", code, resp)
	}
	if strings.Contains(resp, `"id":true`) {
		t.Fatalf("boolean id echoed in response: %s", resp)
	}

	code, resp = post(`{"jsonrpc":"2.0","id":1,"method":"ping","params":"nope"}`)
	if code != http.StatusOK || !strings.Contains(resp, "-32600") {
		t.Fatalf("primitive params: %d %s", code, resp)
	}
	if !strings.Contains(resp, `"id":1`) {
		t.Fatalf("valid id not echoed for invalid params: %s", resp)
	}

	code, resp = post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if code != http.StatusOK || !strings.Contains(resp, `"id":1`) || !strings.Contains(resp, `"result"`) {
		t.Fatalf("numeric id ping: %d %s", code, resp)
	}

	code, resp = post(`{"jsonrpc":"2.0","id":"req-1","method":"ping"}`)
	if code != http.StatusOK || !strings.Contains(resp, `"id":"req-1"`) || !strings.Contains(resp, `"result"`) {
		t.Fatalf("string id ping: %d %s", code, resp)
	}
}

func TestMcpNotifications(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "tok"}, t.TempDir())
	s.port = 8976
	h := s.mcpGuard(s.handleMCP)

	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8976/mcp", strings.NewReader(body))
		r.Host = "100.64.0.1:8976"
		r.RemoteAddr = "100.1.2.3:9"
		r.Header.Set("Authorization", "Bearer tok")
		r.Header.Set("Content-Type", "application/json")
		h(w, r)
		return w.Code, w.Body.String()
	}

	code, body := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != http.StatusNoContent || body != "" {
		t.Fatalf("notifications/initialized: %d %q", code, body)
	}

	code, body = post(`{"jsonrpc":"2.0","method":"ping"}`)
	if code != http.StatusAccepted || body != "" {
		t.Fatalf("ping notification: %d %q", code, body)
	}
	if strings.Contains(body, `"jsonrpc"`) || strings.Contains(body, `"result"`) || strings.Contains(body, `"error"`) {
		t.Fatalf("ping notification returned JSON-RPC body: %s", body)
	}
}
