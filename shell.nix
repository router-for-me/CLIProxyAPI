{ pkgs ? import <nixpkgs> {} }:

pkgs.mkShell {
  nativeBuildInputs = with pkgs; [ ];

  buildInputs = with pkgs; [
    go
  ];

  shellHook = ''
    go mod tidy
  '';
}
