{
  description = "owldrop dev shell + NixOS package";

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

      # nixpkgs still ships Go 1.26.5; go.mod pins 1.26.6 for the stdlib
      # security fixes (govulncheck gates the release). Override the version
      # and source so nix builds use the fixed toolchain — GOTOOLCHAIN=local
      # means nix never auto-downloads one.
      go1266 = pkgs.go_1_26.overrideAttrs (old: {
        version = "1.26.6";
        src = pkgs.fetchurl {
          url = "https://go.dev/dl/go1.26.6.src.tar.gz";
          hash = "sha256-oHIcVMaIkBRI13rZs+x+p8R0cwdV/4kTgukuy5P/LLE=";
        };
      });

      # Version from build/config.yml — single source of truth for the
      # updater's CurrentVersion (injected via ldflags).
      appVersion = builtins.head (builtins.match
        ".*version: \"([^\"]+)\".*"
        (builtins.readFile ./build/config.yml));

      # The UI is a Vite project (web/); its build output is embedded into
      # the Go binary via go:embed web/dist.
      frontend = pkgs.buildNpmPackage {
        pname = "owldrop-web";
        version = appVersion;
        src = ./web;
        npmDepsHash = "sha256-2uGD7LBkSIdGiSQ1ffg8jTgg8hEu7s2GJhCpp1XEIb4=";
      };
      # .override (not an attrset `go =`) so the goModules download phase
      # uses the same fixed toolchain — an attrset arg does not win over
      # callPackage's baked-in go_1_26 there.
      sidecar = (pkgs.buildGoModule.override { go = go1266; }) {
        pname = "owldrop";
        version = appVersion;
        src = pkgs.runCommand "owldrop-src" { } ''
          cp -r ${self} $out
          chmod -R u+w $out
          rm -rf $out/web/dist
          cp -r ${frontend}/lib/node_modules/owldrop-web/dist $out/web/dist
        '';
        # `go mod vendor` chokes on a wails embed pattern (WebView2Loader.dll
        # files aren't in the module zip; they're opt-in via build tags).
        # proxyVendor downloads the module cache instead — same result, no
        # embed resolution at fetch time.
        proxyVendor = true;
        vendorHash = "sha256-tA2Dix6JITMRC74RxNimDb9MRp4UQbewbVah8vrUz08=";
        subPackages = [ "." ];
        # drops_test.go talks to a live tailscaled daemon; not available in
        # the build sandbox (they run fine on a machine with tailscaled).
        doCheck = false;
        nativeBuildInputs = [ pkgs.pkg-config pkgs.gcc ];
        buildInputs = guiLibs;
        env.CGO_ENABLED = "1";
        buildTags = [ "production" ];
        # Carry the release version for the self-updater.
        ldflags = [ "-s" "-w" "-X main.appVersion=${appVersion}" ];
      };
    in
    {
      devShells.x86_64-linux.default = pkgs.mkShell {
        packages = [ go1266 pkgs.nodejs pkgs.pkg-config pkgs.gcc ] ++ guiLibs;
      };

      packages.x86_64-linux.default =
      # Start-menu integration merged around the FHS wrapper: the desktop
      # entry + icon make `nix profile install .#default` show Owldrop in
      # the application menu, and `nix profile upgrade owldrop` keeps it
      # current.
      let
        fhs = pkgs.buildFHSEnv {
          name = "owldrop";
          # noto-fonts is deliberate: WebKitGTK/Pango lays DejaVu Sans text at the
          # top of the line box, so the UI's font stack leads with Noto Sans and
          # the env must always provide it (not depend on host fonts).
          targetPkgs = ps: with ps; [ gtk4 webkitgtk_6_0 glib-networking gsettings-desktop-schemas dconf fontconfig dejavu_fonts noto-fonts ];
          # The FHS env is a bwrap sandbox: point fontconfig at the store config
          # so the webview picks up the bundled fonts instead of tofu-boxing.
          runScript = pkgs.writeShellScript "owldrop-run" ''
            export FONTCONFIG_FILE="${pkgs.makeFontsConf { fontDirectories = [ pkgs.dejavu_fonts ]; }}"
            exec ${sidecar}/bin/owldrop "$@"
          '';
        };
        desktopItem = pkgs.makeDesktopItem {
          name = "owldrop";
          desktopName = "Owldrop";
          comment = "Desktop app for Tailscale file sharing";
          exec = "owldrop";
          icon = "owldrop";
          terminal = false;
          categories = [ "Utility" ];
          startupWMClass = "owldrop";
        };
        iconDir = pkgs.runCommand "owldrop-icon" { } ''
          mkdir -p $out/share/icons/hicolor/512x512/apps
          cp ${./icon.png} $out/share/icons/hicolor/512x512/apps/owldrop.png
        '';
      in
      pkgs.symlinkJoin {
        name = "owldrop";
        paths = [ fhs desktopItem iconDir ];
      };

      # The raw nix-built binary (RPATHs point into the nix store, so it runs
      # in the FHS wrapper above). CI publishes this to the public
      # owldrop-install repo — a container-built binary would need the
      # Debian X11 stack the wrapper doesn't provide.
      packages.x86_64-linux.sidecar = sidecar;
    };
}
