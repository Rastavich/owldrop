{
  description = "tailscale-drop dev shell + NixOS package";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      pkgs = import nixpkgs {
        system = "x86_64-linux";
      };
      lib = pkgs.lib;
      # The app links GTK4 + WebKitGTK 6.0 via CGO; both are needed at build
      # time (pkg-config) and at runtime.
      guiLibs = with pkgs; [ gtk4 webkitgtk_6_0 ];

      # Version from build/config.yml — single source of truth for the
      # updater's CurrentVersion (injected via ldflags).
      appVersion = builtins.head (builtins.match
        ".*version: \"([^\"]+)\".*"
        (builtins.readFile ./build/config.yml));

      # The UI is a Vite project (web/); its build output is embedded into
      # the Go binary via go:embed web/dist.
      frontend = pkgs.buildNpmPackage {
        pname = "tailscale-drop-web";
        version = appVersion;
        src = ./web;
        npmDepsHash = "sha256-dHT8x8rzyRIX8n/WQ+S/fKnnVXLjFXyHSE4ZOQ+CM9I=";
      };
      sidecar = pkgs.buildGoModule {
        pname = "tailscale-drop";
        version = appVersion;
        src = pkgs.runCommand "tailscale-drop-src" { } ''
          cp -r ${self} $out
          chmod -R u+w $out
          rm -rf $out/web/dist
          cp -r ${frontend}/lib/node_modules/tailscale-drop-web/dist $out/web/dist
        '';
        # `go mod vendor` chokes on a wails embed pattern (WebView2Loader.dll
        # files aren't in the module zip; they're opt-in via build tags).
        # proxyVendor downloads the module cache instead — same result, no
        # embed resolution at fetch time.
        proxyVendor = true;
        vendorHash = "sha256-yEry1C6gzYo9weLBEucMEw5cf1h+HVPgNgj7I0c4BuQ=";
        # relay/ is a separate Go module (own go.mod) nested in the repo;
        # newer nixpkgs auto-discovers package-main dirs, so pin the build to
        # the app module root.
        subPackages = [ "." ];
        # drops_test.go talks to a live tailscaled daemon; not available in
        # the build sandbox (they run fine on a machine with tailscaled).
        doCheck = false;
        nativeBuildInputs = [ pkgs.pkg-config pkgs.gcc ];
        buildInputs = guiLibs;
        env.CGO_ENABLED = "1";
        buildTags = [ "production" ];
        # Distributed builds default to the seller's relay (the app is
        # key-less; premium is enforced server-side) and carry the release
        # version for the self-updater.
        ldflags = [ "-s" "-w" "-X main.defaultRelayURL=https://taildrop-relay.fly.dev" "-X main.appVersion=${appVersion}" ];
      };
    in
    {
      devShells.x86_64-linux.default = pkgs.mkShell {
        packages = with pkgs; [ go nodejs pkg-config gcc ] ++ guiLibs;
      };

      packages.x86_64-linux.default =
      # FHS wrapper so the CGO binary finds its dynamic libs (GTK, WebKitGTK,
      # and their transitive deps) plus GIO TLS modules and GTK settings
      # schemas at runtime — the NixOS equivalent of the old electron wrapper.
      pkgs.buildFHSEnv {
        name = "tailscale-drop";
        # noto-fonts is deliberate: WebKitGTK/Pango lays DejaVu Sans text at the
        # top of the line box, so the UI's font stack leads with Noto Sans and
        # the env must always provide it (not depend on host fonts).
        targetPkgs = ps: with ps; [ gtk4 webkitgtk_6_0 glib-networking gsettings-desktop-schemas dconf fontconfig dejavu_fonts noto-fonts ];
        # The FHS env is a bwrap sandbox: point fontconfig at the store config
        # so the webview picks up the bundled fonts instead of tofu-boxing.
        runScript = pkgs.writeShellScript "tailscale-drop-run" ''
          export FONTCONFIG_FILE="${pkgs.makeFontsConf { fontDirectories = [ pkgs.dejavu_fonts ]; }}"
          exec ${sidecar}/bin/tailscale-drop "$@"
        '';
      };

      # The raw nix-built binary (RPATHs point into the nix store, so it runs
      # in the FHS wrapper above). CI publishes this to the public
      # taildrop-install repo — a container-built binary would need the
      # Debian X11 stack the wrapper doesn't provide.
      packages.x86_64-linux.sidecar = sidecar;
    };
}
