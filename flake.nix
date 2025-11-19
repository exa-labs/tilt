{
  description = "Exa Labs fork of Tilt - local development tool for Kubernetes";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        
        version = "0.36.0-exa";
        
        tilt-assets = pkgs.stdenvNoCC.mkDerivation {
          pname = "tilt-assets";
          src = "${self}/web";
          inherit version;

          nativeBuildInputs = [
            pkgs.nodejs
            pkgs.yarn-berry
          ];

          yarnOfflineCache = pkgs.stdenvNoCC.mkDerivation {
            name = "tilt-assets-deps";
            src = "${self}/web";

            nativeBuildInputs = [ pkgs.yarn-berry ];

            supportedArchitectures = builtins.toJSON {
              os = [
                "darwin"
                "linux"
              ];
              cpu = [
                "arm"
                "arm64"
                "ia32"
                "x64"
              ];
              libc = [
                "glibc"
                "musl"
              ];
            };

            NODE_EXTRA_CA_CERTS = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";

            configurePhase = ''
              runHook preConfigure

              export HOME="$NIX_BUILD_TOP"
              export YARN_ENABLE_TELEMETRY=0

              yarn config set enableGlobalCache false
              yarn config set cacheFolder $out
              yarn config set supportedArchitectures --json "$supportedArchitectures"

              runHook postConfigure
            '';

            buildPhase = ''
              runHook preBuild

              mkdir -p $out
              yarn install --immutable --mode skip-build

              runHook postBuild
            '';

            dontInstall = true;

            outputHashAlgo = "sha256";
            outputHash = "sha256-UdNvUSz86E1W1gVPQrxt5g3Z3JIX/tq8rI5E8+h20PI=";
            outputHashMode = "recursive";
          };

          configurePhase = ''
            runHook preConfigure

            export HOME="$NIX_BUILD_TOP"
            export YARN_ENABLE_TELEMETRY=0

            yarn config set enableGlobalCache false
            yarn config set cacheFolder $yarnOfflineCache

            runHook postConfigure
          '';

          buildPhase = ''
            runHook preBuild

            yarn install --immutable --immutable-cache
            yarn build

            runHook postBuild
          '';

          installPhase = ''
            mkdir -p $out
            cp -r build/. $out/
          '';

          meta = with pkgs.lib; {
            description = "Assets needed for Tilt";
            homepage = "https://tilt.dev/";
            license = licenses.asl20;
            maintainers = [ ];
            platforms = platforms.all;
          };
        };

        tilt = pkgs.buildGoModule rec {
          pname = "tilt";
          inherit version;
          src = self;

          vendorHash = null;

          subPackages = [ "cmd/tilt" ];

          ldflags = [ "-X main.version=${version}" ];

          nativeBuildInputs = [ pkgs.installShellFiles ];

          postInstall = pkgs.lib.optionalString (pkgs.stdenv.buildPlatform.canExecute pkgs.stdenv.hostPlatform) ''
            installShellCompletion --cmd tilt \
              --bash <($out/bin/tilt completion bash) \
              --fish <($out/bin/tilt completion fish) \
              --zsh <($out/bin/tilt completion zsh)
          '';

          preBuild = ''
            mkdir -p pkg/assets/build
            cp -r ${tilt-assets}/* pkg/assets/build/
          '';

          meta = with pkgs.lib; {
            description = "Local development tool to manage your developer instance when your team deploys to Kubernetes in production (Exa Labs fork)";
            mainProgram = "tilt";
            homepage = "https://github.com/exa-labs/tilt";
            license = licenses.asl20;
            maintainers = [ ];
          };
        };

      in
      {
        packages = {
          default = tilt;
          tilt = tilt;
          tilt-assets = tilt-assets;
        };

        apps.default = {
          type = "app";
          program = "${tilt}/bin/tilt";
        };
      }
    );
}
