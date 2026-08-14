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

	hit := func(enabled bool, token, host, remote, path string) int {
		s.cfg.McpEnabled = enabled
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "http://"+host+path, nil)
		r.Host = host
		r.RemoteAddr = remote
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		ok(w, r)
		return w.Code
	}

	if code := hit(false, "aabbcc", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusNotFound {
		t.Errorf("disabled: got %d, want 404", code)
	}
	s.cfg.McpEnabled = true
	s.selfDNSName()
	s.selfDNS = "desktop.taila4569.ts.net"
	s.serving = newServingManager(&fakeServeStore{cfg: &serveConfigWire{
		AllowFunnel: map[string]bool{"desktop.taila4569.ts.net:443": true},
	}})
	if code := hit(true, "aabbcc", "desktop.taila4569.ts.net", "127.0.0.1:1", "/mcp"); code != http.StatusNotFound {
		t.Errorf("funnel /mcp: got %d, want 404", code)
	}
	s.cfg.McpEnabled = true
	if code := hit(true, "wrong", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusUnauthorized {
		t.Errorf("bad token: got %d, want 401", code)
	}
	if code := hit(true, "aabbcc", "100.64.0.1:8976", "100.1.2.3:9", "/mcp"); code != http.StatusOK {
		t.Errorf("good: got %d, want 200", code)
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
