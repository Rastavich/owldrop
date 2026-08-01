{
  description = "tailscale-drop dev shell + NixOS package";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      pkgs = import nixpkgs {
        system = "x86_64-linux";
        config.allowUnfree = true; # nixpkgs' electron is flagged unfree
      };
    in
    {
      devShells.x86_64-linux.default = pkgs.mkShell {
        packages = with pkgs; [ go nodejs electron ];
      };

      packages.x86_64-linux.default = let
        sidecar = pkgs.buildGoModule {
          pname = "tailscale-drop";
          version = "0.4.0";
          src = self;
          vendorHash = "sha256-MIX6mGbnIQ2YL+GLFS8WUsGdkCbzT6p1eSSo8iVc3fA=";
          ldflags = [ "-s" "-w" ];
        };
      in
      pkgs.stdenv.mkDerivation {
        pname = "tailscale-drop";
        version = "0.4.0";
        src = self;
        nativeBuildInputs = [ pkgs.makeWrapper ];
        installPhase = ''
          mkdir -p $out/bin $out/lib/tailscale-drop
          cp ${sidecar}/bin/tailscale-drop $out/lib/tailscale-drop/
          cp -r electron $out/lib/tailscale-drop/electron
          makeWrapper ${pkgs.electron}/bin/electron $out/bin/tailscale-drop \
            --add-flags "$out/lib/tailscale-drop/electron"
        '';
      };
    };
}
