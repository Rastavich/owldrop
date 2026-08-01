{
  description = "tailscale-drop dev shell (go + nixpkgs' wrapped electron)";

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
        packages = with pkgs; [ go electron ];
      };
    };
}
