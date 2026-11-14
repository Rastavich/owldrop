{
  description = "owldrop dev shell + NixOS package";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  # Bundled tailcat CLI (Send to anyone / Receive via tailcat): source only,
  # built below with this flake's Go and the security-patched dependency set
  # in nix/tailcat. Same tag as TailcatVersion in tools/buildtailcat.
  inputs.tailcat = {
    url = "github:tailscale/tailcat/v0.7.0";
    flake = false;
  };

  outputs = { self, nixpkgs, tailcat }:
    let
      pkgs = import nixpkgs {
        system = "x86_64-linux";
      };
      lib = pkgs.lib;
      # The app links GTK4 + WebKitGTK 6.0 via CGO; both are needed at build
      # time (pkg-config) and at runtime.
      guiLibs = with pkgs; [ gtk4 webkitgtk_6_0 ];

      # go.mod requires >= 1.27.4; nixpkgs' go_1_27 still ships an older
      # 1.27.x, so override the version and source (same pattern as the
      # 1.26.6 override in 2b16f30). GOTOOLCHAIN=local means nix never
      # auto-downloads a toolchain.
      go = pkgs.go_1_27.overrideAttrs (old: {
        version = "1.27.4";
        src = pkgs.fetchurl {
          url = "https://go.dev/dl/go1.27.4.src.tar.gz";
          hash = "sha256-c8+4VxrsDAEXdofbhxY9Rqog6XeWcncokaoXZU2aYJk=";
        };
      });

      # tailcat, built like its releases (CGO off, release tags) but on this
      # flake's patched Go and with nix/tailcat/go.{mod,sum}: tailcat v0.7.0's
      # own dependency set carries vulns fixed upstream since (see
      # securityOverrides in tools/buildtailcat; regenerate both together).
      tailcatPkg = (pkgs.buildGoModule.override { go = go; }) {
        pname = "tailcat";
        version = "0.7.0";
        src = tailcat;
        postPatch = ''
          cp ${./nix/tailcat/go.mod} go.mod
          cp ${./nix/tailcat/go.sum} go.sum
        '';
        proxyVendor = true;
        vendorHash = "sha256-QaDCH/xNMqsVYdfuC55z7dx6a8ieeLX+RP2JXSCyrA0=";
        subPackages = [ "cmd/tailcat" ];
        tags = lib.splitString "," (lib.removeSuffix "\n" (builtins.readFile "${tailcat}/build-tags.txt"));
        env.CGO_ENABLED = "0";
        ldflags = [ "-s" "-w" "-X main.version=v0.7.0" ];
        doCheck = false;
      };

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
        npmDepsHash = "sha256-OBgjG6cB3HITobCw55/E3cq8h0V//CnwkDiTjeDE+pM=";
      };
      # .override (not an attrset `go =`) so the goModules download phase
      # uses the same toolchain — an attrset arg does not win over
      # callPackage's baked-in `go` there.
      sidecar = (pkgs.buildGoModule.override { go = go; }) {
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
        vendorHash = "sha256-dnc+HkbLvF5K8ebClcMhphX0zD4QkpGU+i2mQt9iTE8=";
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
        packages = [ go pkgs.nodejs pkgs.pkg-config pkgs.gcc ] ++ guiLibs;
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
            export OWLDROP_TAILCAT="''${OWLDROP_TAILCAT:-${tailcatPkg}/bin/tailcat}"
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
        # owldrop:// share links ("Open in Owldrop" on owldrop.app/r).
        urlHandler = pkgs.makeDesktopItem {
          name = "owldrop-url";
          desktopName = "Owldrop";
          comment = "Open Owldrop share links";
          exec = "owldrop %u";
          icon = "owldrop";
          terminal = false;
          noDisplay = true;
          mimeTypes = [ "x-scheme-handler/owldrop" ];
        };
        iconDir = pkgs.runCommand "owldrop-icon" { } ''
          mkdir -p $out/share/icons/hicolor/512x512/apps
          cp ${./icon.png} $out/share/icons/hicolor/512x512/apps/owldrop.png
        '';
      in
      pkgs.symlinkJoin {
        name = "owldrop";
        paths = [ fhs desktopItem urlHandler iconDir ];
      };

      # The raw nix-built binary (RPATHs point into the nix store, so it runs
      # in the FHS wrapper above). CI publishes this to the public
      # owldrop-install repo — a container-built binary would need the
      # Debian X11 stack the wrapper doesn't provide.
      packages.x86_64-linux.sidecar = sidecar;
    };
}
