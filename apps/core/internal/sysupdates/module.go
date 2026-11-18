// Package sysupdates records what changed on the system, generation by
// generation, for the dashboard's Updates page. It also checks for new NixOS
// releases and reboots the host.
package sysupdates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	logging "homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/notifications"

	"homelab-ngx.com/core/internal/cmdrunner"
)

var log = logging.Logger("sysupdates")

const (
	profilesDir   = "/nix/var/nix/profiles"
	currentSystem = "/run/current-system"
	scanInterval  = time.Minute
)

var genLinkRe = regexp.MustCompile(`^system-([0-9]+)-link$`)

type Module struct {
	file string           // persisted generations
	repo string           // the flake checkout whose flake.lock names the base nixpkgs; "" = unknown
	r    cmdrunner.Runner // nix store diff-closures, git ls-remote, systemctl reboot

	mu   sync.Mutex // guards gens and the file
	gens map[int]Generation

	scanMu   sync.Mutex // one scan at a time
	profiles string
	scanned  time.Time

	relMu    sync.Mutex // guards the release check's results
	releases []string   // NixOS release branches, oldest first
	checked  *time.Time // last release check; nil = not yet

	notify Notifier // tells the operator a new release is out; may be nil

	dep DeployConfig // core deploys; a zero Source = off
	// image: an image host, updated through sysupdate (image.go).
	image bool
	ds    deployState

	now func() time.Time // seam for tests
}

// Notifier keeps the "new NixOS release" notification in line with the
// release check (*notifications.Notifier).
type Notifier interface {
	Reconcile(ctx context.Context, scope string, current []notifications.Problem) (notifications.Result, error)
}

// SetNotifier makes the release check notify the operator.
func (m *Module) SetNotifier(n Notifier) { m.notify = n }

// New records generations under stateDir. r runs nix store diff-closures,
// the daily release check and the reboot; repo is the flake checkout the
// release check reads the base nixpkgs from.
func New(r cmdrunner.Runner, stateDir, repo string) *Module {
	m := &Module{file: filepath.Join(stateDir, "system-generations.json"), repo: repo, r: r, gens: map[int]Generation{}, profiles: profilesDir, now: time.Now}
	if data, err := os.ReadFile(m.file); err == nil {
		var gens []Generation
		if err := json.Unmarshal(data, &gens); err != nil {
			log.Error("failed to parse generation history", "file", m.file, "error", err)
		}
		for _, g := range gens {
			m.gens[g.Number] = g
		}
	} else if !os.IsNotExist(err) {
		log.Error("failed to read generation history", "file", m.file, "error", err)
	}
	return m
}

// Start records new generations every minute, and checks for NixOS releases
// daily, until ctx ends.
func (m *Module) Start(ctx context.Context) {
	go m.releaseLoop(ctx)
	m.Scan(ctx)
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Scan(ctx)
		}
	}
}

// Scan records any system generation not seen yet. It returns early while
// the profiles directory's mtime is the one of the last scan that recorded
// every generation, so a generation that failed to record is retried.
// Concurrent callers return immediately instead of waiting on a scan in
// progress.
func (m *Module) Scan(ctx context.Context) {
	// NixOS system generations: an image host has none.
	if m.image {
		return
	}
	if !m.scanMu.TryLock() {
		return
	}
	defer m.scanMu.Unlock()

	// Stat before listing: a link added during the scan moves the mtime on.
	fi, err := os.Stat(m.profiles)
	if err != nil {
		log.Error("failed to list system profiles", "error", err)
		return
	}
	if fi.ModTime().Equal(m.scanned) {
		return
	}
	entries, err := os.ReadDir(m.profiles)
	if err != nil {
		log.Error("failed to list system profiles", "error", err)
		return
	}
	type link struct {
		n    int
		path string
	}
	var links []link
	for _, e := range entries {
		if sm := genLinkRe.FindStringSubmatch(e.Name()); sm != nil {
			n, _ := strconv.Atoi(sm[1])
			links = append(links, link{n, filepath.Join(m.profiles, e.Name())})
		}
	}
	sort.Slice(links, func(i, j int) bool { return links[i].n < links[j].n })

	added, complete := 0, true
	for _, l := range links {
		m.mu.Lock()
		_, known := m.gens[l.n]
		prev, hasPrev := m.latestBefore(l.n)
		m.mu.Unlock()
		if known {
			if m.backfillTiers(l.n, prev, hasPrev) {
				added++
			}
			continue
		}
		g, err := m.recordGeneration(ctx, l.n, l.path, prev, hasPrev)
		if err != nil {
			log.Warn("failed to record generation", "generation", l.n, "error", err)
			complete = false
			continue
		}
		m.mu.Lock()
		m.gens[l.n] = g
		m.mu.Unlock()
		added++
	}
	if added > 0 {
		log.Info("recorded system generations", "count", added)
		m.save()
	}
	if complete {
		m.scanned = fi.ModTime()
	}
}

func (m *Module) latestBefore(n int) (Generation, bool) {
	best, ok := Generation{}, false
	for num, g := range m.gens {
		if num < n && (!ok || num > best.Number) {
			best, ok = g, true
		}
	}
	return best, ok
}

// backfillTiers adds tiers to a generation recorded before they existed,
// while its store path is still on disk.
func (m *Module) backfillTiers(n int, prev Generation, hasPrev bool) bool {
	m.mu.Lock()
	g := m.gens[n]
	m.mu.Unlock()
	if g.Tiers != nil {
		return false
	}
	if _, err := os.Stat(g.System); err != nil {
		return false
	}
	g.Tiers = generationTiers(g.System, prev, hasPrev)
	m.mu.Lock()
	m.gens[n] = g
	m.mu.Unlock()
	return true
}

// generationTiers covers both sides of the diff against prev, so removals
// keep their tier.
func generationTiers(system string, prev Generation, hasPrev bool) map[string]string {
	tiers := systemTiers(system)
	if !hasPrev || prev.System == system {
		return tiers
	}
	if _, err := os.Stat(prev.System); err == nil {
		return mergeTiers(systemTiers(prev.System), tiers)
	}
	return mergeTiers(prev.Tiers, tiers)
}

func (m *Module) recordGeneration(ctx context.Context, n int, link string, prev Generation, hasPrev bool) (Generation, error) {
	fi, err := os.Lstat(link)
	if err != nil {
		return Generation{}, err
	}
	system, err := filepath.EvalSymlinks(link)
	if err != nil {
		return Generation{}, err
	}
	version, _ := os.ReadFile(filepath.Join(system, "nixos-version"))
	g := Generation{
		Number:       n,
		Time:         fi.ModTime(),
		System:       system,
		NixosVersion: strings.TrimSpace(string(version)),
		Changes:      []UpdateChange{},
		Tiers:        generationTiers(system, prev, hasPrev),
	}
	if !hasPrev {
		g.Baseline = true
		return g, nil
	}
	if prev.System == system {
		return g, nil
	}
	if _, err := os.Stat(prev.System); err != nil {
		// Garbage-collected before we could diff it.
		g.Baseline = true
		return g, nil
	}
	dctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := m.r.Run(dctx, cmdrunner.Cmd{
		Module: "updates", Name: "nix", Args: []string{"store", "diff-closures", prev.System, system},
	})
	if err != nil {
		return Generation{}, fmt.Errorf("diff-closures %d: %w", n, err)
	}
	g.Changes = ParseDiffClosures(res.Stdout)
	return g, nil
}

func (m *Module) save() {
	m.mu.Lock()
	gens := make([]Generation, 0, len(m.gens))
	for _, g := range m.gens {
		gens = append(gens, g)
	}
	m.mu.Unlock()
	sort.Slice(gens, func(i, j int) bool { return gens[i].Number < gens[j].Number })
	data, err := json.Marshal(gens)
	if err != nil {
		log.Error("failed to encode generation history", "error", err)
		return
	}
	tmp := m.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		log.Error("failed to write generation history", "error", err)
		return
	}
	if err := os.Rename(tmp, m.file); err != nil {
		log.Error("failed to replace generation history", "error", err)
	}
}

// ── API ─────────────────────────────────────────────────────────────────

// Timeline returns one page of the update history.
func (m *Module) Timeline(ctx context.Context, q Query) UpdateTimeline {
	m.Scan(ctx)
	m.mu.Lock()
	gens := make([]Generation, 0, len(m.gens))
	for _, g := range m.gens {
		gens = append(gens, g)
	}
	m.mu.Unlock()
	current, _ := filepath.EvalSymlinks(currentSystem)
	return paginate(buildEvents(gens, current), q)
}
