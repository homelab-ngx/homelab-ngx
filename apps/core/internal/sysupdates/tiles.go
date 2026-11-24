package sysupdates

import (
	"context"
	"errors"

	"homelab-ngx.com/core/internal/tilestatus"
)

// Tiles reports the updates tile: bad when a new image or core did not
// boot and the host fell back, info when an update waits for a reboot or is
// ready to install. A host not on the image has no badge.
func (m *Module) Tiles(ctx context.Context) (map[string]tilestatus.TileStatus, error) {
	st, err := m.ImageStatus(ctx)
	if errors.Is(err, ErrNotImageHost) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]tilestatus.TileStatus{"updates": tile(st)}, nil
}

func tile(st ImageStatus) tilestatus.TileStatus {
	switch {
	case st.FellBackFrom != "" || st.Core.FellBackFrom != "" || st.BootCheck != "":
		return tilestatus.Of(tilestatus.Bad)
	case st.PendingReboot || len(st.Available) > 0 || len(st.Core.Available) > 0:
		return tilestatus.Of(tilestatus.Info)
	}
	return tilestatus.TileStatus{}
}
