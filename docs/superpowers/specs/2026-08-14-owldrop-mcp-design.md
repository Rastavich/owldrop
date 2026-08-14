# Owldrop MCP (tailnet agents)

Date: 2026-08-14  
Status: approved for implementation

## Problem

People’s agents (Hermes, CI tsnet nodes, a coding agent on another tailnet device) need to send and receive files through the household drop box without opening the GTK window. The REST API exists but every agent author would reinvent auth, Funnel-vs-LAN URLs, and tool names.

## Goal

Owldrop serves a **Streamable HTTP MCP** endpoint on the tailnet so an agent can list/save/delete the inbox, send a file to a device, use Sync, and mint a drop link. Funnel never exposes this.

## Non-goals

- stdio MCP for Cursor on the same machine (same tool names later; not v1)
- WhoIs / `tag:agent` ACL (token is enough)
- MCP resources, prompts, or inbox subscriptions
- Turning Funnel on from a tool
- 1-click Vaultwarden / Hermes / other servers
- Accepting the UI session token as MCP auth

## Exposure

- Path: `POST /mcp` (JSON-RPC 2.0). GET `/mcp` is not required in v1 (POST-only Streamable HTTP).
- Same listeners as the app (loopback, LAN `:8976`, Serve HTTPS when Funnel is off).
- Existing Funnel rule still applies: loopback + MagicDNS host + Funnel active + path not `/drop/` → 404. `/mcp` is not `/drop/`, so `https://<machine>.ts.net/mcp` is a 404 while Public access is on.
- Settings copies the URL an agent should use: `pickPhoneAccessURL(...) + "mcp"` — tailnet IP when LAN is on, Serve HTTPS only when Funnel is off. Never advertise the Funnel hostname for MCP.

## Auth

- Off by default (`mcp_enabled: false`). `/mcp` returns 404 when off, even with a token.
- Dedicated token in config (`mcp_token`), minted when Agent access is first enabled, rotatable from Settings. Not the HTML session `token`.
- Every `/mcp` request (including initialize) requires `Authorization: Bearer <mcp_token>` or `X-Owldrop-Token: <mcp_token>`. Constant-time compare. Wrong/missing token → 401.
- Host/origin checks: reuse `hostAllowed` + Funnel 404 branch from `guard`, but **do not** fall back to the session token.

## Protocol

- JSON-RPC 2.0 over HTTP POST, `Content-Type: application/json`.
- `protocolVersion`: `2025-03-26`.
- Methods: `initialize`, `notifications/initialized` (ack, empty 202/no body), `tools/list`, `tools/call`, `ping`.
- Implement in-process (`mcp.go`); do not pull a large SDK unless a tiny official client is clearly smaller than a 200-line dispatcher.
- Server name: `owldrop`. Instructions string: this is the drop box on this machine; files save here; Funnel is not MCP.

## Tools

Arguments are JSON objects with snake_case keys. Failures are MCP `isError: true` text, not HTTP 500, unless the RPC itself is malformed.

| Tool | Args | Result |
|---|---|---|
| `list_inbox` | none | `{files:[{name,size,arrived,source,sender}]}` |
| `save_file` | `name`, optional `dir`, optional `source` (`""` or `"link"`) | `{path}` on the Owldrop machine |
| `delete_file` | `name`, optional `source` | `{ok:true}` |
| `get_file` | `name`, optional `source` | If size ≤ 1048576 bytes: `{name,size,encoding:"base64",data}`. If larger: `{error:"too_large",size,hint:"use save_file"}` as tool error |
| `list_devices` | none | Visible send-picker devices: `{id,name,os,online,taildrop,tagged}`. `tagged` is true when `isTaggedTaildropBlock(taildrop)`. Tool description tells the agent not to `send_file` to tagged peers — mint a drop link on **that** box instead. |
| `send_file` | `peer` (device id), `name`, `data` (base64), optional extra `peers` | `{ok:true}` after Taildrop. Reject tagged/unavailable peers with the tagged-node copy. Cap decoded size at 32 MiB. |
| `list_sync` | none | `{items:[{id,kind,text,name,size,created}]}` — text truncated to 2 KiB |
| `post_sync` | `text` and/or small file `{name,data}` (base64) | the created item. Text ≤ 64 KiB (existing Sync cap). File ≤ 4 MiB for this tool. |
| `create_drop_link` | `name`, optional `ttl_minutes` (default 60, max 7 days), optional `max_uses` (0 unlimited), optional `single` bool | `{url, public_url?, share_url, expires, token}` where `share_url = shareableDropURL(local, public)`. **Must not** call `setFunnel`. Public URL only if Funnel is already on. |
| `list_drop_links` | none | Active (not revoked/expired) links: `{name,url,share_url,expires,uses,max_uses}` |

## Settings UI

Card **Agent access**:

- Checkbox: enable (requires LAN or Serve; if neither, toast to turn on LAN).
- Read-only URL (the MCP URL).
- Token field with Copy + Rotate (rotate mints a new token and persists).
- Copy: “Agents on your tailnet POST here. Public *.ts.net is drop links only.”

## Config

```json
"mcp_enabled": false,
"mcp_token": ""
```

Persisted in `config.json` like `token`. Enabling with an empty `mcp_token` mints one. Disabling keeps the token (re-enable does not rotate).

## Frontend API

`GET /api/config` includes `mcpEnabled`, `mcpUrl` (never the token).  
`POST /api/config` accepts `mcpEnabled`.  
`POST /api/mcp/rotate` mints a new token; response `{mcpToken, mcpUrl}` once (Settings copy).  
`GET /api/mcp` (session-guarded) returns `{enabled, url, token}` so Settings can show the current token without putting it in the general config GET used elsewhere.

Do not embed `mcp_token` in `window.__CONFIG__`.

## Docs

- README: short “Agent MCP” section — enable Agent access + LAN, example Cursor/`mcp.json` or generic `{url, headers}`.
- CHANGELOG `[Unreleased]`.
- Privacy: MCP token stays on the machine; agents on the tailnet can send/receive if enabled. Not a new cloud.

## Testing

- Unit: `mcpGuard` (off→404, bad token→401, Funnel `/mcp`→404, good token+LAN host→200).
- Unit: `pickPhoneAccessURL` + `"mcp"` path never uses `https://*.ts.net/` when Funnel is on.
- RPC: `initialize` + `tools/list` returns all ten tools.
- Tools: `create_drop_link` does not set Funnel; `send_file` to a tagged peer errors with the tagged copy; `get_file` over 1 MiB errors `too_large`.
- `go test -tags server`.

## Success

An agent on another tailnet device, with the copied URL and bearer token, can send a file to a phone, save the NAS inbox, post Sync text, and mint a drop link — without opening the Owldrop window, and without Funnel exposing `/mcp`.
