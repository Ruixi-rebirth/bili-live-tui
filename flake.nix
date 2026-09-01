{
  description = "Terminal Bilibili live streaming client";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems =
        f: nixpkgs.lib.genAttrs systems (system: f { pkgs = import nixpkgs { inherit system; }; });
    in
    {
      packages = forAllSystems (
        { pkgs }:
        let
          package = pkgs.buildGoModule {
            pname = "bili-live-tui";
            version = "0.1.0";
            src = ./.;
            subPackages = [ "cmd/bili-live-tui" ];
            go = pkgs.go_1_26;
            vendorHash = "sha256-WQMlhZrtt9st+BEe39EzivpBLAG0OM3bzX/FKXhL2Qo=";
            ldflags = [
              "-s"
              "-w"
            ];
            meta.mainProgram = "bili-live-tui";
          };
        in
        {
          default = package;
          bili-live-tui = package;
        }
      );

      devShells = forAllSystems (
        { pkgs }:
        {
          default = pkgs.mkShell {
            packages = [
              pkgs.go_1_26
              pkgs.gopls
              pkgs.gotools
            ];
          };
        }
      );

    };
}
