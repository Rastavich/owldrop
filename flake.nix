{
  description = "tailscale-drop dev shell";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux // {
        inherit (nixpkgs.legacyPackages.x86_64-linux)
          go
          electron;
      };
      # Allow unfree so nixpkgs' electron is usable.
      pkgs' = import nixpkgs {
        system = "x86_64-linux";
        config.allowUnfree = true;
      };
    in
    {
      devShells.x86_64-linux.default = pkgs'.mkShell {
        packages = with pkgs'; [
          go
          electron
        ];
      };
    };
}
