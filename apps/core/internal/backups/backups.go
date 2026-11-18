package backups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/store"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// pathDenied are host directories a custom source may not be or sit under:
// kernel and runtime filesystems, and the nix store.
var pathDenied = []string{"/proc", "/sys", "/dev", "/run", "/nix", "/tmp"}

// recentWindow is how far back a backup's recent_runs go: the strip's 14 local
// days, plus one so the oldest day is whole in any time zone.
const recentWindow = 15 * 24 * time.Hour

// foldersTTL is how long the stacks' recommended folders are reused; reading
// them asks systemd about every stack.
const foldersTTL = 10 * time.Second

type folderCache struct {
	at   time.Time
	list []StackFolders
}

// stackFolders returns every stack's recommended folders, a few seconds old
// at most.
func (s *Service) stackFolders(ctx context.Context) []StackFolders {
	if s.stacks == nil {
		return nil
	}
	s.mu.Lock()
	c := s.folders
	s.mu.Unlock()
	if c != nil && s.now().Sub(c.at) < foldersTTL {
		return c.list
	}
	list, err := s.stacks.BackupFolders(ctx)
	if err != nil {
		s.log.Warn("read the stacks' recommended folders", "error", err)
		if c != nil {
			return c.list
		}
		return nil
	}
	s.mu.Lock()
	s.folders = &folderCache{at: s.now(), list: list}
	s.mu.Unlock()
	return list
}

func findFolder(all []StackFolders, stack, key string) (StackFolders, StackFolder, bool) {
	for _, sf := range all {
		if sf.Stack != stack {
			continue
		}
		for _, f := range sf.Folders {
			if f.Key == key {
				return sf, f, true
			}
		}
		return sf, StackFolder{}, false
	}
	return StackFolders{}, StackFolder{}, false
}

func cleanExcludes(in []Exclude) ([]Exclude, error) {
	out := make([]Exclude, 0, len(in))
	for _, e := range in {
		p := strings.TrimSpace(e.Pattern)
		if err := stacks.ValidBackupPattern(p); err != nil {
			return nil, invalid("invalid_pattern", "skip pattern %q: %s", e.Pattern, err)
		}
		reason := strings.TrimSpace(e.Reason)
		if len(reason) > 300 || hasControl(reason) {
			return nil, invalid("invalid_pattern", "the reason for %q is one line of at most 300 characters", p)
		}
		out = append(out, Exclude{Pattern: p, Reason: reason})
	}
	return out, nil
}

// cleanSources validates a backup's sources and returns them as rows.
func cleanSources(in []SourceInput) ([]store.InsertBackupSourceParams, error) {
	seen := map[string]bool{}
	out := make([]store.InsertBackupSourceParams, 0, len(in))
	for i, src := range in {
		row := store.InsertBackupSourceParams{Kind: string(src.Kind), Position: int64(i), SizeOnly: src.SizeOnly}
		switch src.Kind {
		case SourceStack:
			if src.Stack == "" || src.Folder == "" {
				return nil, invalid("invalid_source", "a stack source names the stack and one of its recommended folders")
			}
			row.Stack, row.Folder = src.Stack, src.Folder
		case SourcePath:
			p := filepath.Clean(strings.TrimSpace(src.Path))
			if !filepath.IsAbs(p) || p == "/" || hasControl(p) {
				return nil, invalid("invalid_path", "%q is not an absolute folder", src.Path)
			}
			for _, d := range pathDenied {
				if p == d || strings.HasPrefix(p, d+"/") {
					return nil, invalid("invalid_path", "%s cannot be backed up: it is under %s", p, d)
				}
			}
			if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
				return nil, invalid("invalid_path", "%s is not a folder on this host", p)
			}
			row.Path = p
		case SourceCore:
		default:
			return nil, invalid("invalid_source", "a source's kind is stack, path or core")
		}
		key := row.Kind + "\x00" + row.Stack + "\x00" + row.Folder + "\x00" + row.Path
		if seen[key] {
			return nil, invalid("invalid_source", "the same source is listed twice")
		}
		seen[key] = true
		if src.RemotePath != nil {
			rp := strings.Trim(strings.TrimSpace(*src.RemotePath), "/")
			if hasControl(rp) || strings.Contains("/"+rp+"/", "/../") {
				return nil, invalid("invalid_remote_path", "%q is not a folder at the destination", *src.RemotePath)
			}
			row.RemotePath = &rp
		}
		ex, err := cleanExcludes(src.Excludes)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(ex)
		row.Excludes = string(b)
		out = append(out, row)
	}
	return out, nil
}

func cleanSchedule(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", nil
	}
	if len(strings.Fields(spec)) != 5 {
		return "", invalid("invalid_schedule", "the schedule is a 5-field cron expression, e.g. 0 4 * * *")
	}
	if _, err := cron.ParseStandard(spec); err != nil {
		return "", invalid("invalid_schedule", "invalid cron %q: %s", spec, err)
	}
	return spec, nil
}

func cleanBwlimit(v string) (string, error) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v != "" && !bwlimitRE.MatchString(v) {
		return "", invalid("invalid_bwlimit", "the upload limit is a number with an optional K, M or G, e.g. 4M")
	}
	return v, nil
}

// cleanHealthcheck accepts a ping URL, or a bare Healthchecks.io check id.
func cleanHealthcheck(v string) (string, error) {
	v = strings.TrimSuffix(strings.TrimSpace(v), "/")
	if v == "" {
		return "", nil
	}
	if uuidRE.MatchString(v) {
		return "https://hc-ping.com/" + strings.ToLower(v), nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", invalid("invalid_healthcheck", "the healthcheck is a ping URL or a Healthchecks.io check id")
	}
	return v, nil
}

func (s *Service) backupRow(ctx context.Context, id int64) (store.Backup, error) {
	row, err := s.q.GetBackup(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("%w: %d", ErrUnknownBackup, id)
	}
	return row, err
}

// checkDestinations confirms every id names a destination, once.
func (s *Service) checkDestinations(ctx context.Context, ids []int64) error {
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			return invalid("invalid_destination", "the same destination is listed twice")
		}
		seen[id] = true
		if _, err := s.destination(ctx, id); err != nil {
			if errors.Is(err, ErrUnknownDestination) {
				return invalid("invalid_destination", "there is no destination %d", id)
			}
			return err
		}
	}
	return nil
}

func replaceSources(ctx context.Context, q *store.Queries, id int64, rows []store.InsertBackupSourceParams) error {
	if err := q.DeleteBackupSources(ctx, id); err != nil {
		return err
	}
	for _, r := range rows {
		r.BackupID = id
		if err := q.InsertBackupSource(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func replaceTargets(ctx context.Context, q *store.Queries, id int64, dests []int64) error {
	if err := q.DeleteBackupTargets(ctx, id); err != nil {
		return err
	}
	for i, d := range dests {
		if err := q.InsertBackupTarget(ctx, store.InsertBackupTargetParams{BackupID: id, DestinationID: d, Position: int64(i)}); err != nil {
			return err
		}
	}
	return nil
}

// Create stores a new backup.
func (s *Service) Create(ctx context.Context, in BackupInput) (Backup, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return Backup{}, err
	}
	schedule, err := cleanSchedule(in.Schedule)
	if err != nil {
		return Backup{}, err
	}
	bw, err := cleanBwlimit(in.Bwlimit)
	if err != nil {
		return Backup{}, err
	}
	hc, err := cleanHealthcheck(in.HealthcheckUrl)
	if err != nil {
		return Backup{}, err
	}
	sources, err := cleanSources(in.Sources)
	if err != nil {
		return Backup{}, err
	}
	if err := s.checkDestinations(ctx, in.DestinationIds); err != nil {
		return Backup{}, err
	}
	now := s.now().UnixMilli()
	var id int64
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		var err error
		id, err = q.InsertBackup(ctx, store.InsertBackupParams{
			Name: name, Enabled: in.Enabled == nil || *in.Enabled, Schedule: schedule, Bwlimit: bw,
			Transfers: int64(in.Transfers), HealthcheckUrl: hc, CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if err := replaceSources(ctx, q, id, sources); err != nil {
			return err
		}
		return replaceTargets(ctx, q, id, in.DestinationIds)
	})
	if isUnique(err) {
		return Backup{}, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	if err != nil {
		return Backup{}, err
	}
	s.log.Info("backup created", "backup", name)
	s.reschedule(ctx)
	return s.Get(ctx, id)
}

// Update changes a backup; absent members stay as they are.
func (s *Service) Update(ctx context.Context, id int64, p BackupPatch) (Backup, error) {
	row, err := s.backupRow(ctx, id)
	if err != nil {
		return Backup{}, err
	}
	if p.Name != nil {
		if row.Name, err = cleanName(*p.Name); err != nil {
			return Backup{}, err
		}
	}
	if p.Enabled != nil {
		row.Enabled = *p.Enabled
	}
	if p.Schedule != nil {
		if row.Schedule, err = cleanSchedule(*p.Schedule); err != nil {
			return Backup{}, err
		}
	}
	if p.Bwlimit != nil {
		if row.Bwlimit, err = cleanBwlimit(*p.Bwlimit); err != nil {
			return Backup{}, err
		}
	}
	if p.Transfers != nil {
		row.Transfers = int64(*p.Transfers)
	}
	if p.HealthcheckUrl != nil {
		if row.HealthcheckUrl, err = cleanHealthcheck(*p.HealthcheckUrl); err != nil {
			return Backup{}, err
		}
	}
	var sources []store.InsertBackupSourceParams
	if p.Sources != nil {
		if sources, err = cleanSources(*p.Sources); err != nil {
			return Backup{}, err
		}
	}
	if p.DestinationIds != nil {
		if err := s.checkDestinations(ctx, *p.DestinationIds); err != nil {
			return Backup{}, err
		}
	}
	err = s.st.Tx(ctx, func(q *store.Queries) error {
		if err := q.UpdateBackup(ctx, store.UpdateBackupParams{
			Name: row.Name, Enabled: row.Enabled, Schedule: row.Schedule, Bwlimit: row.Bwlimit,
			Transfers: row.Transfers, HealthcheckUrl: row.HealthcheckUrl, UpdatedAt: s.now().UnixMilli(), ID: id,
		}); err != nil {
			return err
		}
		if p.Sources != nil {
			if err := replaceSources(ctx, q, id, sources); err != nil {
				return err
			}
		}
		if p.DestinationIds != nil {
			return replaceTargets(ctx, q, id, *p.DestinationIds)
		}
		return nil
	})
	if isUnique(err) {
		return Backup{}, fmt.Errorf("%w: %q", ErrNameTaken, row.Name)
	}
	if err != nil {
		return Backup{}, err
	}
	s.reschedule(ctx)
	if !row.Enabled || row.Schedule == "" {
		// Nothing checks an unscheduled backup for staleness again.
		s.resolve(ctx, staleKey(id))
	}
	return s.Get(ctx, id)
}

// Delete removes a backup. Its runs stay in the history under its name.
func (s *Service) Delete(ctx context.Context, id int64) error {
	row, err := s.backupRow(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	_, busy := s.running[id]
	s.mu.Unlock()
	if busy {
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, row.Name)
	}
	if err := s.q.DeleteBackup(ctx, id); err != nil {
		return err
	}
	s.log.Info("backup deleted", "backup", row.Name)
	s.reschedule(ctx)
	s.resolve(ctx, problemKey(id))
	s.resolve(ctx, staleKey(id))
	return nil
}

func (s *Service) resolve(ctx context.Context, key string) {
	if s.notify == nil || !s.notify.Enabled() {
		return
	}
	if _, err := s.notify.Resolve(ctx, key); err != nil {
		s.log.Warn("failed to resolve a backup problem", "key", key, "error", err)
	}
}

func parseExcludes(raw string) []Exclude {
	out := []Exclude{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// coreLabel names the built-in source.
const coreLabel = "Core"

// sourceView resolves a stored source against the stacks as they are now.
func (s *Service) sourceView(row store.BackupSource, folders []StackFolders) Source {
	src := Source{
		Kind: SourceKind(row.Kind), Stack: row.Stack, Folder: row.Folder, Path: row.Path,
		RemotePath: row.RemotePath, Excludes: parseExcludes(row.Excludes), DeclaredExcludes: []Exclude{},
		SizeOnly: row.SizeOnly,
	}
	switch src.Kind {
	case SourceCore:
		src.Label = coreLabel
	case SourcePath:
		src.Label, src.HostPath = row.Path, row.Path
		if fi, err := os.Stat(row.Path); err != nil || !fi.IsDir() {
			src.Problem = "this folder does not exist on the host"
		}
	case SourceStack:
		src.Label = row.Stack + ": " + row.Folder
		sf, f, ok := findFolder(folders, row.Stack, row.Folder)
		switch {
		case sf.Stack == "":
			src.Problem = "the stack " + row.Stack + " is not installed"
		case !ok:
			src.Problem = "the stack no longer recommends this folder"
		default:
			src.Label, src.HostPath, src.Problem = row.Stack+": "+f.Label, f.HostPath, f.Problem
			if f.Excludes != nil {
				src.DeclaredExcludes = f.Excludes
			}
		}
	}
	return src
}

func runView(r store.BackupRun) BackupRun {
	return BackupRun{
		Id: r.ID, BackupId: r.BackupID, BackupName: r.BackupName, Trigger: BackupRunTrigger(r.Trigger),
		StartedAt: ms(r.StartedAt), FinishedAt: msPtr(r.FinishedAt), Result: BackupRunResult(r.Result),
		Error: r.Error, Files: r.Files, Bytes: r.Bytes,
	}
}

// List returns every backup with its sources, destinations and latest run.
func (s *Service) List(ctx context.Context) ([]Backup, error) {
	rows, err := s.q.ListBackups(ctx)
	if err != nil {
		return nil, err
	}
	sources, err := s.q.ListBackupSources(ctx)
	if err != nil {
		return nil, err
	}
	targets, err := s.q.ListBackupTargets(ctx)
	if err != nil {
		return nil, err
	}
	dests, err := s.q.ListBackupDestinations(ctx)
	if err != nil {
		return nil, err
	}
	destByID := make(map[int64]store.BackupDestination, len(dests))
	for _, d := range dests {
		destByID[d.ID] = d
	}
	last, err := s.q.LastBackupRuns(ctx)
	if err != nil {
		return nil, err
	}
	lastRun := map[int64]store.BackupRun{}
	for _, r := range last {
		if r.BackupID != nil {
			lastRun[*r.BackupID] = r
		}
	}
	succ, err := s.q.LastSuccessfulBackupRuns(ctx)
	if err != nil {
		return nil, err
	}
	lastOK := map[int64]*int64{}
	for _, r := range succ {
		if r.BackupID != nil {
			lastOK[*r.BackupID] = r.FinishedAt
		}
	}
	recent, err := s.q.RecentBackupRuns(ctx, s.now().Add(-recentWindow).UnixMilli())
	if err != nil {
		return nil, err
	}
	recentRuns := map[int64][]BackupRun{}
	for _, r := range recent {
		recentRuns[*r.BackupID] = append(recentRuns[*r.BackupID], runView(r))
	}
	stale, _ := s.staleness(ctx, rows)
	overdue := map[int64]bool{}
	for _, b := range stale {
		overdue[b.ID] = true
	}
	folders := s.stackFolders(ctx)
	now := s.now()
	out := make([]Backup, 0, len(rows))
	for _, row := range rows {
		b := Backup{
			Id: row.ID, Name: row.Name, Enabled: row.Enabled, Schedule: row.Schedule, Bwlimit: row.Bwlimit,
			Transfers: int(row.Transfers), HealthcheckUrl: row.HealthcheckUrl,
			Sources: []Source{}, Destinations: []DestinationRef{}, RecentRuns: recentRuns[row.ID],
			LastSuccess: msPtr(lastOK[row.ID]), Overdue: overdue[row.ID],
			CreatedAt: ms(row.CreatedAt), UpdatedAt: ms(row.UpdatedAt),
		}
		if b.RecentRuns == nil {
			b.RecentRuns = []BackupRun{}
		}
		for _, src := range sources {
			if src.BackupID == row.ID {
				b.Sources = append(b.Sources, s.sourceView(src, folders))
			}
		}
		for _, t := range targets {
			if d, ok := destByID[t.DestinationID]; ok && t.BackupID == row.ID {
				b.Destinations = append(b.Destinations, DestinationRef{
					Id: d.ID, Name: d.Name, Kind: DestinationKind(d.Kind), Ready: destinationReady(d),
				})
			}
		}
		if r, ok := lastRun[row.ID]; ok {
			v := runView(r)
			b.LastRun, b.Running = &v, r.Result == ResultRunning
		}
		if row.Enabled && row.Schedule != "" {
			if sched, err := cron.ParseStandard(row.Schedule); err == nil {
				n := sched.Next(now)
				b.NextRun = &n
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// Get returns one backup.
func (s *Service) Get(ctx context.Context, id int64) (Backup, error) {
	all, err := s.List(ctx)
	if err != nil {
		return Backup{}, err
	}
	for _, b := range all {
		if b.Id == id {
			return b, nil
		}
	}
	return Backup{}, fmt.Errorf("%w: %d", ErrUnknownBackup, id)
}

// Runs returns the run history of one backup, newest first.
func (s *Service) Runs(ctx context.Context, id int64, limit int) ([]BackupRun, error) {
	if _, err := s.backupRow(ctx, id); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.q.ListBackupRuns(ctx, store.ListBackupRunsParams{BackupID: &id, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]BackupRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, runView(r))
	}
	return out, nil
}

// Logs returns the last journal lines of the backup's unit.
func (s *Service) Logs(ctx context.Context, id int64) (string, error) {
	if _, err := s.backupRow(ctx, id); err != nil {
		return "", err
	}
	return s.sd.JournalTail(ctx, unitName(id), 200), nil
}

// Coverage lists every stack's recommended folders with the enabled backups
// that include them and have somewhere to copy to.
func (s *Service) Coverage(ctx context.Context) (Coverage, error) {
	covered, err := s.coveredBy(ctx)
	if err != nil {
		return Coverage{}, err
	}
	out := Coverage{Stacks: []StackCoverage{}}
	for _, sf := range s.stackFolders(ctx) {
		sc := StackCoverage{Stack: sf.Stack, Prepare: sf.Prepare, Folders: []FolderCoverage{}}
		for _, f := range sf.Folders {
			fc := FolderCoverage{
				Key: f.Key, Label: f.Label, HostPath: f.HostPath, Excludes: f.Excludes, Problem: f.Problem,
				CoveredBy: covered[sf.Stack+"\x00"+f.Key],
			}
			if fc.Excludes == nil {
				fc.Excludes = []Exclude{}
			}
			if fc.CoveredBy == nil {
				fc.CoveredBy = []string{}
				out.Uncovered++
			}
			sc.Folders = append(sc.Folders, fc)
		}
		if len(sc.Folders) > 0 {
			out.Stacks = append(out.Stacks, sc)
		}
	}
	return out, nil
}

// coveredBy maps "stack\x00folder" to the names of the enabled backups with a
// destination that include it.
func (s *Service) coveredBy(ctx context.Context) (map[string][]string, error) {
	rows, err := s.q.ListBackups(ctx)
	if err != nil {
		return nil, err
	}
	targets, err := s.q.ListBackupTargets(ctx)
	if err != nil {
		return nil, err
	}
	hasTarget := map[int64]bool{}
	for _, t := range targets {
		hasTarget[t.BackupID] = true
	}
	names := map[int64]string{}
	for _, b := range rows {
		if b.Enabled && hasTarget[b.ID] {
			names[b.ID] = b.Name
		}
	}
	sources, err := s.q.ListBackupSources(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, src := range sources {
		if name, ok := names[src.BackupID]; ok && src.Kind == string(SourceStack) {
			k := src.Stack + "\x00" + src.Folder
			out[k] = append(out[k], name)
		}
	}
	return out, nil
}

// FoldersAdded tells the operator, once, that a stack now recommends folders
// no backup includes. The stacks service calls it after an install or update.
func (s *Service) FoldersAdded(ctx context.Context, stack string, keys []string) {
	s.mu.Lock()
	s.folders = nil
	s.mu.Unlock()
	if len(keys) == 0 || s.notify == nil || !s.notify.Enabled() {
		return
	}
	covered, err := s.coveredBy(ctx)
	if err != nil {
		s.log.Warn("read backup coverage", "error", err)
		return
	}
	n := 0
	for _, k := range keys {
		if len(covered[stack+"\x00"+k]) == 0 {
			n++
		}
	}
	if n == 0 {
		return
	}
	body := fmt.Sprintf("%s recommends %d folders that no backup includes yet.", stack, n)
	if n == 1 {
		body = stack + " recommends a folder that no backup includes yet."
	}
	if _, err := s.notify.Send(ctx, notificationFor(stack, body)); err != nil {
		s.log.Warn("failed to send the not-backed-up notification", "stack", stack, "error", err)
	}
}

// LogUnit is the unit a backup's runs are logged under.
type LogUnit struct{ Name, Unit string }

// LogUnits lists every backup's unit, for the log viewer.
func (s *Service) LogUnits(ctx context.Context) []LogUnit {
	rows, err := s.q.ListBackups(ctx)
	if err != nil {
		return nil
	}
	out := make([]LogUnit, 0, len(rows))
	for _, b := range rows {
		out = append(out, LogUnit{Name: b.Name, Unit: unitName(b.ID)})
	}
	return out
}
