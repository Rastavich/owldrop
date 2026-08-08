# Changelog

All notable changes to Owldrop are documented here. Versions follow
[Semantic Versioning](https://semver.org); the format is based on
[Keep a Changelog](https://keepachangelog.com).

The `release` workflow turns the newest section below into the GitHub release
notes and publishes this file to the public install repository, where
<https://owldrop.app/changelog> renders it. Keep the newest entry on top.

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
