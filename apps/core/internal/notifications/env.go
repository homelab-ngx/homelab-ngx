package notifications

import (
	"bufio"
	"os"
	"strings"
)

// DefaultEnvFile is where sops renders the daemon environment that carries
// HOMELAB_LINK_BASE.
const DefaultEnvFile = "/run/secrets/rendered/homelab-notify-env"

// LinkBase is the dashboard origin from HOMELAB_LINK_BASE, read from
// DefaultEnvFile when the environment lacks it (interactive CLI use).
func LinkBase() string {
	if v := os.Getenv("HOMELAB_LINK_BASE"); v != "" {
		return v
	}
	f, err := os.Open(DefaultEnvFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && k == "HOMELAB_LINK_BASE" {
			return v
		}
	}
	return ""
}
