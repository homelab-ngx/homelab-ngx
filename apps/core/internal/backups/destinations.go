package backups

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"homelab-ngx.com/core/backup-runner/runfile"

	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/store"
)

// destConfig is a destination's settings that are not secret, as stored in
// backup_destinations.config. Only the members of its kind are set.
type destConfig struct {
	S3     *S3Settings     `json:"s3,omitempty"`
	Rclone *RcloneSettings `json:"rclone,omitempty"`
	Restic *resticConfig   `json:"restic,omitempty"`
}

type resticConfig struct {
	Repository string `json:"repository"`
	// Init: the daemon generated the password, so the first run may create
	// the repository. A repository added with its own password must exist.
	Init bool `json:"init,omitempty"`
}

// destSecret is a destination's secrets, stored age-encrypted.
type destSecret struct {
	SecretAccessKey string            `json:"secret_access_key,omitempty"`
	Section         string            `json:"section,omitempty"`
	Password        string            `json:"password,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
}

func (s *Service) seal(sec destSecret) ([]byte, error) {
	plain, err := json.Marshal(sec)
	if err != nil {
		return nil, err
	}
	return agefile.Seal(s.cfg.KeyFile, plain)
}

func (s *Service) open(row store.BackupDestination) (destSecret, error) {
	var sec destSecret
	plain, err := agefile.Open(s.cfg.KeyFile, row.Secret)
	if err != nil {
		return sec, fmt.Errorf("decrypt the secrets of destination %q: %w", row.Name, err)
	}
	if err := json.Unmarshal(plain, &sec); err != nil {
		return sec, fmt.Errorf("read the secrets of destination %q: %w", row.Name, err)
	}
	return sec, nil
}

func parseDestConfig(row store.BackupDestination) destConfig {
	var c destConfig
	_ = json.Unmarshal([]byte(row.Config), &c)
	return c
}

var (
	envNameRE   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	sectionRE   = regexp.MustCompile(`^\[([A-Za-z0-9_][A-Za-z0-9_ .+@-]*)\]$`)
	bwlimitRE   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[KMG]?$`)
	urlSecretRE = regexp.MustCompile(`://[^/@\s]*:[^/@\s]+@`)
)

// cleanName trims a name and refuses an empty or unprintable one.
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", invalid("invalid_name", "a name has 1 to 64 characters")
	}
	if hasControl(name) {
		return "", invalid("invalid_name", "a name cannot contain control characters")
	}
	return name, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// rcloneSection checks a pasted rclone config section: one [name] header
// followed by key = value lines, one of them the type.
func rcloneSection(text string) (name, typ, clean string, err error) {
	var b strings.Builder
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
			continue
		case strings.HasPrefix(line, "["):
			m := sectionRE.FindStringSubmatch(line)
			if m == nil {
				return "", "", "", invalid("invalid_rclone_config", "%q is not a valid remote name", line)
			}
			if name != "" {
				return "", "", "", invalid("invalid_rclone_config", "paste one remote only; found [%s] and [%s]", name, m[1])
			}
			name = m[1]
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok || name == "" {
				return "", "", "", invalid("invalid_rclone_config", "expected a [name] line followed by key = value lines")
			}
			if strings.TrimSpace(k) == "type" {
				typ = strings.TrimSpace(v)
			}
		}
		b.WriteString(line + "\n")
	}
	if name == "" || typ == "" {
		return "", "", "", invalid("invalid_rclone_config", "the config needs a [name] line and a type")
	}
	return name, typ, b.String(), nil
}

// build validates in against the destination's kind and returns what to
// store. old is the stored secret on an update, nil on a create.
func buildDestination(in DestinationInput, old *destSecret) (destConfig, destSecret, error) {
	var cfg destConfig
	var sec destSecret
	if old != nil {
		sec = *old
	}
	switch in.Kind {
	case KindS3:
		if in.S3 == nil {
			return cfg, sec, invalid("invalid_destination", "an s3 destination needs its s3 settings")
		}
		v := *in.S3
		u, err := url.Parse(strings.TrimSpace(v.Endpoint))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return cfg, sec, invalid("invalid_endpoint", "the endpoint is a URL such as https://s3.example.com")
		}
		bucket := strings.TrimSpace(v.Bucket)
		if bucket == "" || strings.ContainsAny(bucket, "/ ") {
			return cfg, sec, invalid("invalid_bucket", "the bucket is a name without slashes")
		}
		for _, f := range []string{v.Region, v.Prefix, v.AccessKeyId, v.SecretAccessKey} {
			if hasControl(f) {
				return cfg, sec, invalid("invalid_destination", "settings cannot contain control characters")
			}
		}
		if strings.TrimSpace(v.AccessKeyId) == "" {
			return cfg, sec, invalid("invalid_destination", "the access key id is required")
		}
		if v.SecretAccessKey != "" {
			sec.SecretAccessKey = v.SecretAccessKey
		}
		if sec.SecretAccessKey == "" {
			return cfg, sec, invalid("invalid_destination", "the secret access key is required")
		}
		cfg.S3 = &S3Settings{
			Endpoint: u.String(), Region: strings.TrimSpace(v.Region), Bucket: bucket,
			Prefix: strings.Trim(strings.TrimSpace(v.Prefix), "/"), AccessKeyId: strings.TrimSpace(v.AccessKeyId),
		}
	case KindRclone:
		if in.Rclone == nil {
			return cfg, sec, invalid("invalid_destination", "an rclone destination needs its rclone settings")
		}
		if strings.TrimSpace(in.Rclone.Config) != "" {
			_, _, clean, err := rcloneSection(in.Rclone.Config)
			if err != nil {
				return cfg, sec, err
			}
			sec.Section = clean
		}
		if sec.Section == "" {
			return cfg, sec, invalid("invalid_rclone_config", "paste the remote's rclone config section")
		}
		name, typ, _, err := rcloneSection(sec.Section)
		if err != nil {
			return cfg, sec, err
		}
		p := strings.TrimSpace(in.Rclone.Path)
		if hasControl(p) {
			return cfg, sec, invalid("invalid_destination", "the path cannot contain control characters")
		}
		cfg.Rclone = &RcloneSettings{Remote: name, Type: typ, Path: p}
	case KindRestic:
		if in.Restic == nil {
			return cfg, sec, invalid("invalid_destination", "a restic destination needs its restic settings")
		}
		repo := strings.TrimSpace(in.Restic.Repository)
		if repo == "" || hasControl(repo) {
			return cfg, sec, invalid("invalid_repository", "the repository is required")
		}
		if urlSecretRE.MatchString(repo) {
			return cfg, sec, invalid("invalid_repository",
				"leave the password out of the repository URL and add it as a credential (e.g. RESTIC_REST_PASSWORD)")
		}
		if in.Restic.Env != nil {
			env := map[string]string{}
			for k, v := range *in.Restic.Env {
				if !envNameRE.MatchString(k) || strings.HasPrefix(k, "RESTIC_PASSWORD") || k == "RESTIC_REPOSITORY" ||
					k == "RESTIC_CACHE_DIR" || k == "PATH" || k == "HOME" {
					return cfg, sec, invalid("invalid_credential", "%q cannot be used as a credential name", k)
				}
				if strings.ContainsAny(v, "\n\r\x00") {
					return cfg, sec, invalid("invalid_credential", "the value of %s cannot span lines", k)
				}
				env[k] = v
			}
			sec.Env = env
		}
		cfg.Restic = &resticConfig{Repository: repo}
		if old == nil {
			if in.Restic.Password != "" {
				sec.Password = in.Restic.Password
			} else {
				cfg.Restic.Init = true
			}
		}
		for _, n := range []int{in.Keep.Daily, in.Keep.Weekly, in.Keep.Monthly, in.Keep.Yearly} {
			if n < 0 || n > 9999 {
				return cfg, sec, invalid("invalid_keep", "a keep number is between 0 and 9999")
			}
		}
	default:
		return cfg, sec, invalid("invalid_destination", "kind is s3, rclone or restic")
	}
	return cfg, sec, nil
}

// newPassword returns 32 random bytes as text that survives being typed.
func newPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Service) destination(ctx context.Context, id int64) (store.BackupDestination, error) {
	row, err := s.q.GetBackupDestination(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("%w: %d", ErrUnknownDestination, id)
	}
	return row, err
}

func destinationView(row store.BackupDestination, usedBy []string) Destination {
	cfg := parseDestConfig(row)
	d := Destination{
		Id: row.ID, Name: row.Name, Kind: DestinationKind(row.Kind), DeleteRemoved: row.DeleteRemoved,
		Keep: Keep{
			Daily: int(row.KeepDaily), Weekly: int(row.KeepWeekly), Monthly: int(row.KeepMonthly), Yearly: int(row.KeepYearly),
		},
		PasswordConfirmed: row.PasswordConfirmedAt != nil,
		S3:                cfg.S3, Rclone: cfg.Rclone,
		UsedBy: usedBy, CreatedAt: ms(row.CreatedAt), UpdatedAt: ms(row.UpdatedAt),
	}
	if d.UsedBy == nil {
		d.UsedBy = []string{}
	}
	if cfg.Restic != nil {
		d.Restic = &ResticSettings{Repository: cfg.Restic.Repository, EnvNames: []string{}}
	}
	return d
}

// ready reports whether a run may use the destination: a restic password
// must be confirmed as saved first.
func destinationReady(row store.BackupDestination) bool {
	return row.Kind != string(KindRestic) || row.PasswordConfirmedAt != nil
}

// usedBy maps a destination id to the names of the backups copying to it.
func (s *Service) usedBy(ctx context.Context) (map[int64][]string, error) {
	backups, err := s.q.ListBackups(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(backups))
	for _, b := range backups {
		names[b.ID] = b.Name
	}
	targets, err := s.q.ListBackupTargets(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64][]string{}
	for _, t := range targets {
		out[t.DestinationID] = append(out[t.DestinationID], names[t.BackupID])
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out, nil
}

// Destinations lists the destinations, without secrets.
func (s *Service) Destinations(ctx context.Context) ([]Destination, error) {
	rows, err := s.q.ListBackupDestinations(ctx)
	if err != nil {
		return nil, err
	}
	used, err := s.usedBy(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Destination, 0, len(rows))
	for _, row := range rows {
		d := destinationView(row, used[row.ID])
		if d.Restic != nil {
			// The names of the stored credentials help the operator see what is
			// set; the secret is only opened for them.
			if sec, err := s.open(row); err == nil {
				for k := range sec.Env {
					d.Restic.EnvNames = append(d.Restic.EnvNames, k)
				}
				sort.Strings(d.Restic.EnvNames)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Service) destinationByID(ctx context.Context, id int64) (Destination, error) {
	all, err := s.Destinations(ctx)
	if err != nil {
		return Destination{}, err
	}
	for _, d := range all {
		if d.Id == id {
			return d, nil
		}
	}
	return Destination{}, fmt.Errorf("%w: %d", ErrUnknownDestination, id)
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CreateDestination stores a new destination. For a restic repository
// without a password it generates one and returns it, this once.
func (s *Service) CreateDestination(ctx context.Context, in DestinationInput) (DestinationCreated, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return DestinationCreated{}, err
	}
	cfg, sec, err := buildDestination(in, nil)
	if err != nil {
		return DestinationCreated{}, err
	}
	now := s.now().UnixMilli()
	generated := ""
	var confirmed *int64
	if in.Kind == KindRestic {
		if cfg.Restic.Init {
			if generated, err = newPassword(); err != nil {
				return DestinationCreated{}, err
			}
			sec.Password = generated
		} else {
			// The operator typed the password of a repository they already have.
			confirmed = &now
		}
	}
	sealed, err := s.seal(sec)
	if err != nil {
		return DestinationCreated{}, fmt.Errorf("encrypt the destination's secrets: %w", err)
	}
	cfgJSON, _ := json.Marshal(cfg)
	id, err := s.q.InsertBackupDestination(ctx, store.InsertBackupDestinationParams{
		Name: name, Kind: string(in.Kind), Config: string(cfgJSON), DeleteRemoved: in.DeleteRemoved && in.Kind != KindRestic,
		KeepDaily: int64(in.Keep.Daily), KeepWeekly: int64(in.Keep.Weekly),
		KeepMonthly: int64(in.Keep.Monthly), KeepYearly: int64(in.Keep.Yearly),
		Secret: sealed, PasswordConfirmedAt: confirmed, CreatedAt: now, UpdatedAt: now,
	})
	if isUnique(err) {
		return DestinationCreated{}, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	if err != nil {
		return DestinationCreated{}, err
	}
	d, err := s.destinationByID(ctx, id)
	if err != nil {
		return DestinationCreated{}, err
	}
	s.log.Info("backup destination created", "destination", name, "kind", in.Kind)
	return DestinationCreated{Destination: d, GeneratedPassword: generated}, nil
}

// DuplicateDestination stores a copy of a destination under the first free
// name of the form "<name>-copy", "<name>-copy2", "<name>-copy3". The copy
// keeps the settings and the sealed secrets, so it reaches the same storage.
func (s *Service) DuplicateDestination(ctx context.Context, id int64) (Destination, error) {
	row, err := s.destination(ctx, id)
	if err != nil {
		return Destination{}, err
	}
	rows, err := s.q.ListBackupDestinations(ctx)
	if err != nil {
		return Destination{}, err
	}
	taken := make(map[string]bool, len(rows))
	for _, r := range rows {
		taken[r.Name] = true
	}
	name := row.Name + "-copy"
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-copy%d", row.Name, n)
	}
	now := s.now().UnixMilli()
	newID, err := s.q.InsertBackupDestination(ctx, store.InsertBackupDestinationParams{
		Name: name, Kind: row.Kind, Config: row.Config, DeleteRemoved: row.DeleteRemoved,
		KeepDaily: row.KeepDaily, KeepWeekly: row.KeepWeekly, KeepMonthly: row.KeepMonthly, KeepYearly: row.KeepYearly,
		Secret: row.Secret, PasswordConfirmedAt: row.PasswordConfirmedAt, CreatedAt: now, UpdatedAt: now,
	})
	if isUnique(err) {
		return Destination{}, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	if err != nil {
		return Destination{}, err
	}
	s.log.Info("backup destination duplicated", "destination", name, "from", row.Name)
	return s.destinationByID(ctx, newID)
}

// UpdateDestination replaces a destination's settings. Its kind stays, a
// secret left empty is kept, and a restic password never changes.
func (s *Service) UpdateDestination(ctx context.Context, id int64, in DestinationInput) (Destination, error) {
	row, err := s.destination(ctx, id)
	if err != nil {
		return Destination{}, err
	}
	if string(in.Kind) != row.Kind {
		return Destination{}, invalid("invalid_destination", "a destination's kind cannot change; add a new destination")
	}
	if in.Restic != nil && in.Restic.Password != "" {
		return Destination{}, invalid("invalid_destination", "a repository's password cannot be changed here")
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return Destination{}, err
	}
	old, err := s.open(row)
	if err != nil {
		return Destination{}, err
	}
	cfg, sec, err := buildDestination(in, &old)
	if err != nil {
		return Destination{}, err
	}
	if cfg.Restic != nil {
		cfg.Restic.Init = parseDestConfig(row).Restic != nil && parseDestConfig(row).Restic.Init
	}
	sealed, err := s.seal(sec)
	if err != nil {
		return Destination{}, fmt.Errorf("encrypt the destination's secrets: %w", err)
	}
	cfgJSON, _ := json.Marshal(cfg)
	err = s.q.UpdateBackupDestination(ctx, store.UpdateBackupDestinationParams{
		Name: name, Config: string(cfgJSON), DeleteRemoved: in.DeleteRemoved && in.Kind != KindRestic,
		KeepDaily: int64(in.Keep.Daily), KeepWeekly: int64(in.Keep.Weekly),
		KeepMonthly: int64(in.Keep.Monthly), KeepYearly: int64(in.Keep.Yearly),
		Secret: sealed, UpdatedAt: s.now().UnixMilli(), ID: id,
	})
	if isUnique(err) {
		return Destination{}, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	if err != nil {
		return Destination{}, err
	}
	return s.destinationByID(ctx, id)
}

// DeleteDestination removes a destination no backup copies to.
func (s *Service) DeleteDestination(ctx context.Context, id int64) error {
	row, err := s.destination(ctx, id)
	if err != nil {
		return err
	}
	n, err := s.q.CountBackupDestinationUses(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %s", ErrDestinationInUse, row.Name)
	}
	if err := s.q.DeleteBackupDestination(ctx, id); err != nil {
		return err
	}
	s.log.Info("backup destination deleted", "destination", row.Name)
	return nil
}

// ConfirmPassword records that the operator saved a restic repository's
// password, which lets runs use the destination.
func (s *Service) ConfirmPassword(ctx context.Context, id int64) (Destination, error) {
	row, err := s.destination(ctx, id)
	if err != nil {
		return Destination{}, err
	}
	if row.Kind != string(KindRestic) {
		return Destination{}, invalid("invalid_destination", "only a restic destination has a password")
	}
	if row.PasswordConfirmedAt == nil {
		now := s.now().UnixMilli()
		if err := s.q.ConfirmBackupDestinationPassword(ctx, store.ConfirmBackupDestinationPasswordParams{
			PasswordConfirmedAt: &now, UpdatedAt: now, ID: id,
		}); err != nil {
			return Destination{}, err
		}
	}
	return s.destinationByID(ctx, id)
}

// RevealPassword returns a restic repository's password. confirm must be the
// destination's name; every reveal is logged.
func (s *Service) RevealPassword(ctx context.Context, id int64, confirm string) (string, error) {
	row, err := s.destination(ctx, id)
	if err != nil {
		return "", err
	}
	if row.Kind != string(KindRestic) {
		return "", invalid("invalid_destination", "only a restic destination has a password")
	}
	if strings.TrimSpace(confirm) != row.Name {
		return "", ErrConfirm
	}
	sec, err := s.open(row)
	if err != nil {
		return "", err
	}
	s.log.Info("restic repository password revealed", "destination", row.Name)
	return sec.Password, nil
}

// destinationSpec turns a stored destination into the runner's terms, and
// names the local directories the run must be allowed to write.
func (s *Service) destinationSpec(row store.BackupDestination) (runfile.Destination, []string, error) {
	sec, err := s.open(row)
	if err != nil {
		return runfile.Destination{}, nil, err
	}
	cfg := parseDestConfig(row)
	d := runfile.Destination{ID: row.ID, Name: row.Name}
	var write []string
	switch DestinationKind(row.Kind) {
	case KindS3:
		if cfg.S3 == nil {
			return d, nil, fmt.Errorf("destination %q has no s3 settings", row.Name)
		}
		var b strings.Builder
		b.WriteString("[dest]\ntype = s3\nprovider = Other\nenv_auth = false\nno_check_bucket = true\n")
		fmt.Fprintf(&b, "access_key_id = %s\nsecret_access_key = %s\nendpoint = %s\n",
			cfg.S3.AccessKeyId, sec.SecretAccessKey, cfg.S3.Endpoint)
		if cfg.S3.Region != "" {
			fmt.Fprintf(&b, "region = %s\n", cfg.S3.Region)
		}
		d.Kind, d.Config, d.DeleteRemoved = runfile.KindRclone, b.String(), row.DeleteRemoved
		d.Remote = "dest:" + strings.TrimSuffix(cfg.S3.Bucket+"/"+cfg.S3.Prefix, "/")
		d.Bucket = "dest:" + cfg.S3.Bucket
	case KindRclone:
		if cfg.Rclone == nil {
			return d, nil, fmt.Errorf("destination %q has no rclone settings", row.Name)
		}
		d.Kind, d.Config, d.DeleteRemoved = runfile.KindRclone, sec.Section, row.DeleteRemoved
		d.Remote = cfg.Rclone.Remote + ":" + cfg.Rclone.Path
		if cfg.Rclone.Type == "local" && filepath.IsAbs(cfg.Rclone.Path) {
			write = append(write, filepath.Clean(cfg.Rclone.Path))
		}
	case KindRestic:
		if cfg.Restic == nil {
			return d, nil, fmt.Errorf("destination %q has no restic settings", row.Name)
		}
		d.Kind, d.Repository, d.Password, d.Env, d.Init = runfile.KindRestic, cfg.Restic.Repository, sec.Password, sec.Env, cfg.Restic.Init
		d.Keep = runfile.Keep{
			Daily: int(row.KeepDaily), Weekly: int(row.KeepWeekly), Monthly: int(row.KeepMonthly), Yearly: int(row.KeepYearly),
		}
		if p := strings.TrimPrefix(cfg.Restic.Repository, "local:"); filepath.IsAbs(p) {
			write = append(write, filepath.Clean(p))
		}
	default:
		return d, nil, fmt.Errorf("destination %q has unknown kind %q", row.Name, row.Kind)
	}
	return d, write, nil
}

// testTimeout bounds a destination test.
const testTimeout = 90 * time.Second

// TestDestination tries a destination the way a run would, in the same
// sandbox, without copying anything.
func (s *Service) TestDestination(ctx context.Context, id int64) (DestinationTest, error) {
	row, err := s.destination(ctx, id)
	if err != nil {
		return DestinationTest{}, err
	}
	d, write, err := s.destinationSpec(row)
	if err != nil {
		return DestinationTest{Detail: err.Error()}, nil
	}
	dir := filepath.Join(s.cfg.RunDir, fmt.Sprintf("test-%d-%d", id, s.now().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return DestinationTest{}, err
	}
	defer os.RemoveAll(dir)
	spec := &runfile.Spec{
		Version: runfile.Version, Backup: "test", CheckOnly: true, WorkDir: dir, CacheDir: s.cfg.CacheDir,
		Destinations: []runfile.Destination{d},
	}
	specPath, statePath := filepath.Join(dir, "spec.json"), filepath.Join(dir, "state.json")
	if err := runfile.WriteSpec(specPath, spec); err != nil {
		return DestinationTest{}, err
	}
	tctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	unit := fmt.Sprintf("homelab-backup-test-%d", id)
	_, runErr := s.launch(tctx, unit, "Homelab backup destination test: "+row.Name, specPath, statePath, write, true)
	st, err := runfile.ReadState(statePath)
	if err != nil {
		if runErr != nil {
			return DestinationTest{Detail: "the test could not run: " + runErr.Error()}, nil
		}
		return DestinationTest{Detail: "the test left no result"}, nil
	}
	for _, it := range st.Items {
		if it.Kind != runfile.ItemDestination {
			continue
		}
		if it.Result == runfile.ResultSuccess {
			detail := "The destination answered."
			if it.Note != "" {
				detail = strings.ToUpper(it.Note[:1]) + it.Note[1:] + "."
			}
			return DestinationTest{Ok: true, Detail: detail}, nil
		}
		return DestinationTest{Detail: it.Error}, nil
	}
	return DestinationTest{Detail: st.Error}, nil
}
