# Wails v3 as an Electron replacement for tailscale-drop

**Date:** 2026-08-03 · **Scope:** desktop shell evaluation only (research deliverable, no build/test run)
**Method:** primary sources only — https://v3.wails.io/ docs, `github.com/wailsapp/wails` master source + releases, official announcement. Every claim cites its source; `[INFERENCE]`/`[ESTIMATE]` marks anything derived rather than documented. Doc paths that 404 on the live site are cited via their repo path under `docs/src/content/docs/…`.

---

## 1. Status: current release, v3 stability, v2→v3 changes

**Latest release: `v3.0.0-beta.2`, tagged 2026-08-02** (released "02 Aug 15:26" by leaanthony; labelled **Pre-release**; "automated nightly release generated from the latest changes on master"). Install: `go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.2`.
— https://github.com/wailsapp/wails/releases/tag/v3.0.0-beta.2

**v3 is still beta in mid-2026. There is no stable/GA v3 release.** The docs homepage states "Wails v3 is currently in beta" (https://v3.wails.io/); the README version table lists v2 as "Stable" and v3 as "Beta" (https://github.com/wailsapp/wails#readme). The official announcement (2026-08-02, "Wails v3 Beta: a new foundation for Go desktop applications") says: "This is a beta release, not the final 3.0 release. The desktop API is stable and teams are already using v3 in production, but you should test thoroughly before deploying… Wails v2 remains the current stable release and will continue to receive fixes." (repo: `docs/src/content/docs/blog/2026-08-02-wails-v3-beta.md`). v3.0.0-beta.2 itself carries the note "The API is stable, but you may still encounter issues before the final 3.0 release."

**"The new release of wails3"** = this v3 beta milestone: the first v3 alpha tag was published **18 January 2023**, and the project stayed in alpha for ~3.5 years before promoting to beta; the beta announcement + v3.0.0-beta.2 (both 2026-08-02) are the "new release" event users are hearing about. `[INFERENCE: only major v3 release event in the Aug 2026 news cycle]`

**Big v2→v3 changes** (https://v3.wails.io/migration/v2-to-v3/ and the announcement):

| Area | v2 | v3 |
|---|---|---|
| Lifecycle | single `wails.Run(&options.App{…})` | explicit `application.New(Options)` → `app.Window.NewWithOptions(…)` → `app.Run()`; multi-window first-class |
| Bindings | context-bound structs (`ctx` + `startup(ctx)`), reflection-generated | standalone **services** with static source analysis; richer TypeScript (comments, parameter names); generated into `bindings/<app>/<service>` |
| Runtime | `runtime.WindowSetTitle(ctx,…)` global functions | methods on objects (`window.SetTitle`, `app.Event.Emit`) |
| Events | variadic `interface{}`, context required | typed `app.Event.On/Emit` with `*CustomEvent` |
| Build | hidden `wails build` pipeline | visible, editable Taskfile-based build (`wails3 task …`), Docker/Zig cross-compile |
| Server mode | — | `-tags server`: same app runs as HTTP server, no window (browser clients, WebSocket events) |
| Linux stack | WebKit2GTK 4.1 | **GTK4 + WebKitGTK 6.0** default; GTK3 tag legacy |
| Mobile | — | experimental iOS/Android |

---

## 2. Runtime model: OS webviews, versions, distribution, sizes

**Webviews:** Windows = **WebView2** (Edge/Chromium; "Pre-installed on Windows 10/11, Automatic updates via Windows Update"), macOS = **WebKit** ("built into macOS"), Linux = **WebKitGTK** ("installed via package manager"). No bundled browser ("Unlike Electron, Wails doesn't bundle a browser—it uses the operating system's native WebView"). — `docs/src/content/docs/concepts/architecture.mdx`

**Linux minimum versions (matters for old distros):** "Wails v3 requires **WebKitGTK 6.0** by default. Distributions that ship only WebKit2GTK 4.1 — Ubuntu 22.04 LTS, Debian 12, Fedora ≤ 39, RHEL 9.x — must build with the legacy `-tags gtk3` opt-in. Older releases that ship only WebKit2GTK 4.0 (Ubuntu 20.04, Debian 11, RHEL 8) are **not supported**." The legacy GTK3/WebKit2GTK 4.1 path "is supported through the v3.0.x line and will be **removed in v3.1**." — https://v3.wails.io/quick-start/installation (Linux tab), https://v3.wails.io/guides/build/linux/ (Legacy GTK3 Support)

**WebView2 runtime distribution on Windows: not bundled with the app.** "WebView2 Runtime (usually pre-installed) — Windows 10/11 includes WebView2 by default. If missing: download from Microsoft, or run `wails3 doctor`" (https://v3.wails.io/quick-start/installation). The NSIS installer "includes a **WebView2 bootstrapper that downloads the runtime if needed**. If you need offline installation, download the Evergreen Standalone Installer from Microsoft." (https://v3.wails.io/guides/build/windows/) — i.e. the Wails binary itself stays small; the runtime is an OS-level dependency.

**NixOS implications:** Linux builds need CGO against the distro webkit/GTK dev libraries. Docs give the dev-shell recipe `buildInputs = with pkgs; [ webkitgtk_6_0 gtk4 pkg-config gcc ];` and mention `wails3 doctor` reports exact packages per distro (https://v3.wails.io/quick-start/installation, NixOS tab). At *runtime* the app links the system WebKitGTK 6.0 (nixpkgs `webkitgtk_6_0`), so a NixOS flake would wrap the binary with that lib rather than Electron. `[INFERENCE: runtime linkage follows from the CGO/WebKitGTK requirement; not separately documented]`

**Size / RAM (official Wails claims, not independently measured):** "~15MB binaries vs Electron's 150MB; ~10MB baseline memory vs 100MB+; <0.5s startup time vs 2-3s" (https://v3.wails.io/ "Why Wails?"; identical numbers in `concepts/architecture.mdx`). Mark these as vendor-published estimates for the *framework shell*; tailscale-drop's own binary already embeds the sidecar + UI, so real numbers must be measured after the port. `[ESTIMATE]`

---

## 3. Feature parity for THIS app (tray, notifications, shortcut, clipboard, dialogs, drag & drop, single instance, close-to-tray)

All checked against v3 docs/source (beta.2 era):

| Electron `main.js` feature today | Wails v3 equivalent | Source |
|---|---|---|
| Tray icon + dynamic device submenu, icon/label, quit | `app.SystemTray.New()`, `SetIcon`/`SetLabel`/`SetTooltip`, menus with **submenu, checkbox, radio, disabled** items, dynamic updates (`menu.Update()`, full rebuild via `systray.SetMenu(newMenu)`), `OnClick/OnRightClick/OnDoubleClick`, `Show/Hide`; Linux uses StatusNotifierItem (GNOME needs a tray extension) | `docs/src/content/docs/features/menus/systray.mdx` |
| Native notifications driven by sidecar SSE | `notifications` service: `SendNotification`, action buttons/reply categories, `OnNotificationResponse`, `ThreadID` grouping, `InterruptionLevel`, scheduled delivery; macOS needs user authorization, Win/Linux always authorized | `docs/src/content/docs/features/notifications/overview.mdx` |
| Global shortcut Ctrl+Shift+T | `app.GlobalShortcut.Register("CmdOrCtrl+Shift+T", cb)` + `Unregister/IsRegistered/GetAll`; native per platform (mac Carbon hotkeys, Win32 `RegisterHotKey`, X11 grab, **Wayland via XDG portal** — compositor may re-map keys) | `docs/src/content/docs/features/keyboard/global-shortcuts.mdx` |
| Clipboard read/write | `app.Clipboard.SetText(text)` / `Text()` | `docs/src/content/docs/features/clipboard/basics.mdx` |
| File open dialog + folder picker (tray quick-send) | `app.Dialog.OpenFileWithOptions(&OpenFileDialogOptions{…})` with `CanChooseFiles`/`CanChooseDirectories`/`AllowsMultipleSelection`, `PromptForSingleSelection/MultipleSelection`; source confirms options incl. folder selection | migration guide (dialogs mapping); `v3/pkg/application/dialogs.go` |
| Drag & drop file send | `EnableFileDrop: true` window option; `events.Common.WindowFilesDropped` event → absolute `DroppedFiles()` paths; optional `data-file-drop-target` zones | `docs/src/content/docs/features/drag-and-drop/files.mdx` |
| Single-instance lock | `SingleInstance: &SingleInstanceOptions{UniqueID, OnSecondInstanceLaunch, AdditionalData}`; named mutex on Win/mac, DBus on Linux; optional AES-256-GCM between instances | `docs/src/content/docs/guides/single-instance.mdx` |
| Close-to-tray, summon via tray/shortcut | `window.Hide()`/`Show()` (hide ≠ close); `Hidden: true` window option; `systray.AttachWindow(window)`; macOS: leave `Mac.ApplicationShouldTerminateAfterLastWindowClosed` unset (default false) so hiding the last window doesn't quit | `docs/src/content/docs/features/menus/systray.mdx`, `docs/src/content/docs/features/keyboard/global-shortcuts.mdx` (macOS caveat), `v3/pkg/application/webview_window_options.go` (`Hidden`) |
| App running in background, no window | documented pattern: create hidden window + tray at startup; also `-tags server` headless mode (no GUI deps at all) | `features/menus/systray.mdx` (hidden-window example), `docs/src/content/docs/guides/server-build.mdx` |

No Electron feature is missing from the v3 API set. One caveat: `systray.AttachWindow`'s popup auto-hide has platform "smart defaults" and Linux focus-follows-mouse quirks are handled by Wails (beta.2 release notes + systray doc). `[verified against docs; per-platform behavior still needs on-device testing]`

---

## 4. Frontend model: static HTML, external URL loading, IPC, REST+SSE

**Plain static frontend — yes, no framework required, but the default workflow assumes a build step producing `frontend/dist`.** "The frontend of a Wails app is **just a web project** — anything that builds to static HTML/CSS/JS will work." The only contract: `frontend/dist/` is embedded via `//go:embed all:frontend/dist` and served by the asset server; `wails3 build` runs `frontend/package.json`'s `build` script; bindings are generated into `frontend/bindings/`. Templates shipped: `vanilla` (TS), `vanilla-js`, `react`, `react-js`, `vue`, `svelte` — all Vite-based (https://v3.wails.io/guides/dev/frontend-frameworks/). There is no documented "zero build tooling" mode, but the asset server serves *any* `embed.FS` (`AssetFileServerFS(assets)`, migration guide), so tailscale-drop's existing single-file `web/index.html` can be embedded as-is, with the build script replaced by a copy step — or skipped entirely. `[INFERENCE: docs only describe the dist/ + build-script path; nothing in the asset-server API requires a bundler]`

**External URL loading: supported.** `WebviewWindowOptions.URL` ("URL is the URL to load in the window", `v3/pkg/application/webview_window_options.go`) and `window.SetURL(url)` — "Navigate to external URL (if allowed)" (https://v3.wails.io/reference/window/). `urlvalidator.go` (source) permits `http`/`https` with a host and rejects `javascript`/`data`/`file`/`ftp` and shell metachars — so `SetURL("http://127.0.0.1:8976")` is allowed at the framework level.

**IPC model:** v3 = **services** (plain Go structs with exported methods, registered via `application.NewService`) + auto-generated **bindings** (async TS/JS SDK) + typed **events** (`app.Event.Emit`/`Events.On`, JS side `@wailsio/runtime`). Default transport is **HTTP fetch from the page to `/wails/runtime`** on the same origin as the page (`docs/src/content/docs/guides/custom-transport.mdx`, `v3/pkg/application/transport_http.go` — the transport is a middleware mounted around the asset server). You can replace it with a custom `Transport`/`AssetServerTransport` (WebSocket, gRPC, …). The architecture doc stresses "in-memory IPC. No network ports" for the standard model (https://v3.wails.io/ "Why Wails?").

**Can the existing REST API + SSE model be kept as-is? Yes — it doesn't even need Wails IPC.** Two viable architectures:

1. **Single binary (recommended):** the Go sidecar's existing net/http server (port 8976, REST + SSE, embedded `web/index.html`, LAN mode) is started inside the same process as the Wails app; the webview loads `http://127.0.0.1:8976/`. The UI is byte-for-byte the current page (fetch + EventSource), zero frontend changes. Native features are driven from **Go** (SSE events → `notifications.SendNotification` in-process; tray menu rebuilt from the same device state; `GlobalShortcut` callback → `window.Show()`), not from Electron-main JS. Single-instance, clipboard and dialogs come from `application.Options`.
2. **Keep the shell separate** (sidecar process + Wails window loading its URL) — also possible, but gives up the single-binary benefit.

Caveats for architecture 1 (documented behavior, needs on-device confirmation):
- Wails' own JS runtime/bindings are served at `/wails/runtime` **on the page's origin** (`transport_http.go`). On a sidecar-served page, Wails bindings/events from JS would POST to `127.0.0.1:8976/wails/runtime`, which the sidecar does not serve — so **don't use Wails bindings in the UI**; keep REST+SSE (which is the plan anyway). `[INFERENCE from transport mounting, not an explicit doc statement]`
- Native file **drag & drop** events (`WindowFilesDropped`) are OS-level and still fire, but the optional `data-file-drop-target` drop-zone detection relies on injected JS; behavior on an externally-loaded page should be verified, or drops accepted window-wide. `[INFERENCE; flagged as a spike item]`
- CORS: none needed, the page and its fetch targets share origin `127.0.0.1:8976`.
- **LAN mode is unaffected**: it is just the sidecar's HTTP server; Wails is irrelevant to browser clients. Bonus: v3's `-tags server` mode (`guides/server-build.mdx`) shows the same Go app can run fully headless with browser clients + WebSocket events if you ever want a non-webview deployment.

---

## 5. Build & release: CLI, cross-compilation, installers, CI, signing

**CLI workflow:** `wails3 init -n myapp` → `wails3 dev` (hot reload, Go auto-rebuild, Vite proxied on `WAILS_VITE_PORT` 9245) → `wails3 build` → `wails3 package GOOS=…` / `wails3 sign GOOS=…`; everything else via explicit Taskfile tasks (https://v3.wails.io/quick-start/first-app/, `docs/src/content/docs/guides/cli.mdx`).

**Cross-compilation feasibility (from Linux, this project's CI case)** — `docs/src/content/docs/guides/build/cross-platform.mdx`:

| Host → Target | Windows | macOS | Linux |
|---|---|---|---|
| Linux | **Native Go, no CGO** (nothing extra needed) | **Docker** (`wails3 task setup:docker`, `wails-cross` image = Zig + macOS SDK from `wailsapp/macosx-sdks`) | Native (needs gcc/clang; falls back to Docker) |

- Windows is the easy one: "Windows is the simplest cross-compilation target because it doesn't require CGO by default… works from any host OS with no additional setup." (CGO deps like tailscale.com's are fine since `CGO_ENABLED=0` builds already work for this repo.)
- macOS and Linux **require CGO** (WebView integration); macOS SDK is **not distributed by Wails** — the Docker image downloads it from `wailsapp/macosx-sdks`, "Users are responsible for reviewing Apple's SDK license terms" (cross-platform.mdx).
- Universal macOS binaries can be built from any platform via `wails3 task darwin:package:universal` (uses `wails3 tool lipo`) (https://v3.wails.io/guides/build/macos/).
- ARM64 for all three platforms (Linux ARM64 from x86_64 uses Docker).

**Installers (built into the template, not plugins):**
- Linux: `wails3 package GOOS=linux` → **AppImage, DEB, RPM, Arch/AUR**; nfpm config + PGP signing tasks (https://v3.wails.io/guides/build/linux/).
- Windows: `wails3 package GOOS=windows` → **NSIS installer** (with WebView2 bootstrapper) + optional **MSIX**; `info.json` version resources, signing via `windows:sign` (https://v3.wails.io/guides/build/windows/).
- macOS: `.app` bundle, **DMG** (`wails3 task darwin:package:dmg`; DMG creation only on macOS), **notarization** (`darwin:sign:notarize` via `notarytool`), entitlements (https://v3.wails.io/guides/build/macos/). Cross-compiled macOS binaries are unsigned — "Transfer to a Mac and sign before testing."

**CI / GitHub Actions:** there is **no official Wails GitHub Action or ready-made workflow template** (checked the `wailsapp` org's 18 public repos, 2026-08-03 — none is an action/CI template repo `[INFERENCE from absence]`). The practical pattern, per the cross-platform doc, is: run `wails3 build/package` on GitHub-hosted runners (Linux runner can emit all targets via the Docker image; DMG needs a macOS runner). The Wails repo's own workflows (`release-v3.yml`, `nightly-release-v3.yml`, `build-cross-image.yml`, `cross-compile-test-v3.yml` in `.github/workflows/`) are working references. v3 also ships a **built-in self-updater with a GitHub Releases provider** (asset matcher per platform/arch, installer-exclusion, checksums) — `docs/src/content/docs/guides/updater-github-release-assets.mdx` — which Electron doesn't give you for free.

---

## 6. Migration cost for tailscale-drop

**What's reusable (the bulk):**
- `web/index.html` + all UI JS: **unchanged** (same REST + SSE against the same sidecar routes, whether loaded from the asset server or from `http://127.0.0.1:8976`).
- Sidecar Go code (`server.go`, tailscale/funnel logic): **unchanged**; it becomes the Wails process's own HTTP server (already `CGO_ENABLED=0`-clean and cross-platform).
- `electron/main.js` maps almost 1:1 (see §3 table).

**What gets rewritten (small):**
- A new Go main: `application.New` + window (`URL` or embedded assets) + tray/menu + services (notifications, tray rebuild, quick-send), `SingleInstance`, `GlobalShortcut`, close→`Hide()`.
- The Electron-main notification/SSE consumer becomes a Go SSE consumer → `notifications.SendNotification` (in-process, no port dance, no IPC).
- The tray quick-send "token from the page" hack (Electron `executeJavaScript`) disappears: Go owns both the HTTP server and the tray, so the send token lives in Go state.
- Packaging: replace electron-builder (AppImage/deb/dmg/NSIS config) with the generated `build/{linux,windows,darwin}` Taskfile assets; NixOS flake swaps `nixpkgs.electron` for wrapping the Go binary with `webkitgtk_6_0`.

**Effort estimate:** shell port ~2–5 dev-days (tray/menu/notifications/shortcut wiring + packaging + CI), plus a half-day spike for drag & drop on the externally-loaded page (the one `[INFERENCE]` risk). Frontend work: none. `[ESTIMATE]` The official v2→v3 migration guide quotes "1–4 hours for typical applications" — that's for apps already on Wails v2, not an Electron→Wails port; this app is a *different* kind of migration, dominated by the shell rewrite above.

---

## 7. Risks

- **Beta status.** "The desktop API is stable," but "test thoroughly before deploying" (announcement). v3.0.0-beta.2 is an automated **nightly** release; expect frequent, possibly behavior-changing betas until 3.0, and v2 remains the "current stable release" (announcement; README). `[INFERENCE: nightly cadence implies higher change velocity]`
- **GTK3 support window.** Old-but-common distros (Ubuntu 22.04, Debian 12) are on WebKit2GTK 4.1 only; Wails supports them via `-tags gtk3` **through the v3.0.x line only — removed in v3.1** (installation + linux packaging docs). If users run those distros, pin to v3.0.x or require newer distros.
- **Linux desktop variance.** Tray depends on StatusNotifierItem (GNOME needs an extension); global shortcuts on Wayland go through the XDG portal (compositor may choose different keys) (systray + global-shortcuts docs).
- **Webview quirks (known, actively patched).** beta.2 fixed a "Linux WebKit crash when sending Blob or FormData in fetch requests" and a WebView2 body-size limit (2MB) is worked around with request chunking in `transport_http.go`; NVIDIA + WebKitGTK DMA-BUF blank-window bug is auto-mitigated (`WEBKIT_DISABLE_DMABUF_RENDERER=1`) (beta.2 release notes; linux packaging guide). Each OS webview is a moving target (WebKitGTK 6.0 is new-ish).
- **Plugin ecosystem: none yet.** "A general plugin system is not part of this beta" (announcement); services can bundle assets/scripts, which is the foundation. Anything Electron-ecosystem you rely on (auto-update is covered; others) must be hand-rolled.
- **Project concentration.** 35.7k+ stars, high activity (nightly v3 + weekly v2 releases), but core work is heavily concentrated in the lead maintainer (`leaanthony` authored beta.2, the announcement acknowledges governance growing pains). Single-maintainer bus-factor is a real consideration for a load-bearing dependency. `[INFERENCE]`
- **License: MIT** — no licensing blocker (`LICENSE`).

---

## 8. Verdict

**For this app — Go + static single-file web UI, needs tray/notifications/shortcut/clipboard/drag-drop, wants small bundles and CI releases for Win/mac/Linux — Wails v3 is a credible Electron replacement as of mid-2026, with one timing caveat (it is beta, not GA).** The fit is unusually good because this app does not actually need a browser engine: its UI is a static page talking REST+SSE to its own Go server, and every Electron `main.js` capability has a documented, native Wails equivalent. The sidecar and `web/index.html` survive the migration untouched.

### Comparison: Electron (current) vs Wails v3 (proposed)

| Dimension | Electron (today) | Wails v3 (beta.2, 2026-08-02) |
|---|---|---|
| Runtime | Bundled Chromium + Node.js | OS webview (WebView2 / WKWebView / WebKitGTK 6.0) |
| Bundle size (shell) | ~150MB vendor claim | ~15MB vendor claim `[ESTIMATE until measured]` |
| RAM baseline | 100MB+ vendor claim | ~10MB vendor claim `[ESTIMATE until measured]` |
| Startup | 2–3s vendor claim | <0.5s vendor claim |
| UI for this app | `BrowserWindow` → sidecar URL | `WebviewWindowOptions.URL` → same URL; or embed `web/index.html` directly |
| Tray + dynamic menu | `Tray`/`Menu`, custom code | `app.SystemTray` + menus, `menu.Update()`/rebuild — 1:1 |
| Notifications | Electron `Notification` fed by SSE (main.js) | Go service `notifications.SendNotification` fed by SSE in-process — 1:1 |
| Global shortcut | `globalShortcut` | `app.GlobalShortcut` (Wayland via portal) — 1:1 |
| Clipboard | electron `clipboard` | `app.Clipboard` — 1:1 |
| Dialogs (file/folder) | electron `dialog` | `app.Dialog.OpenFileWithOptions` — 1:1 |
| Drag & drop send | HTML5 in page + main-process handling | `EnableFileDrop` + `WindowFilesDropped` native event (verify on external URL) |
| Single instance | `app.requestSingleInstanceLock()` | `SingleInstanceOptions` (mutex/DBus, optional encryption) — 1:1 |
| Close-to-tray / background | `app.on('window-all-closed')` handling | `window.Hide()` + `ApplicationShouldTerminateAfterLastWindowClosed:false` — 1:1 |
| Frontend rewrite needed | — | **none** (same REST+SSE page) |
| Cross-compile from Linux | needs Wine/matrix runners for NSIS/dmg | Windows: native Go; macOS/Linux: Docker (`wails-cross`); DMG on macOS runner |
| Installers | electron-builder (NSIS/dmg/AppImage/deb) | built-in `wails3 package` (NSIS/MSIX, dmg, AppImage/deb/rpm/AUR) + signing/notarization tasks |
| Self-update | manual/electron-updater | built-in updater with GitHub Releases provider |
| CI templates | electron-builder docs/actions | no official action; copy Wails' own workflows; tasks run on any runner `[INFERENCE]` |
| NixOS | wrap nixpkgs electron | wrap binary with `webkitgtk_6_0` (+ `-tags gtk3` on old stacks) |
| Status | stable, mature | **beta** (stable API, nightly releases; v2 still the stable line) |
| Linux support floor | any distro (bundled) | WebKitGTK 6.0 default; 4.1 via `-tags gtk3` (until v3.1); **4.0 unsupported** |
| License | MIT | MIT |

### Strongest argument for Wails v3
One small Go binary replaces the Electron shell + Node runtime + its packaging stack; every native feature this app uses is a documented, cross-platform Wails API; the existing sidecar and UI are reused verbatim (architecture 1: single process, LAN mode intact); CI for Win/mac/Linux is realistic from Linux runners; plus a built-in updater — while cutting the per-install footprint by roughly an order of magnitude.

### Strongest argument against (i.e., for staying on Electron)
Wails v3 is **not yet a stable release**: the desktop API is promised stable but the project is shipping nightly betas, v2 is still the "current stable" line, the GTK3 fallback disappears in v3.1, and the Linux experience carries real variance (tray extensions, Wayland shortcut portal, WebKitGTK 6.0 bugs like the NVIDIA blank-window case). For a production app shipping today, the conservative move is to wait for 3.0 GA (or pin a specific v3.0.x beta and test hard) — the migration itself is cheap enough to defer without much cost.

**Bottom line:** Wails v3 is the most credible Electron replacement available for this codebase — but adopt it on a **pinned beta** with a spike on drag & drop over the external URL and per-platform smoke tests, and treat GA as the trigger for a wider rollout. Nothing about this app's architecture argues for staying with Electron once v3 is stable.

---

*Sources: https://v3.wails.io/ (quick-start/installation, migration/v2-to-v3, guides/build/{linux,windows,macos}, guides/dev/frontend-frameworks, guides/custom-transport, guides/server-build, guides/single-instance, guides/updater-github-release-assets, reference/window, reference/application) · github.com/wailsapp/wails: releases/tag/v3.0.0-beta.2, README, LICENSE, `.github/workflows/`, `docs/src/content/docs/{blog/2026-08-02-wails-v3-beta.md, concepts/architecture.mdx, features/{menus/systray.mdx, notifications/overview.mdx, keyboard/global-shortcuts.mdx, clipboard/basics.mdx, drag-and-drop/files.mdx}, guides/build/cross-platform.mdx}`, `v3/pkg/application/{webview_window_options.go, urlvalidator.go, transport_http.go, dialogs.go}` · github.com/wailsapp org repo list (18 repos, checked 2026-08-03).*
