package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadHostConfig(t *testing.T) {
	t.Parallel()
	n, err := LoadNix(filepath.Join("testdata", "host.json"))
	if err != nil {
		t.Fatalf("LoadNix(host.json): %v", err)
	}
	if n.Version != SupportedVersion {
		t.Errorf("version = %d", n.Version)
	}
	if n.DB.SnapshotSchedule != "45 2 * * *" || n.DB.SnapshotKeep != 7 || n.DB.RetentionDays != 90 {
		t.Errorf("db = %+v", n.DB)
	}
	if len(n.CoreServices) == 0 {
		t.Fatal("no core services decoded")
	}
	if n.Paths.DB == "" || n.Paths.Socket == "" || n.Paths.BackupRunner == "" {
		t.Errorf("paths = %+v", n.Paths)
	}
	if !n.CloudflareTunnel.Enabled || n.CloudflareTunnel.MetricsURL == "" || len(n.CloudflareTunnel.Hostnames) == 0 {
		t.Errorf("cloudflare_tunnel = %+v", n.CloudflareTunnel)
	}
}

func writeTmp(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// minimal returns a valid document with a splice point for extra top-level JSON.
func minimal(extra string) string {
	return `{"version":2,"db":{"snapshot_schedule":"15 3 * * *","snapshot_keep":7,"retention_days":90}` + extra + `}`
}

func TestLoadNixRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, doc, wantErr string
	}{
		{"unknown field", minimal(`,"bogus":1`), "unknown field"},
		{"bad version", `{"version":1,"db":{"snapshot_schedule":"15 3 * * *"}}`, "unsupported version"},
		{"bad snapshot cron", `{"version":2,"db":{"snapshot_schedule":"*-*-* 03:15:00"}}`, "snapshot_schedule"},
		{"managed services", minimal(`,"services":[]`), "unknown field"},
		{"removed backups", minimal(`,"backups":[]`), "unknown field"},
		{"trailing data", minimal(``) + `{}`, "trailing"},
		{"removed feature block", minimal(`,"peer":{"enabled":false}`), "unknown field"},
		{"removed tunnel field", minimal(`,"cloudflare_tunnel":{"enabled":true,"token_file":"/t"}`), "unknown field"},
		{"removed block", minimal(`,"legacy":{"base_url":"https://h","token_file":"/t"}`), "unknown field"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadNix(writeTmp(t, c.doc))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestLoadNixMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := LoadNix(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error")
	}
}
