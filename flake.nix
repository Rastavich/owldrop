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

      packages.x86_64-linux.default = pkgs.stdenv.mkDerivation {
        pname = "tailscale-drop";
        version = "0.4.0";
        src = self;
        nativeBuildInputs = [ pkgs.go pkgs.makeWrapper ];
        buildPhase = ''
          go build -o tailscale-drop .
        '';
        installPhase = ''
          mkdir -p $out/bin $out/lib/tailscale-drop
          cp tailscale-drop $out/lib/tailscale-drop/
          cp -r electron $out/lib/tailscale-drop/electron
          makeWrapper ${pkgs.electron}/bin/electron $out/bin/tailscale-drop \
            --add-flags "$out/lib/tailscale-drop/electron"
        '';
      };
    };
}
