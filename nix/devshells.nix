# Dev shells — enter from the repo root: `nix develop .#<daemon|dashboard>`.
# The dashboard shell cds into its app dir on entry.
{ pkgs }:
let
  daemon = pkgs.mkShell {
    name = "homelab-daemon-dev";
    packages = with pkgs; [
      go
      gopls
      golangci-lint
      sqlc
      sqlite
    ];
    shellHook = ''
      echo "homelab-daemon dev shell (go $(go version | cut -d' ' -f3))"
    '';
  };
in
{
  inherit daemon;

  # The image host (#95): QEMU and OVMF boot the image for `make
  # image-check`. mkosi itself runs in an Arch build root (image/build.sh).
  # Its own shell: the default one stays small.
  image = pkgs.mkShell {
    name = "homelab-image";
    packages = with pkgs; [
      zstd
      qemu_kvm
      socat
      openssh
      erofs-utils
      jq
      # The check's stand-in for a Windows disk: a GPT, a FAT ESP and files on it.
      util-linux
      dosfstools
      mtools
    ];
    shellHook = ''
      export HOMELAB_OVMF="${pkgs.OVMF.fd}/FV"
    '';
  };

  # The shell the Makefile runs every target in; HOMELAB_DEV_SHELL tells it
  # it is already inside.
  default = pkgs.mkShell {
    name = "homelab-dev";
    inputsFrom = [ daemon ];
    packages = with pkgs; [
      nodejs
      nixfmt
      gnumake
    ];
    shellHook = ''
      echo "homelab dev shell (go $(go version | cut -d' ' -f3), node $(node --version))"
      export HOMELAB_ROOT=$(git rev-parse --show-toplevel)
      export HOMELAB_DEV_SHELL=1
    '';
  };

  # Node and Playwright for the dashboard and the verify skill's browser runs.
  dashboard = pkgs.mkShell {
    name = "homelab-dashboard-dev";
    packages = with pkgs; [
      nodejs
      playwright-driver.browsers
    ];
    shellHook = ''
      echo "homelab-dashboard dev shell (node $(node --version))"
      export PLAYWRIGHT_BROWSERS_PATH="${pkgs.playwright-driver.browsers}"
      export PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
      # The dashboard dev server sends /api to this host (vite.config.ts).
      export HOMELAB_URL="''${HOMELAB_URL:-https://homelab.example.com}"
      # Scripts import playwright-core from nixpkgs, not npm: an npm copy
      # expects the browser revisions of its own version and breaks whenever
      # a nixpkgs bump moves playwright-driver.browsers. ESM ignores
      # NODE_PATH, so link it where resolution from apps/dashboard/ finds it.
      if [ -d apps/dashboard ]; then
        mkdir -p node_modules
        ln -sfn ${pkgs.playwright-driver} node_modules/playwright-core
      fi
      # typescript-language-server looks for tsserver under the LSP workspace
      # root (the repo root), but the only typescript lives in the dashboard's
      # node_modules (nixpkgs ships TS7-go, which has no tsserver.js). Without
      # this the TS LSP degrades to syntax-only.
      if [ -d apps/dashboard/node_modules/typescript ]; then
        mkdir -p node_modules
        ln -sfn ../apps/dashboard/node_modules/typescript node_modules/typescript
      fi
      cd apps/dashboard 2>/dev/null || true
    '';
  };
}
