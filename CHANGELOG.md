# Changelog

All notable changes to Owldrop are documented here. Versions follow
[Semantic Versioning](https://semver.org); the format is based on
[Keep a Changelog](https://keepachangelog.com).

The `release` workflow turns the newest section below into the GitHub release
notes and publishes this file to the public install repository, where
<https://owldrop.app/changelog> renders it. Keep the newest entry on top.

## [Unreleased]

### Fixed

- **Global shortcut no longer hijacks Ctrl+Shift+T** — every browser uses
  Ctrl+Shift+T to reopen a closed tab, and Owldrop registering it globally
  stole that key system-wide (reported on Linux Mint: Vivaldi and Edge both
  opened Owldrop instead). The default is now **Ctrl+Alt+O**, and the
  shortcut is configurable in Settings → Shortcuts — it re-registers live,
  no restart needed.

## [0.9.0]

### Added

- **Agent MCP (tailnet)** — Streamable HTTP MCP on `/mcp` for tailnet agents:
  inbox, send, Sync, and drop links via a dedicated bearer token (off by
  default). Public Funnel URLs do not expose MCP.

## [0.8.0]

### Added

- **Activation scoreboard** — telemetry now records `sync_item_added`,
  `drop_link_used`, and `drop_link_failed`. The stats page tracks download →
  install → first successful transfer (file, Sync, or drop-link upload) and
  14-day repeat, instead of treating daily opens as the product metric.
- **Empty inbox onboarding** — an empty inbox is a first-run, not a calm
  empty state: drop a test file, copy a drop link, paste on Sync, or scan a
  phone QR (HTTPS Serve or LAN).
- **Household drop box** — landing page and first-run copy position Owldrop
  as one always-on receive node. Tagged devices in Send explain why Taildrop
  cannot reach them and offer a drop link to *this* machine.
- **OS share / Open With** — files handed to the app (second instance, argv,
  Linux `%F`, macOS document types, Windows Send to / context menu) land on
  the Send tab to pick a device.
- **tsnet Funnel** — a tsnet node now serves public `/drop/*` links on
  `:443` when Funnel is allowed (still no Taildrop inbox).
- **Runnable builds** — CI ships a universal Mac zip (amd64 + arm64) and a
  GTK3 / WebKit2GTK 4.1 Linux package that runs on Ubuntu 24.04 LTS.

### Changed

- Creating a drop link copies the URL a recipient can actually open (Funnel
  public URL when Public access is on).
- Sync is on the marketing site as a phone→desktop scratchpad, not clipboard
  sync.
- Docker/NAS docs state the tagged-node gap and that host-socket is the
  path with a real inbox.

### Fixed

- Privacy policy wording now matches the opt-out toggle (it previously said
  opt-in in the body).

## [0.7.3]

### Added

- **Session token now persists in the config file** instead of being minted
  per process start. On Docker/NAS installs (config in the `/data` volume)
  an already-open UI keeps working across container rebuilds and updates —
  no more 403s (and re-auth) on every image update. Covers the lost-volume
  case too: the UI reloads once to pick up a freshly-embedded token when a
  mutation is refused.
- **Hidden devices** — Settings → Hidden devices lets you remove a device
  from the Send picker and the tray's quick-send menu. Hidden devices can
  still receive files; the setting persists in the config file.
- **Trusted domains for reverse proxies** — Settings → Trusted domains
  lets you serve the app at your own hostname through a reverse proxy
  (e.g. `drop.example.com`, subdomains included) while keeping the default
  DNS-rebinding protection for everything else.
- **tsnet mode now reads status from the embedded node** — with
  `OWLDROP_TSNET=1` (no host Tailscale) the app's local API client talks
  to the in-process node instead of a missing host socket, so the
  tailnet-state indicator and self MagicDNS name reflect the node itself.
  Taildrop inbox/send still need a tailscaled daemon and remain unavailable
  in tsnet mode.

### Security

- `index.html` is served with `Cache-Control: no-store` so a stale cached
  page can never keep sending a dead session token.

## [0.7.2]

### Fixed

- Windows headless builds (`GOOS=windows -tags server`) failed to compile against
  Wails v3 beta.2 (`undefined: windowsWebviewWindow`). Bumped Wails to
  `v3.0.0-beta.5`, which correctly excludes Windows webview code from server
  builds.
- Serve/Funnel LocalAPI client no longer hardcodes the Linux Tailscale socket
  path; it uses the same cross-platform `local.Client` as the rest of the app
  (fixes Serve/Funnel on Windows and other non-Linux hosts).

### Added

- `scripts/vulncheck.sh`: fails on high/critical npm advisories in `web/` and
  `site/`, and on reachable Go/Wails vulns via `govulncheck`.
- CI workflow on PRs and `main`, plus a required `vuln` job in the release
  pipeline; `scripts/bump-release.sh` runs the same check locally before
  bumping.
- Marketing site now pins `wrangler` in `site/package.json` so deploys and
  audits use a lockfile instead of floating `npx wrangler`.
- `buildWindows.sh` for a quick headless Windows cross-compile.

### Security

- Bumped `nanoid` (web transitive) and `golang.org/x/text` to clear high
  advisories caught by the new vuln gate.

### Changed

- Release packaging installs Wails CLI `v3.0.0-beta.5` to match `go.mod`.
- Stopped tracking Cloudflare Wrangler cache/state under `site/.wrangler`.

## [0.7.1]

### Added

- Update to new release banner
- Fixed last release issue

## [0.7.0]

### Added

- Tailscale Serve integration: toggle tailnet-only HTTPS on your MagicDNS name
  with automatic Let's Encrypt certificates, managed through the daemon's
  serve-config API (no CLI needed). Funnel moved onto the same config manager
  so CLI-configured funnels and the in-app toggle can never disagree.
- Per-drop-link auto-save: each link can route its uploads into a specific
  folder, even when global auto-save is off. Configured via the link row.
- Peer transport badges in the Send picker: each device shows whether the
  connection is direct or relayed, with the relay region when applicable.
- tsnet mode for headless server builds (`OWLDROP_TSNET=1`): the container
  joins the tailnet as its own node when no host `tailscaled` exists. UI, drop
  links, and Sync work without a host daemon.
- Per-drop-link upload rate limiting: each link can cap uploads per minute
  (token bucket). Rate-limited uploads return 429 with a `Retry-After` header.
  Configurable in the create-link form or via `ratePerMin` in the API.
- tsnet state persistence: the embedded node stores its state under the config
  directory so auth keys survive container restarts.
- Server build now checks save-directory writability at startup and logs a
  warning when the folder isn't usable.
- `build.sh` and updated `run.sh`: one-command build (frontend + Go).

### Fixed

- Serve-config writes use POST with `If-Match` etag (the daemon rejects PUT)
  and refetch the etag before every write so CLI-made changes aren't silently
  clobbered.

### Changed

- In-app header logo is now the owl-eyes mark, matching the tray and app icons.
- Drop-link creation API now accepts `ratePerMin` (0 = unlimited).
- MagicDNS hostnames appear in LAN URL listings for stable cross-device access.

## [0.6.5] - 2026-08-08

### Added 

- Added CI for windows build to have a signing for downloading via the microsoft store - still awaiting certificiation in Microsoft Partner Center.
- Added a new sync tab for easily copying links to and from machines (main use case is for mobile -> desktop text syncing while there is no mobile app)
- Cleaned up some old docs

## [0.6.4] - 2026-08-08

### Fixed

- Release pipeline: the changelog is no longer required for the publish step,
  so a missing file can never block a release (no user-facing changes).

## [0.6.3] - 2026-08-08

### Added

- Privacy policy and legal pages on the public site.

### Fixed

- Send tab: dropping or selecting a file did nothing — the client threw
  `ReferenceError: uuid is not defined` before any transfer started. A missing
  import, introduced by the 0.5.10 security refactor, broke file
  drop/picker/paste on every platform (report: #1).
- Frontend builds now run `tsc --noEmit` before bundling, so a missing import
  can never silently ship again.

### Changed

- Public site: the version pill is fetched from the live update manifest
  instead of being baked in, so it can no longer go stale.

## [0.6.2] - 2026-08-08

### Changed

- Docker image (`ghcr.io/rastavich/owldrop`) — the NAS/self-hosted story — is
  now the release focus alongside the desktop apps.
- CI: release artifact downloads are scoped to the expected patterns and the
  docker build-record artifact is disabled, so release housekeeping no longer
  breaks artifact downloads.

## [0.6.1] - 2026-08-08

### Security

- Trivy scan: updated vulnerable packages in the Docker image.

### Changed

- CI: dropped the `type=gha` docker cache (its cache artifact broke catch-all
  artifact downloads).

## [0.6.0] - 2026-08-08

### Added

- Headless server mode and a Docker image for NAS/self-hosted setups (mount
  `tailscaled`'s socket, open the UI from any device on your tailnet).
- Public site demo video.

### Changed

- CI builds and pushes the `ghcr.io/rastavich/owldrop` container image on every
  release.

## [0.5.10] - 2026-08-07

### Security

- Hardened local API path handling and updater checks; stricter treatment of
  risky file extensions when opening saved files.

## [0.5.9] - 2026-08-07

### Changed

- Project rename and packaging cleanup.

## [0.5.8] - 2026-08-07

### Removed

- The relay/billing integration (retired).

## [0.5.7] - 2026-08-07

No user-facing changes (build/version maintenance).

## [0.5.6] - 2026-08-05

### Fixed

- Send picker: target checkbox no longer flex-grows to half the row.

### Added

- One-command release scripting (`scripts/bump-release.sh`) for the maintainer.

## [0.5.5] - 2026-08-05

### Added

- ntfy phone notifications: ping your phone when a file is sent to it.
- Send UX: delivery confirmation and where-it-landed hints per target OS.
- Tailscale setup guidance: if the client is missing, a banner offers to
  download Tailscale.

### Changed

- Funnel ingress node hidden from the send picker.
- Relay infra updates (Railway relay URL).

## [0.5.4] - 2026-08-04

### Fixed

- Windows installer now stops a running instance first — a locked exe was
  blocking installs.

## [0.5.3] - 2026-08-04

### Added

- Self-updater: check for + install updates from Settings (Windows/macOS,
  public update feed).

## [0.5.2] - 2026-08-04

### Changed

- Relay mode enabled by default in release builds.

## [0.5.1] - 2026-08-04

### Added

- Relay mode with server-enforced premium drops + billing.

### Changed

- CI smoke-tests Windows and macOS binaries on their native runners.
- CI publishes artifacts to the public install repository.
- Regenerated platform assets (fixed bad defaults); desktop `Exec` entry fixed.

## [0.5.0] - 2026-08-03

### Added

- Single-instance lock: launching a second copy binds to the running instance
  instead of clobbering the port.

### Removed

- The AppImage release artifact (broken with bundled WebKit; .deb/.rpm and the
  Nix package remain the supported Linux options).

## [0.4.0] - 2026-08-03

Initial release.

### Added

- A desktop inbox for Tailscale's Taildrop: incoming files appear the instant
  they arrive, with one-click save/delete, auto-save to a folder, and
  drag-and-drop replying.
- Multi-device send and LAN mode (no Tailscale account needed).
- Paste-to-send (images and text), tray quick-send, keyboard shortcuts and
  hotkeys, type filters.
- Full local history with export; inbox search; batch save.
- Public drop links: short-lived upload URLs so anyone can drop files into
  your inbox; upload multiple files at once; folders arrive as one zip; funnel
  management (public URL + enable/disable toggle).
- File dialogs for saving, open-with-warning for risky files, notification
  preferences.
- Packaging for Windows, macOS, and Linux; `nix run .#tailscale-drop` on
  NixOS.
