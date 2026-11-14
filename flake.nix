{
  description = "newsletter-system Go API dev shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            golangci-lint
            sqlc
            goose
            postgresql
            go-swag

            # frontend/ (React + Vite) — bundles npm
            nodejs_22
          ];

          shellHook = ''
            export GOPATH="$PWD/.gopath"
            export PATH="$GOPATH/bin:$PATH"
          '';
        };

        packages = rec {
          newsletter-api = pkgs.callPackage ./nix/newsletter-api.nix {
            src = self;
          };

          newsletter-frontend = pkgs.callPackage ./nix/newsletter-frontend.nix {
            src = "${self}/frontend";
            # Local dev values (see frontend/.env.example).
            # Deployments must override these with `newsletter-frontend.override { ... }`.
            keycloakUrl = "http://localhost:8180";
            keycloakRealm = "members";
            keycloakClientId = "newsletter-system";
          };

          default = newsletter-api;
        };

      });
}
