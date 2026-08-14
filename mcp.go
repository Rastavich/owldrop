package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func mcpPublicURL(base string) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/mcp"
}

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

func (s *server) canEnableMCP(ctx context.Context, lan bool) bool {
	if lan {
		return true
	}
	on, _ := s.serveState(ctx)
	return on
}

func (s *server) mcpStatusResponse() map[string]any {
	s.cfgMu.Lock()
	enabled, token := s.cfg.McpEnabled, s.cfg.McpToken
	s.cfgMu.Unlock()
	return map[string]any{
		"enabled": enabled,
		"url":     s.mcpURL(),
		"token":   token,
	}
}

func (s *server) handleMcpStatus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.mcpStatusResponse())
	case http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.cfgMu.Lock()
		lan := s.cfg.LAN
		s.cfgMu.Unlock()
		if req.Enabled && !s.canEnableMCP(r.Context(), lan) {
			http.Error(w, "turn on LAN mode or HTTPS access first", http.StatusBadRequest)
			return
		}

		s.cfgMu.Lock()
		c := *s.cfg
		c.McpEnabled = req.Enabled
		if req.Enabled && c.McpToken == "" {
			c.McpToken = newToken()
		}
		s.cfgMu.Unlock()
		if err := c.save(); err != nil {
			writeErr(w, err)
			return
		}
		s.cfgMu.Lock()
		s.cfg.McpEnabled = c.McpEnabled
		s.cfg.McpToken = c.McpToken
		s.cfgMu.Unlock()
		writeJSON(w, s.mcpStatusResponse())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleMcpRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if r.Body != nil && r.Body != http.NoBody {
		if err := decodeJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	s.cfgMu.Lock()
	lan := s.cfg.LAN
	s.cfgMu.Unlock()
	if req.Enabled != nil && *req.Enabled && !s.canEnableMCP(r.Context(), lan) {
		http.Error(w, "turn on LAN mode or HTTPS access first", http.StatusBadRequest)
		return
	}

	s.cfgMu.Lock()
	c := *s.cfg
	c.McpToken = newToken()
	if req.Enabled != nil {
		c.McpEnabled = *req.Enabled
	}
	s.cfgMu.Unlock()
	if err := c.save(); err != nil {
		writeErr(w, err)
		return
	}
	s.cfgMu.Lock()
	s.cfg.McpToken = c.McpToken
	s.cfg.McpEnabled = c.McpEnabled
	s.cfgMu.Unlock()
	writeJSON(w, map[string]any{
		"mcpToken":   c.McpToken,
		"mcpUrl":     s.mcpURL(),
		"mcpEnabled": c.McpEnabled,
	})
}

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

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mcpObjectSchema(properties map[string]any, required ...string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var mcpToolList = []mcpTool{
	{
		Name:        "list_inbox",
		Description: "List files waiting in the inbox (Taildrop and drop links).",
		InputSchema: mcpObjectSchema(map[string]any{}),
	},
	{
		Name:        "save_file",
		Description: "Save an inbox file to disk on this machine.",
		InputSchema: mcpObjectSchema(map[string]any{
			"name":   map[string]any{"type": "string", "description": "File name in the inbox"},
			"dir":    map[string]any{"type": "string", "description": "Optional directory to save into"},
			"source": map[string]any{"type": "string", "enum": []string{"", "link"}, "description": "Inbox source: taildrop (default) or link"},
		}, "name"),
	},
	{
		Name:        "delete_file",
		Description: "Delete a file from the inbox without saving.",
		InputSchema: mcpObjectSchema(map[string]any{
			"name":   map[string]any{"type": "string", "description": "File name in the inbox"},
			"source": map[string]any{"type": "string", "enum": []string{"", "link"}, "description": "Inbox source: taildrop (default) or link"},
		}, "name"),
	},
	{
		Name:        "get_file",
		Description: "Read a small inbox file (≤1 MiB) as base64. Use save_file for larger files.",
		InputSchema: mcpObjectSchema(map[string]any{
			"name":   map[string]any{"type": "string", "description": "File name in the inbox"},
			"source": map[string]any{"type": "string", "enum": []string{"", "link"}, "description": "Inbox source: taildrop (default) or link"},
		}, "name"),
	},
	{
		Name: "list_devices",
		Description: "List tailnet devices visible in the send picker. " +
			"Do not send_file to tagged peers — install Owldrop on that box and mint a drop link there instead.",
		InputSchema: mcpObjectSchema(map[string]any{}),
	},
	{
		Name: "send_file",
		Description: "Send a file to a tailnet device via Taildrop. " +
			"Tagged peers are rejected — mint a drop link on that box instead.",
		InputSchema: mcpObjectSchema(map[string]any{
			"peer":  map[string]any{"type": "string", "description": "Target device ID"},
			"name":  map[string]any{"type": "string", "description": "File name"},
			"data":  map[string]any{"type": "string", "description": "File contents (base64)"},
			"peers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional extra device IDs"},
		}, "peer", "name", "data"),
	},
	{
		Name:        "list_sync",
		Description: "List Sync clipboard items on this machine.",
		InputSchema: mcpObjectSchema(map[string]any{}),
	},
	{
		Name:        "post_sync",
		Description: "Post text or a small file to Sync.",
		InputSchema: mcpObjectSchema(map[string]any{
			"text": map[string]any{"type": "string", "description": "Text to post (≤64 KiB)"},
			"name": map[string]any{"type": "string", "description": "Optional file name when posting a file"},
			"data": map[string]any{"type": "string", "description": "Optional file contents (base64, ≤4 MiB)"},
		}),
	},
	{
		Name:        "create_drop_link",
		Description: "Create a drop link for uploading a file to this machine. Does not enable Public access.",
		InputSchema: mcpObjectSchema(map[string]any{
			"name":        map[string]any{"type": "string", "description": "Link label / expected file name"},
			"ttl_minutes": map[string]any{"type": "number", "description": "Lifetime in minutes (default 60, max 7 days)"},
			"max_uses":    map[string]any{"type": "integer", "description": "Maximum uploads (0 = unlimited)"},
			"single":      map[string]any{"type": "boolean", "description": "Single-use link (max_uses=1)"},
		}, "name"),
	},
	{
		Name:        "list_drop_links",
		Description: "List active drop links on this machine.",
		InputSchema: mcpObjectSchema(map[string]any{}),
	},
}

func isMCPNotification(req mcpReq) bool {
	return req.JSONRPC == "2.0" && req.Method != "" && req.ID == nil
}

func isValidMCPID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '"':
		var s string
		return json.Unmarshal(raw, &s) == nil
	case 'n':
		return string(trimmed) == "null"
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		return json.Unmarshal(raw, &n) == nil
	default:
		return false
	}
}

func isValidMCPParams(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '{', '[':
		return true
	default:
		return false
	}
}

func parseMCPRequest(raw json.RawMessage) (mcpReq, *mcpRPCError) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return mcpReq{}, &mcpRPCError{Code: -32600, Message: "Invalid Request"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return mcpReq{}, &mcpRPCError{Code: -32600, Message: "Invalid Request"}
	}
	var req mcpReq
	if v, ok := fields["jsonrpc"]; ok {
		if err := json.Unmarshal(v, &req.JSONRPC); err != nil {
			return mcpReq{}, &mcpRPCError{Code: -32600, Message: "Invalid Request"}
		}
	}
	if v, ok := fields["method"]; ok {
		if err := json.Unmarshal(v, &req.Method); err != nil {
			return mcpReq{}, &mcpRPCError{Code: -32600, Message: "Invalid Request"}
		}
	}
	if v, ok := fields["id"]; ok {
		if !isValidMCPID(v) {
			return mcpReq{}, &mcpRPCError{Code: -32600, Message: "Invalid Request"}
		}
		req.ID = v
	}
	if v, ok := fields["params"]; ok {
		if !isValidMCPParams(v) {
			return mcpReq{ID: req.ID}, &mcpRPCError{Code: -32600, Message: "Invalid Request"}
		}
		req.Params = v
	}
	return req, nil
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *mcpRPCError) {
	w.Header().Set("Content-Type", "application/json")
	if rpcErr != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   rpcErr,
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 48<<20)
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeRPC(w, nil, nil, &mcpRPCError{Code: -32700, Message: "parse error"})
		return
	}
	body = bytes.TrimSpace(body)
	dec := json.NewDecoder(bytes.NewReader(body))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		writeRPC(w, nil, nil, &mcpRPCError{Code: -32700, Message: "parse error"})
		return
	}
	if dec.More() || len(bytes.TrimSpace(body[dec.InputOffset():])) > 0 {
		writeRPC(w, nil, nil, &mcpRPCError{Code: -32700, Message: "parse error"})
		return
	}
	req, rpcErr := parseMCPRequest(raw)
	if rpcErr != nil {
		writeRPC(w, req.ID, nil, rpcErr)
		return
	}
	if isMCPNotification(req) {
		switch req.Method {
		case "notifications/initialized":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, req.ID, nil, &mcpRPCError{Code: -32600, Message: "Invalid Request"})
		return
	}
	switch req.Method {
	case "initialize":
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "owldrop",
				"version": appVersion,
			},
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": mcpToolList}, nil)
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeRPC(w, req.ID, nil, &mcpRPCError{Code: -32602, Message: "invalid params"})
			return
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		out, err := s.mcpCallTool(r.Context(), params.Name, params.Arguments)
		if err != nil {
			writeRPC(w, req.ID, map[string]any{
				"isError": true,
				"content": []any{map[string]any{"type": "text", "text": err.Error()}},
			}, nil)
			return
		}
		text, err := json.Marshal(out)
		if err != nil {
			writeRPC(w, req.ID, map[string]any{
				"isError": true,
				"content": []any{map[string]any{"type": "text", "text": err.Error()}},
			}, nil)
			return
		}
		writeRPC(w, req.ID, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": string(text)}},
		}, nil)
	default:
		writeRPC(w, req.ID, nil, &mcpRPCError{Code: -32601, Message: "Method not found"})
	}
}
