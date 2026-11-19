# Daemon + CLI binaries, and the dashboard SPA derivation (part of the
# homelabPkgs set, nix/pkgs/default.nix).
# vendorHash / npmDepsHash are updated automatically by scripts/update-hashes.sh.
{
  pkgs,
  bcduSrc ? null,
}:
let
  final = pkgs;
  # The dashboard's npm deps hash (update-hashes.sh patches it by name).
  dashboardNpmDepsHash = "sha256-hGapl/71sw6sOHarUHbszNtdmuNYZMf3v5gsQxvdC1w=";
  vendorHash = "sha256-FwkDGmK3kN2aQ+53P+f48AGvd7V7RxCB3OV1AmaLlPM=";

  # The daemon's module with the backup runner's next to it: the daemon
  # imports the runner's `runfile` package (the spec and state format) through
  # a local `replace ../backup-runner`, so both have to be in the source.
  daemonSrc = final.lib.cleanSourceWith {
    src = ../../apps;
    filter =
      path: _type:
      let
        rel = final.lib.removePrefix (toString ../../apps + "/") path;
        inCore = final.lib.removePrefix "core/" rel;
      in
      rel == "core"
      || rel == "backup-runner"
      || final.lib.hasPrefix "backup-runner/" rel
      || (
        final.lib.hasPrefix "core/" rel
        && !(final.lib.hasPrefix "result" inCore)
        && !(final.lib.hasPrefix "docs" inCore)
        && !(final.lib.hasPrefix ".git" inCore)
      );
  };

  goArgs = {
    src = daemonSrc;
    modRoot = "core";
    # The module cache, not a vendor tree: a vendor tree would hold a copy of
    # the runner's runfile package, and the hash would change with every
    # edit of it. This way it changes with go.mod and go.sum only.
    proxyVendor = true;
    inherit vendorHash;
    postPatch = "rm -rf core/vendor";
    nativeBuildInputs = [ ];
    # The packages' own version, not a release's: the same build ships in
    # every release, and the daemon reads the release's version from the name
    # of the core extension in use (internal/system).
    version = "0.2.0";
  };

  # The backup runner (apps/backup-runner): its own Go module with nothing
  # outside the standard library, so there is no vendor hash to keep. Static,
  # and pinned to the rclone and restic it was built against; RCLONE_BIN and
  # RESTIC_BIN still override them.
  runnerArgs = {
    version = goArgs.version;
    src = final.lib.cleanSource ../../apps/backup-runner;
    vendorHash = null;
    # Go names the binary after the module path's last element (backup-runner).
    postInstall = "mv $out/bin/backup-runner $out/bin/homelab-backup-runner";
  };

  backupRunner = final.buildGoModule (
    runnerArgs
    // {
      pname = "homelab-backup-runner";
      subPackages = [ "." ];
      env.CGO_ENABLED = 0;
      ldflags = [
        "-s"
        "-w"
        "-X main.rcloneBin=${final.rclone}/bin/rclone"
        "-X main.resticBin=${final.restic}/bin/restic"
      ];
      # The tests run as the backup-runner-test check, with the race detector.
      doCheck = false;
      meta = {
        description = "Runs one homelab backup: drives rclone and restic, writes a state file";
        license = final.lib.licenses.mit;
        mainProgram = "homelab-backup-runner";
      };
    }
  );

  # Daemon and CLI in one Go build: they share almost every package, and the
  # sandbox has no build cache, so two derivations compiled everything twice.
  # The commit is deliberately not stamped in here (it would rebuild on every
  # commit).
  daemon = final.buildGoModule (
    goArgs
    // {
      pname = "homelab-daemon";
      subPackages = [
        "cmd/daemon"
        "cmd/cli"
      ];
      postInstall = ''
        mv $out/bin/daemon $out/bin/homelab-daemon
        mv $out/bin/cli $out/bin/homelab
      '';
      meta = {
        description = "Homelab service orchestrator daemon + CLI";
        license = final.lib.licenses.mit;
        mainProgram = "homelab-daemon";
      };
    }
  );

  # The CLI on its own (systemPackages, `nix run .#cli`): a link into the
  # shared build, so `homelab-daemon` stays off PATH.
  cli =
    final.runCommand "homelab-cli"
      {
        meta = {
          description = "Homelab CLI — manage services, secrets, diagnostics";
          license = final.lib.licenses.mit;
          mainProgram = "homelab";
        };
      }
      ''
        mkdir -p $out/bin
        ln -s ${daemon}/bin/homelab $out/bin/homelab
      '';

  # The dashboard (apps/dashboard, Svelte): a static single-page app with
  # precompressed files, served at / by Caddy.
  dashboard = final.buildNpmPackage {
    name = "homelab-dashboard";
    src = ../../apps/dashboard;
    npmDepsHash = dashboardNpmDepsHash;
    dontNpmBuild = true;
    npmFlags = [ "--loglevel=error" ];
    buildPhase = ''
      runHook preBuild
      npm run build
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      mkdir -p $out
      cp -r build/* $out/
      runHook postInstall
    '';
  };

  # Flake checks that need the daemon/dashboard build inputs (wired in nix/default.nix).
  # Nix never runs code generation at build time; the drift checks only diff.
  checks = {
    daemon-vet = final.buildGoModule (
      goArgs
      // {
        pname = "homelab-daemon-vet";
        # gofmt skips vendor/, which buildGoModule places in the source tree.
        buildPhase = ''
          runHook preBuild
          unformatted=$(find . -name '*.go' -not -path './vendor/*' -exec gofmt -l {} +)
          if [ -n "$unformatted" ]; then echo "gofmt needed:"; echo "$unformatted"; exit 1; fi
          go vet ./...
          runHook postBuild
        '';
        doCheck = false;
        installPhase = "touch $out";
      }
    );

    # -race needs cgo (on by default here). Integration-tagged tests are not built.
    daemon-test = final.buildGoModule (
      goArgs
      // {
        pname = "homelab-daemon-test";
        nativeBuildInputs = goArgs.nativeBuildInputs ++ [ final.git ];
        buildPhase = "runHook preBuild; go build ./...; runHook postBuild";
        doCheck = true;
        checkPhase = "runHook preCheck; go test -race ./...; runHook postCheck";
        installPhase = "touch $out";
      }
    );

    # gofmt, vet and the tests of the backup runner. -race needs cgo, which the
    # shipped binary is built without. The fake rclone and restic are the
    # race-built test binary, which sleeps a second at exit unless told not to.
    backup-runner-test = final.buildGoModule (
      runnerArgs
      // {
        pname = "homelab-backup-runner-test";
        buildPhase = ''
          runHook preBuild
          unformatted=$(gofmt -l .)
          if [ -n "$unformatted" ]; then echo "gofmt needed:"; echo "$unformatted"; exit 1; fi
          go vet ./...
          runHook postBuild
        '';
        doCheck = true;
        checkPhase = "runHook preCheck; GORACE=atexit_sleep_ms=0 go test -race ./...; runHook postCheck";
        installPhase = "touch $out";
      }
    );

    codegen-drift = final.buildGoModule (
      goArgs
      // {
        pname = "homelab-codegen-drift";
        nativeBuildInputs = goArgs.nativeBuildInputs ++ [ final.sqlc ];
        buildPhase = ''
          runHook preBuild
          cp -r internal/store $TMPDIR/store.orig
          go generate ./internal/store
          diff -r $TMPDIR/store.orig internal/store
          go run ./cmd/daemon openapi > $TMPDIR/openapi.json
          diff $TMPDIR/openapi.json api/openapi.json
          runHook postBuild
        '';
        doCheck = false;
        installPhase = "touch $out";
      }
    );

    dashboard-test = dashboard.overrideAttrs (_: {
      pname = "homelab-dashboard-test";
      buildPhase = "runHook preBuild; npm run check && npx vitest run; runHook postBuild";
      installPhase = "touch $out";
    });

    dashboard-schema-drift = final.stdenv.mkDerivation {
      name = "homelab-dashboard-schema-drift";
      src = final.lib.fileset.toSource {
        root = ../../.;
        fileset = final.lib.fileset.unions [
          ../../apps/dashboard
          ../../apps/core/api/openapi.json
        ];
      };
      sourceRoot = "source/apps/dashboard";
      npmDeps = dashboard.npmDeps;
      nativeBuildInputs = [
        final.nodejs
        final.npmHooks.npmConfigHook
      ];
      dontBuild = false;
      buildPhase = ''
        runHook preBuild
        npx openapi-typescript ../core/api/openapi.json -o $TMPDIR/schema.gen.ts
        diff $TMPDIR/schema.gen.ts src/lib/api/schema.gen.ts
        runHook postBuild
      '';
      installPhase = "touch $out";
    };
  };
  # The image host's core extension (#95).
  coreExtension = import ./core-extension.nix {
    pkgs = final;
    inherit
      daemon
      dashboard
      runnerArgs
      bcduSrc
      ;
  };
in
{
  homelab-checks = checks;
  homelab-core-extension = coreExtension;

  homelab-daemon = daemon;
  homelab-daemon-binary = daemon;
  homelab-daemon-cli = cli;
  homelab-backup-runner = backupRunner;
  homelab-dashboard = dashboard;
}
