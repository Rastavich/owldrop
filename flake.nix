{
  description = "tailscale-drop dev shell (Fyne needs cgo + GL/X11/Wayland headers)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux;
    in
    {
      devShells.x86_64-linux.default = pkgs.mkShell {
        packages = with pkgs; [
          go
          gcc
          pkg-config
          # GLFW (Fyne) X11 backend
          xorg.libX11
          xorg.libXrandr
          xorg.libXinerama
          xorg.libXcursor
          xorg.libXi
          xorg.libXext
          xorg.libXxf86vm
          xorg.libXrender
          xorg.libxcb
          xorg.xorgproto
          # GLFW (Fyne) Wayland backend
          wayland
          wayland-protocols
          libxkbcommon
          libdecor
          # OpenGL
          libGL
          mesa
        ];
      };
    };
}
