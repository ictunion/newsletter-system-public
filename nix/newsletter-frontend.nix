{ buildNpmPackage
, lib
, src
  # VITE_* vars are baked into the bundle at build time — a runtime .env has no effect.
  # No defaults: callers must pass them explicitly.
, keycloakUrl
, keycloakRealm
, keycloakClientId
  # Listmonk admin UI as admins' browsers reach it. Optional: without it, list IDs are not links.
, listmonkUrl ? ""
}:
buildNpmPackage {
  pname = "newsletter-frontend";
  version = "0.1.0";
  inherit src;

  npmDepsHash = "sha256-sln3uky5gVLOsTjMG2z1bJsPvpWJa1ed1kCdlnmHIfs=";

  env = {
    VITE_KEYCLOAK_URL = keycloakUrl;
    VITE_KEYCLOAK_REALM = keycloakRealm;
    VITE_KEYCLOAK_CLIENT_ID = keycloakClientId;
    VITE_LISTMONK_URL = listmonkUrl;
  };

  installPhase = ''
    mkdir -p $out/var/www
    cp -r dist/* $out/var/www
  '';
}
