{ pkgs ? if builtins ? getFlake
    then (builtins.getFlake "nixpkgs").legacyPackages.${builtins.currentSystem}
    else import <nixpkgs> {}
}:

let
  runtimeLibs = with pkgs; [
    libGL
    libglvnd
    libx11
    libxcursor
    libxrandr
    libxinerama
    libxi
    libxxf86vm
    wayland
    libxkbcommon
  ];
in
pkgs.mkShell {
  buildInputs = with pkgs; [
    go
    pkg-config
    git
  ] ++ runtimeLibs;

  shellHook = ''
    export CGO_ENABLED=1
    export CGO_LDFLAGS="''${CGO_LDFLAGS:-} -Wl,-rpath,/run/opengl-driver/lib"
    export LD_LIBRARY_PATH="/run/opengl-driver/lib:${pkgs.lib.makeLibraryPath runtimeLibs}''${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
  '';
}
