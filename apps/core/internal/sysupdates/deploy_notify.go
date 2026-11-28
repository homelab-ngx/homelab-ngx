package sysupdates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/notifications"
)

const (
	deployNotifiedFile    = "core-deploy-notified"
	deployNotifiedCap     = 60
	deployBootstrapWindow = 15 * time.Minute
	deployUpdatesPath     = "/settings/updates"
)

// deployNotification builds the terminal notification for a finished update.
// Already-up-to-date no-ops and successful downloads are skipped (staged
// downloads are announced by notifyImageRelease instead).
func deployNotification(d CoreDeploy) (notifications.Message, bool) {
	switch d.Kind {
	case KindBuild:
		key := fmt.Sprintf("host-rebuild:%d", d.Id)
		switch d.Result {
		case DeployHealthy:
			if strings.HasPrefix(d.Phase, "Already") || strings.HasPrefix(d.Phase, "Up to date") {
				return notifications.Message{}, false
			}
			title := "Host image built"
			if d.Version != "" {
				title = "Built image " + d.Version
			}
			return notifications.Message{
				Severity: notifications.Info, Title: title, Body: d.Phase,
				Path: deployUpdatesPath, Event: "host.rebuild", Key: key,
			}, true
		case DeployBuildFailed, DeployFailed, DeployInterrupted:
			return notifications.Message{
				Severity: notifications.Alert, Title: "Host image build failed", Body: d.Reason,
				Path: deployUpdatesPath, Event: "host.rebuild", Key: key,
			}, true
		}

	case KindCoreUpdate:
		key := fmt.Sprintf("core-deploy:%d", d.Id)
		switch d.Result {
		case DeployHealthy:
			if strings.HasPrefix(d.Phase, "Already") {
				return notifications.Message{}, false
			}
			title := "Core updated"
			if d.Version != "" {
				title = "Updated core to " + d.Version
			}
			body := ""
			if len(d.FailedUnits) > 0 {
				body = "Failed units: " + strings.Join(d.FailedUnits, ", ")
			}
			return notifications.Message{
				Severity: notifications.Info, Title: title, Body: body,
				Path: deployUpdatesPath, Event: "core.update", Key: key,
			}, true
		case DeployRolledBack:
			return notifications.Message{
				Severity: notifications.Alert, Title: "Core update rolled back", Body: d.Reason,
				Path: deployUpdatesPath, Event: "core.update", Key: key,
			}, true
		case DeployFailed, DeployInterrupted:
			return notifications.Message{
				Severity: notifications.Alert, Title: "Core update failed", Body: d.Reason,
				Path: deployUpdatesPath, Event: "core.update", Key: key,
			}, true
		}

	case KindImageUpdate, KindInstall:
		key := fmt.Sprintf("core-deploy:%d", d.Id)
		switch d.Result {
		case DeployHealthy:
			if strings.HasPrefix(d.Phase, "Already") {
				return notifications.Message{}, false
			}
			if strings.Contains(d.Phase, "next reboot") {
				title := "Update installed; reboot needed"
				if d.Version != "" {
					title = "Installed " + d.Version + "; reboot needed"
				}
				return notifications.Message{
					Severity: notifications.Info, Title: title, Body: d.Phase,
					Path: deployUpdatesPath, Event: "core.reboot_needed", Key: key,
				}, true
			}
			title := "Update installed"
			if d.Version != "" {
				title = "Installed " + d.Version
			}
			return notifications.Message{
				Severity: notifications.Info, Title: title, Body: d.Phase,
				Path: deployUpdatesPath, Event: "core.update", Key: key,
			}, true
		case DeployRolledBack:
			return notifications.Message{
				Severity: notifications.Alert, Title: "Host update rolled back", Body: d.Reason,
				Path: deployUpdatesPath, Event: "core.update", Key: key,
			}, true
		case DeployFailed, DeployInterrupted:
			return notifications.Message{
				Severity: notifications.Alert, Title: "Host update failed", Body: d.Reason,
				Path: deployUpdatesPath, Event: "core.update", Key: key,
			}, true
		}

	case KindDownload:
		key := fmt.Sprintf("core-deploy:%d", d.Id)
		switch d.Result {
		case DeployFailed, DeployInterrupted:
			return notifications.Message{
				Severity: notifications.Alert, Title: "Release download failed", Body: d.Reason,
				Path: deployUpdatesPath, Event: "core.update", Key: key,
			}, true
		}
	}
	return notifications.Message{}, false
}

func deployNotifiedPath(dir string) string {
	return filepath.Join(dir, deployNotifiedFile)
}

func loadNotifiedIDs(dir string) ([]int64, bool) {
	b, err := os.ReadFile(deployNotifiedPath(dir))
	if err != nil {
		return nil, false
	}
	var ids []int64
	if err := json.Unmarshal(b, &ids); err != nil {
		return nil, true
	}
	return ids, true
}

func saveNotifiedIDs(dir string, ids []int64) error {
	if ids == nil {
		ids = []int64{}
	}
	if len(ids) > deployNotifiedCap {
		ids = ids[len(ids)-deployNotifiedCap:]
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	path := deployNotifiedPath(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func markNotifiedID(dir string, id int64) {
	if dir == "" || id == 0 {
		return
	}
	ids, _ := loadNotifiedIDs(dir)
	if slices.Contains(ids, id) {
		return
	}
	ids = append(ids, id)
	if err := saveNotifiedIDs(dir, ids); err != nil {
		log.Warn("could not record notified update", "id", id, "error", err)
	}
}

func postDeployNotification(socket, dir string) func(context.Context, CoreDeploy) {
	if socket == "" {
		return nil
	}
	return func(ctx context.Context, d CoreDeploy) {
		if ids, _ := loadNotifiedIDs(dir); slices.Contains(ids, d.Id) {
			return
		}
		msg, ok := deployNotification(d)
		if !ok {
			markNotifiedID(dir, d.Id)
			return
		}
		payload, err := json.Marshal(notifications.NotifyRequest{
			Action:   notifications.Send,
			Severity: msg.Severity,
			Title:    msg.Title,
			Body:     msg.Body,
			Path:     msg.Path,
			Event:    msg.Event,
			Key:      msg.Key,
		})
		if err != nil {
			return
		}
		c := &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(pctx, http.MethodPost, "http://unix/api/notifications", bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadGateway {
			markNotifiedID(dir, d.Id)
		}
	}
}

// notifyFinishedDeploys emits notifications for completed deploys that
// core-deploy could not post directly (for example when a deploy restarted or
// rebooted the daemon before posting).
func (m *Module) notifyFinishedDeploys(ctx context.Context) {
	if m.notify == nil || m.dir == "" {
		return
	}
	m.ds.notifyMu.Lock()
	defer m.ds.notifyMu.Unlock()

	ids, exists := loadNotifiedIDs(m.dir)
	bootstrap := !exists
	if bootstrap {
		_ = saveNotifiedIDs(m.dir, ids)
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}

	list := m.deploys().load()
	now := m.now()
	for i := len(list) - 1; i >= 0; i-- {
		d := list[i]
		if d.Result == DeployRunning || seen[d.Id] {
			continue
		}
		if d.Kind == KindDownload && d.Result == DeployHealthy {
			markNotifiedID(m.dir, d.Id)
			m.notifyImageRelease(ctx)
			continue
		}
		if bootstrap && (d.FinishedAt == nil || now.Sub(*d.FinishedAt) > deployBootstrapWindow) {
			markNotifiedID(m.dir, d.Id)
			continue
		}
		msg, ok := deployNotification(d)
		if !ok {
			markNotifiedID(m.dir, d.Id)
			continue
		}
		if _, err := m.notify.Send(ctx, msg); err != nil && !errors.Is(err, notifications.ErrNoChannels) {
			log.Warn("update notification not sent", "id", d.Id, "error", err)
			continue
		}
		markNotifiedID(m.dir, d.Id)
	}
}
