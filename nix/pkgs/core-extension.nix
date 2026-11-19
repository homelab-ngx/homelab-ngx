# The core system extension (`homelab-core`) for the image host (#95): the
# daemon, CLI and backup runner, the dashboard, Caddy with its DNS modules,
# bcdu, and the units and config in image/core. systemd-sysext merges it over
# the image's /usr, so everything in it is static: it needs nothing from the
# image but the extension level. The image itself is built with mkosi
# (image/, `make image`).
{
  pkgs,
  daemon,
  dashboard,
  runnerArgs,
  bcduSrc,
}:
let
  lib = pkgs.lib;

  # systemd-sysext merges an extension only into an image of its level
  # (SYSEXT_LEVEL in the image's os-release). The level stays 1: what a core
  # needs from the image is said by minImage.
  sysextLevel = "1";

  # The oldest image version this core runs on; empty for any. Raise it to
  # the version of the image that brings what the core cannot do without.
  # A host applies a core only on an image that new (the image's
  # usr/lib/homelab/core-at-boot, the daemon's core update), so a release
  # with a higher minimum installs its image first and the core at the
  # restart. What the core can do without, it does without: an older image
  # must not stop it from starting.
  minImage = "";

  static =
    drv:
    drv.overrideAttrs (o: {
      env = (o.env or { }) // {
        CGO_ENABLED = "0";
      };
    });

  daemonStatic = static daemon;

  # The runner calls the image's rclone and restic, not store paths.
  runner = pkgs.buildGoModule (
    runnerArgs
    // {
      pname = "homelab-backup-runner-image";
      subPackages = [ "." ];
      env.CGO_ENABLED = 0;
      ldflags = [
        "-s"
        "-w"
        "-X main.rcloneBin=/usr/bin/rclone"
        "-X main.resticBin=/usr/bin/restic"
      ];
      doCheck = false;
    }
  );

  # Caddy with the DNS-01 providers a host may use for its wildcard
  # certificate. Arch's package has no DNS modules.
  caddy = static (
    pkgs.caddy.withPlugins {
      plugins = [
        "github.com/caddy-dns/cloudflare@v0.2.4"
        "github.com/caddy-dns/porkbun@v0.3.1"
        "github.com/caddy-dns/duckdns@v0.5.0"
        "github.com/caddy-dns/route53@v1.6.3"
      ];
      hash = "sha256-dwsZoa5hclOhL58V8V7oQqIdKbF0f6VbamrKhoVT12I=";
    }
  );

  bcdu = pkgs.pkgsStatic.rustPlatform.buildRustPackage {
    pname = "bcdu";
    version = (builtins.fromTOML (builtins.readFile "${bcduSrc}/Cargo.toml")).package.version;
    src = bcduSrc;
    cargoLock.lockFile = "${bcduSrc}/Cargo.lock";
    doCheck = false;
  };

  # The daemon's config until host settings move into its database (#96):
  # a host with no pool, no domain and no optional feature. It must load
  # under the daemon's own validator (checked below).
  defaultConfig = pkgs.writeText "config.json" (
    builtins.toJSON {
      version = 2;
      paths = {
        state_dir = "/var/lib/homelab-daemon";
        db = "/var/lib/homelab-daemon/homelab.db";
        socket = "/run/homelab-daemon/daemon.sock";
        db_backup_dir = "/var/lib/homelab/daemon/backups";
        backup_runner = "/usr/bin/homelab-backup-runner";
        cache_dir = "/var/lib/homelab";
      };
      db = {
        snapshot_schedule = "45 2 * * *";
        snapshot_keep = 7;
        retention_days = 90;
      };
      core_services = [
        "caddy.service"
        "homelab-daemon.service"
        "sshd.service"
        "systemd-networkd.service"
      ];
      cloudflare_tunnel = {
        enabled = false;
        metrics_url = "http://127.0.0.1:20241";
        hostnames = [ ];
        public_hostnames = { };
        unit = "";
        connectors = 4;
      };
      stacks = {
        domain = "";
        dashboard_url = "";
        lan = [ ];
        gpu = {
          packages = [ ];
          libva_driver = "";
        };
      };
      doctor.scrutiny_url = "";
      security.notes = [ "The image's default config: host settings come with #96." ];
    }
  );

  # Fixed, so two builds of the same inputs give the same image.
  uuid = "6f6d6d6f-6c61-4e62-8c6f-726500000001";
in
pkgs.runCommand "homelab-core-extension"
  {
    nativeBuildInputs = [
      pkgs.erofs-utils
      pkgs.file
    ];
    passthru = {
      inherit
        daemonStatic
        runner
        caddy
        bcdu
        defaultConfig
        sysextLevel
        ;
    };
  }
  ''
    root=$TMPDIR/root
    mkdir -p $root/usr/bin $root/usr/share/homelab $root/usr/lib/extension-release.d
    for b in ${daemonStatic}/bin/homelab-daemon ${daemonStatic}/bin/homelab ${runner}/bin/homelab-backup-runner \
             ${caddy}/bin/caddy ${bcdu}/bin/bcdu; do
      # A dynamic binary would need the store at run time.
      if ! file -L "$b" | grep -qE 'statically linked|static-pie linked'; then echo "not static: $b" >&2; exit 1; fi
      install -m755 "$b" $root/usr/bin/
    done
    cp -r ${dashboard} $root/usr/share/homelab/dashboard
    cp -r ${../../image/core}/. $root/
    install -m644 ${defaultConfig} $root/usr/share/homelab/config.json
    ${daemonStatic}/bin/homelab-daemon check-config $root/usr/share/homelab/config.json
    cat > $root/usr/lib/extension-release.d/extension-release.homelab-core <<EOF
    ID=arch
    SYSEXT_LEVEL=${sysextLevel}
    SYSEXT_ID=homelab-core
    SYSEXT_VERSION_ID=${daemon.version}
    HOMELAB_MIN_IMAGE=${minImage}
    EXTENSION_RELOAD_MANAGER=1
    EOF
    chmod -R u+w,go-w $root
    find $root -exec touch -h -d @1 {} +
    mkdir -p $out
    mkfs.erofs -zlz4hc --all-root -T 1 -U ${uuid} $out/homelab-core.raw $root
    echo ${daemon.version} > $out/version
  ''
