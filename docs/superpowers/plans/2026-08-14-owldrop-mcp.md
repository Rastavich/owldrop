# Owldrop MCP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve a tailnet-only Streamable HTTP MCP at `POST /mcp` so an agent can inbox/save/send, Sync, and mint a drop link without opening the app.

**Architecture:** In-process JSON-RPC dispatcher in `mcp.go` / `mcp_tools.go`, mounted with `mcpGuard` (host + Funnel 404 + dedicated bearer token). Tools call existing `combinedInbox`, `saveOne`, `sendOne`, Sync, and `drops.create`. Settings copies `mcpURL()` which reuses `pickPhoneAccessURL`.

**Tech Stack:** Go (same module), existing HTTP server, React Settings card. No MCP SDK unless a dispatcher exceeds ~250 lines — then `github.com/modelcontextprotocol/go-sdk` is allowed.

**Spec:** [docs/superpowers/specs/2026-08-14-owldrop-mcp-design.md](../specs/2026-08-14-owldrop-mcp-design.md)

## Global Constraints

- Funnel never serves `/mcp` (`https://*.ts.net/mcp` is 404 while Public access is on).
- MCP token is not the UI session token and is not in `window.__CONFIG__`.
- Off by default; `/mcp` is 404 when `mcp_enabled` is false.
- `create_drop_link` must not call `setFunnel`.
- `get_file` hard cap 1048576 bytes; `send_file` decoded cap 32 MiB.
- Protocol: JSON-RPC 2.0, `protocolVersion` `2025-03-26`, POST-only.
- Tool names (exact): `list_inbox`, `save_file`, `delete_file`, `get_file`, `list_devices`, `send_file`, `list_sync`, `post_sync`, `create_drop_link`, `list_drop_links`.
- Tests: `go test -tags server -count=1 .` and `cd web && npm run typecheck`.

## File map

- Create: `mcp.go` — `mcpGuard`, `mcpURL`, JSON-RPC `handleMCP`, `initialize` / `tools/list` / `ping`
- Create: `mcp_tools.go` — `mcpCallTool`, one function per tool
- Create: `mcp_test.go` — guard, URL, RPC, tool tests
- Modify: `main.go` — `McpEnabled`, `McpToken` on `config` + file load/save
- Modify: `server.go` — mount `/mcp`, `/api/mcp`, `/api/mcp/rotate`; config GET/POST `mcpEnabled`; `configResponse` adds `mcpEnabled`, `mcpUrl` (no token)
- Modify: `qr.go` — keep `pickPhoneAccessURL`; `mcpURL` uses it
- Modify: `web/src/types.ts`, `web/src/api.ts`, `web/src/views/Settings.tsx`
- Modify: `README.md`, `CHANGELOG.md`, `site/public/privacy.html`

---

### Task 1: Config, MCP URL, mcpGuard

**Files:**
- Modify: `main.go` (`config` struct ~90-121, load ~160-186)
- Create: `mcp.go` (guard + URL only in this task)
- Test: `mcp_test.go`

**Interfaces:**
- Consumes: `pickPhoneAccessURL`, `hostAllowed`, `funnelHost`, `funnelActive`, `peerIsLoopback`, `newToken`
- Produces: `func (s *server) mcpURL() string`, `func (s *server) mcpGuard(next http.HandlerFunc) http.HandlerFunc`, `config.McpEnabled bool`, `config.McpToken string`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
```

Also add `mcpPublicURL` in the same file as production code:

```go
func mcpPublicURL(base string) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/mcp"
}
```

- [ ] **Step 2: Run tests — expect FAIL** (undefined `mcpGuard` / `mcpPublicURL` or wrong codes)

Run: `go test -tags server -count=1 -run 'TestMcpURLNeverFunnelHost|TestMcpGuard' .`

- [ ] **Step 3: Implement**

`config` fields:

```go
McpEnabled bool   `json:"mcp_enabled"`
McpToken   string `json:"mcp_token"`
```

Wire them through the `file` load struct and `c := &config{...}` in `loadConfig`. Do not mint `McpToken` on first run.

`mcp.go`:

```go
func (s *server) mcpURL() string {
	lan := s.lanURLs()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serve := ""
	if on, u := s.serveState(ctx); on {
		serve = u
	}
	return mcpPublicURL(pickPhoneAccessURL(serve, s.funnelActive(), lan))
}

func (s *server) mcpGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if s.funnelHost(r.Host) && s.funnelActive() && peerIsLoopback(r) {
			http.NotFound(w, r)
			return
		}
		s.cfgMu.Lock()
		on, want := s.cfg.McpEnabled, s.cfg.McpToken
		s.cfgMu.Unlock()
		if !on || want == "" {
			http.NotFound(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(got), "bearer ") {
			got = strings.TrimSpace(got[7:])
		} else {
			got = r.Header.Get("X-Owldrop-Token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
```

Imports: `crypto/subtle`, `context`, `net/http`, `strings`, `time`.

- [ ] **Step 4: Re-run tests — expect PASS**

Run: `go test -tags server -count=1 -run 'TestMcpURLNeverFunnelHost|TestMcpGuard' .`

- [ ] **Step 5: Commit** (if the operator asked for commits)

```bash
git add main.go mcp.go mcp_test.go
git commit -m "feat: MCP guard, URL, and config fields"
```

---

### Task 2: JSON-RPC initialize, ping, tools/list

**Files:**
- Modify: `mcp.go` — `handleMCP`
- Test: `mcp_test.go`

**Interfaces:**
- Consumes: `mcpGuard` wrapping `handleMCP`
- Produces: `func (s *server) handleMCP(w http.ResponseWriter, r *http.Request)`, `var mcpToolList []mcpTool`

- [ ] **Step 1: Failing test**

```go
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
```

- [ ] **Step 2: Run — expect FAIL** (`handleMCP` undefined)

Run: `go test -tags server -count=1 -run TestMcpInitializeAndListTools .`

- [ ] **Step 3: Implement dispatcher**

Types:

```go
type mcpReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}
```

`handleMCP`: only POST; decode `mcpReq`; switch `Method`:

- `initialize` → result `{protocolVersion, capabilities:{tools:{}}, serverInfo:{name:"owldrop", version: appVersion}}`
- `notifications/initialized` → `204`
- `ping` → `{}`
- `tools/list` → `{tools: mcpToolList}` (ten entries with inputSchema `type:object` + properties from the spec)
- `tools/call` → `s.mcpCallTool` (stub in this task: return JSON-RPC error `"not implemented"` for unknown; list tools still works)
- default → JSON-RPC `-32601`

Helper `writeRPC(w, id, result, rpcErr)`.

`mcpToolList` is a package-level slice with all ten names and descriptions (tagged-peer copy on `list_devices` / `send_file`).

- [ ] **Step 4: Tests PASS**

Run: `go test -tags server -count=1 -run TestMcpInitializeAndListTools .`

- [ ] **Step 5: Commit** `feat: MCP JSON-RPC initialize and tools/list`

---

### Task 3: Inbox tools (`list_inbox`, `save_file`, `delete_file`, `get_file`)

**Files:**
- Create: `mcp_tools.go`
- Test: `mcp_test.go`
- Reuse: `combinedInbox`, `saveOne` / `linkSave`, delete handler logic, waiting files on disk

**Interfaces:**
- Consumes: `mcpCallTool(ctx, name, args) (any, error)`
- Produces: implementations for the four inbox tools; `get_file` cap `mcpGetFileMax = 1 << 20`

- [ ] **Step 1: Failing tests**

```go
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
		t.Fatalf("no files key: %#v", out)
	}
}
```

For `get_file`, `mcpTooLarge` returns `fmt.Errorf("too_large: size %d; use save_file", size)`.

- [ ] **Step 2: Run — expect FAIL**

Run: `go test -tags server -count=1 -run 'TestMcpGetFileTooLarge|TestMcpCallListInboxEmpty' .`

- [ ] **Step 3: Implement `mcpCallTool`**

```go
const mcpGetFileMax = 1 << 20
const mcpSendFileMax = 32 << 20

func (s *server) mcpCallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "list_inbox":
		files, err := s.combinedInbox(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"files": files}, nil
	// save_file, delete_file: parse name/dir/source, call the same validation as handleSave/handleDelete
	// get_file: find file in combinedInbox; if size > mcpGetFileMax return mcpTooLarge; else read bytes, base64
	default:
		return nil, fmt.Errorf("unknown tool")
	}
}
```

Wire `tools/call` in `handleMCP`: params `{name, arguments}`, result `{content:[{type:"text", text: json.dumps(out)}]}` or `{isError:true, content:[...]}` on error.

`get_file` must read the waiting file from the daemon inbox or link-drop store the same way save does — follow `handleSave` / waiting-file path. If the file is only in the daemon, use the existing read path used by save (do not invent a second copy).

- [ ] **Step 4: Tests PASS** plus `TestMcpInitializeAndListTools`

Run: `go test -tags server -count=1 -run 'TestMcp' .`

- [ ] **Step 5: Commit** `feat: MCP inbox tools`

---

### Task 4: `list_devices` and `send_file`

**Files:**
- Modify: `mcp_tools.go`
- Test: `mcp_test.go`

**Interfaces:**
- Consumes: `tsDevicesVisible`, `isTaggedTaildropBlock`, `sendOne`, `validBaseName`
- Produces: tagged flag on devices; `send_file` rejects tagged/unavailable peers

- [ ] **Step 1: Failing test**

```go
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
}
```

- [ ] **Step 2: Run — expect FAIL** until helper exists

Run: `go test -tags server -count=1 -run TestMcpSendDenyReason .`

- [ ] **Step 3: Implement helper + tools**

`list_devices`: map each device to `{id,name,os,online,taildrop,tagged: isTaggedTaildropBlock(d.taildrop)}`.

`send_file` args: `peer` string, `name` string, `data` base64, optional `peers` []string. Decode, `validBaseName`, deny reason, `sendOne` with id `"mcp-"+newToken()`.

- [ ] **Step 4: Tests PASS**

Run: `go test -tags server -count=1 -run 'TestMcp' .`

- [ ] **Step 5: Commit** `feat: MCP send and device tools`

---

### Task 5: Sync tools

**Files:**
- Modify: `mcp_tools.go`
- Test: `mcp_test.go`
- Reuse: Sync add-text (64 KiB cap already in `sync.go`)

**Interfaces:**
- Consumes: existing Sync store add/list
- Produces: `list_sync` truncates text to 2048 runes; `post_sync` file cap `4 << 20` for MCP (stricter than 4 GiB HTTP)

- [ ] **Step 1: Failing test** — post text via `mcpCallTool("post_sync", {text:"hello"})` then `list_sync` contains hello.

Use `newServerDir` and the real Sync methods used by `handleSync` POST (find `addSyncText` or equivalent on `s.sync` / whatever the field is named in `sync.go`). Match that function name exactly when implementing.

- [ ] **Step 2: Run — expect FAIL**
- [ ] **Step 3: Implement** `list_sync` / `post_sync` (text required unless `name`+`data` provided)
- [ ] **Step 4: Tests PASS**
- [ ] **Step 5: Commit** `feat: MCP Sync tools`

---

### Task 6: Drop-link tools (must not enable Funnel)

**Files:**
- Modify: `mcp_tools.go`
- Test: `mcp_test.go`

**Interfaces:**
- Consumes: `s.drops.create`, `shareableDropURL`, `funnelEnabled`, `funnelPublicURL`, `dropBaseURL`
- Produces: `create_drop_link` / `list_drop_links`; Funnel state unchanged

- [ ] **Step 1: Failing test**

```go
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
```

- [ ] **Step 2: Run — expect FAIL**
- [ ] **Step 3: Implement**

TTL default 60, max `7*24*60`. `single` true → `max_uses=1`. Call `s.drops.create`. Local URL = `s.dropBaseURL()+"drop/"+token`. Public only if `s.funnelEnabled()`. `share_url := shareableDropURL(local, pub)`. `list_drop_links` skips revoked/expired.

- [ ] **Step 4: Tests PASS**
- [ ] **Step 5: Commit** `feat: MCP drop-link tools without enabling Funnel`

---

### Task 7: Mount HTTP routes and Settings API

**Files:**
- Modify: `server.go` (`newMux` ~337-377, `handleConfig` POST struct, `configResponse`)
- Modify: `mcp.go` — `handleMcpStatus`, `handleMcpRotate`
- Test: `mcp_test.go`

**Interfaces:**
- Consumes: `mcpGuard(s.handleMCP)`, session `guard` for `/api/mcp`
- Produces: `GET/POST /api/mcp`, `POST /api/mcp/rotate`

- [ ] **Step 1: Failing test** — httptest mux: `POST /api/mcp/rotate` with session token mints token and sets enabled if requested; `GET /api/config` has `mcpEnabled` and `mcpUrl` but not `mcpToken`.

```go
func TestConfigHidesMcpToken(t *testing.T) {
	s := newServerDir(&config{LAN: true, McpEnabled: true, McpToken: "secret"}, t.TempDir())
	s.port = 8976
	resp := s.configResponse(*s.cfg)
	if _, ok := resp["mcpToken"]; ok {
		t.Fatal("token leaked in config GET")
	}
	if resp["mcpEnabled"] != true {
		t.Fatal("mcpEnabled missing")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**
- [ ] **Step 3: Implement**

Mux:

```go
mux.HandleFunc("/mcp", s.mcpGuard(s.handleMCP))
mux.HandleFunc("/api/mcp", s.guard(s.handleMcpStatus))
mux.HandleFunc("/api/mcp/rotate", s.guard(s.handleMcpRotate))
```

`handleMcpStatus` GET: `{enabled, url, token}`. POST `{enabled:bool}`: if enabling and token empty, `s.cfg.McpToken = newToken()`; persist; if enable and neither LAN nor Serve, 400 `"turn on LAN mode or HTTPS access first"`.

`handleMcpRotate` POST: mint `newToken()`, persist, return `{mcpToken, mcpUrl, mcpEnabled}`.

`configResponse`: `"mcpEnabled": c.McpEnabled`, `"mcpUrl": s.mcpURL()` — never token.

`handleConfig` POST: optional `McpEnabled *bool` — same enable rules as `/api/mcp`.

- [ ] **Step 4: Tests PASS** + full `go test -tags server -count=1 .`
- [ ] **Step 5: Commit** `feat: mount /mcp and agent token API`

---

### Task 8: Settings UI

**Files:**
- Modify: `web/src/types.ts` — `mcpEnabled?: boolean; mcpUrl?: string`
- Modify: `web/src/api.ts` — `getMcp = () => api<{enabled:boolean;url:string;token:string}>('/api/mcp')`, `setMcpEnabled`, `rotateMcp`
- Modify: `web/src/views/Settings.tsx` — Agent access card after LAN access
- Test: `cd web && npm run typecheck`

**Interfaces:**
- Consumes: `/api/mcp`, `/api/mcp/rotate`, `copyText`
- Produces: Settings card; token never in `AppConfig` GET usage except the dedicated `getMcp` query

- [ ] **Step 1: Typecheck will fail until types/api/card exist** — add types first, then card.

Card copy (exact):

- Title: `Agent access`
- Checkbox: `Allow agents on my tailnet to send and receive via MCP`
- Body: `Agents POST to this URL with the bearer token. Public *.ts.net is drop links only.`
- Show `mcpUrl` when enabled; Copy URL; token with Copy; Rotate button confirming via toast `Token rotated — update your agent config`

Enabling with LAN off: rely on API 400 and toast the error.

- [ ] **Step 2:** `cd web && npm run typecheck` — expect PASS
- [ ] **Step 3: Commit** `feat: Settings card for MCP agent access`

---

### Task 9: Docs

**Files:**
- Modify: `README.md` — section **Agent MCP (tailnet)**
- Modify: `CHANGELOG.md` — `[Unreleased]` Added bullet
- Modify: `site/public/privacy.html` — one sentence: optional agent token never leaves the machine; tailnet agents can send/receive if enabled

README must include:

```json
{
  "mcpServers": {
    "owldrop": {
      "url": "http://100.x.x.x:8976/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Note: enable Agent access + LAN; Funnel URL will 404.

- [ ] **Step 1: Write the three doc edits**
- [ ] **Step 2:** Skim spec — every tool and non-goal still holds
- [ ] **Step 3: Commit** `docs: Agent MCP`

---

## Spec coverage

| Spec item | Task |
|---|---|
| POST `/mcp`, Funnel 404 | 1, 7 |
| Dedicated token, off by default | 1, 7 |
| Ten tools | 2–6 |
| get_file 1 MiB / send 32 MiB | 3, 4 |
| create_drop_link does not setFunnel | 6 |
| mcpUrl via pickPhoneAccessURL | 1 |
| Token not in `__CONFIG__` / config GET | 7, 8 |
| Settings card | 8 |
| README / changelog / privacy | 9 |
| stdio / WhoIs / subscriptions | none (non-goals) |

## Placeholder scan

No TBD. Tool names match the spec. `mcpGetFileMax = 1<<20`, `mcpSendFileMax = 32<<20`.
