package notifications

import "os"

// LinkBase is the dashboard origin notifications link to, from
// HOMELAB_LINK_BASE.
func LinkBase() string { return os.Getenv("HOMELAB_LINK_BASE") }
