package stacks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/storage"
	"homelab-ngx.com/core/internal/store"
)

// Stack variables are the values a stack gets from the host instead of its
// repo: the env, secret and path variables its manifest declares and any the
// operator adds. They are stored encrypted in the stack's state directory, so
// `stacks restore` gets them back with the data and the unencrypted backup of
// that directory cannot read them. Before a boot and after every change the
// env and secret values are decrypted to tmpfs, one file per variable plus an
// env file, and bound read-only at /run/secrets in the stack. A path is not
// delivered as a value: its host directory is bound at its mount.

const (
	varsFileName = "variables.age"
	varsEnvName  = "env"
	// maxVarValue bounds one value.
	maxVarValue = 64 << 10
	// varsFormat is the version of the JSON inside variables.age.
	varsFormat = 1
)

// storedVar is one variable as variables.age holds it.
type storedVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	// UpdatedAt is in unix milliseconds.
	UpdatedAt int64 `json:"updated_at"`
	// ReadOnly binds a declared path read-only although the manifest asks
	// for read-write: the operator's narrowing.
	ReadOnly bool `json:"read_only,omitempty"`
}

// storedFolder is an added folder as variables.age holds it: a host folder
// the operator binds into the stack on top of its manifest.
type storedFolder struct {
	Host     string `json:"host"`
	Mount    string `json:"mount"`
	ReadOnly bool   `json:"read_only"`
	// UpdatedAt is in unix milliseconds.
	UpdatedAt int64 `json:"updated_at"`
}

type varsFile struct {
	Version   int            `json:"version"`
	Variables []storedVar    `json:"variables"`
	Folders   []storedFolder `json:"folders,omitempty"`
}

func (s *Service) varsFile(name string) string { return filepath.Join(s.stateDir(name), varsFileName) }
func (s *Service) varsDir(name string) string  { return filepath.Join(s.host.VarsDir, name) }

// loadVars reads the stack's stored variables. No file is no variables, and
// needs no key. The caller holds s.varsMu.
func (s *Service) loadVars(name string) ([]storedVar, error) {
	f, err := s.loadVarsFile(name)
	return f.Variables, err
}

// loadFolders reads the stack's added folders. The caller holds s.varsMu.
func (s *Service) loadFolders(name string) ([]storedFolder, error) {
	f, err := s.loadVarsFile(name)
	return f.Folders, err
}

// loadBinds reads what the stack's binds come from: its stored variables
// and its added folders. It takes s.varsMu.
func (s *Service) loadBinds(name string) ([]storedVar, []storedFolder, error) {
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	f, err := s.loadVarsFile(name)
	return f.Variables, f.Folders, err
}

func (s *Service) loadVarsFile(name string) (varsFile, error) {
	b, err := os.ReadFile(s.varsFile(name))
	if errors.Is(err, fs.ErrNotExist) {
		return varsFile{}, nil
	}
	if err != nil {
		return varsFile{}, err
	}
	plain, err := agefile.Open(s.host.AgeKeyFile, b)
	if err != nil {
		return varsFile{}, fmt.Errorf("decrypting %s: %w", s.varsFile(name), err)
	}
	var f varsFile
	if err := json.Unmarshal(plain, &f); err != nil {
		return varsFile{}, fmt.Errorf("reading %s: %w", s.varsFile(name), err)
	}
	if f.Version != varsFormat {
		return varsFile{}, fmt.Errorf("reading %s: format %d is not supported", s.varsFile(name), f.Version)
	}
	return f, nil
}

// saveVars stores the variables, keeping the added folders. The caller
// holds s.varsMu.
func (s *Service) saveVars(name string, vars []storedVar) error {
	f, err := s.loadVarsFile(name)
	if err != nil {
		return err
	}
	f.Variables = vars
	return s.saveVarsFile(name, f)
}

// saveFolders stores the added folders, keeping the variables. The caller
// holds s.varsMu.
func (s *Service) saveFolders(name string, folders []storedFolder) error {
	f, err := s.loadVarsFile(name)
	if err != nil {
		return err
	}
	f.Folders = folders
	return s.saveVarsFile(name, f)
}

// saveVarsFile encrypts f to the host's key and replaces the stack's file
// atomically. The caller holds s.varsMu.
func (s *Service) saveVarsFile(name string, f varsFile) error {
	vars := slices.Clone(f.Variables)
	sort.Slice(vars, func(i, j int) bool { return vars[i].Name < vars[j].Name })
	plain, err := json.Marshal(varsFile{Version: varsFormat, Variables: vars, Folders: f.Folders})
	if err != nil {
		return err
	}
	sealed, err := agefile.Seal(s.host.AgeKeyFile, plain)
	if err != nil {
		return err
	}
	if err := mkdirs(0o700, s.stateDir(name)); err != nil {
		return err
	}
	return writeAtomic(s.varsFile(name), sealed, 0o600)
}

// writeAtomic replaces path with data through a temporary file in the same
// directory. A stack that has the directory bound sees the new inode; one
// that has a file open keeps reading the old one.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode) // whatever the umask is
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// envSafe reports whether value reads the same to systemd's EnvironmentFile=
// and to podman's --env-file as a bare NAME=value line: one line, no
// surrounding whitespace, and none of the quotes, backslash or $ either of
// them would interpret.
func envSafe(value string) bool {
	if strings.ContainsAny(value, "\"'\\$") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return false
		}
	}
	first, _ := utf8.DecodeRuneInString(value)
	last, _ := utf8.DecodeLastRuneInString(value)
	return value == "" || (!unicode.IsSpace(first) && !unicode.IsSpace(last))
}

// delivered are the values the stack gets at /run/secrets under manifest m:
// every stored value except paths, and the default of each env variable m
// declares that has none stored. A name m or one of others (the approved
// and fetched manifests) declares as a path is a path.
func delivered(m Manifest, vars []storedVar, others ...Manifest) []storedVar {
	isPath := func(name string) bool {
		for _, mm := range append([]Manifest{m}, others...) {
			if d, ok := mm.Variable(name); ok && d.Kind == VarPath {
				return true
			}
		}
		return false
	}
	out := make([]storedVar, 0, len(vars))
	for _, v := range vars {
		if !isPath(v.Name) {
			out = append(out, v)
		}
	}
	for _, d := range m.Vars() {
		if d.Kind == VarEnv && d.Default != nil && !slices.ContainsFunc(vars, func(v storedVar) bool { return v.Name == d.Name }) {
			out = append(out, storedVar{Name: d.Name, Value: *d.Default})
		}
	}
	return out
}

// resolveAddresses is vars with each word in the value of m's address
// variables replaced by the CIDRs it stands for on a host with nets, so a
// stack that reads the value never sees a word. Addresses and CIDRs stay as
// stored; a word whose network is off is left out.
func resolveAddresses(m Manifest, vars []storedVar, nets Nets) []storedVar {
	out := slices.Clone(vars)
	for i, v := range out {
		d, ok := m.Variable(v.Name)
		if !ok || d.Kind != VarAddress {
			continue
		}
		var entries []string
		for _, e := range strings.Split(v.Value, ",") {
			cidrs, word := nets.Word(e)
			if !word {
				cidrs = []string{e}
			}
			for _, c := range cidrs {
				if c != "" && !slices.Contains(entries, c) {
					entries = append(entries, c)
				}
			}
		}
		out[i].Value = strings.Join(entries, ",")
	}
	return out
}

// secretsInEnv reports whether secrets are lines of /run/secrets/env for a
// stack booting m. From version 7 the env file overrides the environment of
// every service in the stack, so it holds env values only; a secret stays
// its own file.
func secretsInEnv(m Manifest) bool { return m.Version < 7 }

// envFile is /run/secrets/env: the env-safe variables, one per line, and
// the secrets among them only when withSecrets.
func envFile(vars []storedVar, withSecrets bool) string {
	var b strings.Builder
	for _, v := range vars {
		if envSafe(v.Value) && (withSecrets || !v.Secret) {
			b.WriteString(v.Name + "=" + v.Value + "\n")
		}
	}
	return b.String()
}

// renderVars delivers the stack's variables under manifest m, the one it
// boots, to its directory under VarsDir.
func (s *Service) renderVars(ctx context.Context, name string, m Manifest) error {
	var others []Manifest
	if r, err := s.row(ctx, name); err == nil {
		if approved, fetched, err := manifests(r); err == nil {
			others = []Manifest{approved, fetched}
		}
	}
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	vars, err := s.loadVars(name)
	if err != nil {
		return err
	}
	return s.renderVarsLocked(name, m, append(delivered(m, vars, others...), s.builtins(m)...))
}

// renderVarsLocked writes /run/secrets as the stack sees it: a file per
// variable and env, always, even when empty, so EnvironmentFile= never fails.
// The directory stays in place while a stack may have it bound; files are
// replaced, and those of removed variables deleted. vars are the delivered
// ones for a stack booting m; an address variable's words are written as
// the CIDRs they stand for now. The caller holds s.varsMu.
func (s *Service) renderVarsLocked(name string, m Manifest, vars []storedVar) error {
	withSecrets := secretsInEnv(m)
	vars = resolveAddresses(m, vars, s.hostNets())
	if s.host.VarsDir == "" {
		return errors.New("no directory for stack variables is configured")
	}
	dir := s.varsDir(name)
	if err := mkdirs(0o700, s.host.VarsDir, dir); err != nil {
		return err
	}
	sorted := slices.Clone(vars)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	keep := map[string]bool{varsEnvName: true}
	for _, v := range sorted {
		if err := writeAtomic(filepath.Join(dir, v.Name), []byte(v.Value), 0o400); err != nil {
			return err
		}
		keep[v.Name] = true
	}
	if err := writeAtomic(filepath.Join(dir, varsEnvName), []byte(envFile(sorted, withSecrets)), 0o400); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if !keep[e.Name()] {
			errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
		}
	}
	return errors.Join(errs...)
}

// removeVarsDir deletes a removed stack's delivered variables. Its stored
// ones stay with its state directory.
func (s *Service) removeVarsDir(name string) error {
	if s.host.VarsDir == "" {
		return nil
	}
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	return os.RemoveAll(s.varsDir(name))
}

// Where a variable's value comes from.
const (
	SourceStored  = "stored"
	SourceDefault = "default"
	SourceUnset   = "unset"
)

// resolve is the value of the declared variable d: the stored one, else an
// env or path default, else none (source unset). A stored path that is not a
// host path a stack may be given is no value.
func resolve(d Variable, vars []storedVar) (value, source string) {
	if i := slices.IndexFunc(vars, func(v storedVar) bool { return v.Name == d.Name }); i >= 0 {
		if d.Kind != VarPath || validHostPath(vars[i].Value) == nil {
			return vars[i].Value, SourceStored
		}
		return "", SourceUnset
	}
	if d.Default != nil {
		return *d.Default, SourceDefault
	}
	return "", SourceUnset
}

// missingIn lists the required variables of m that resolve to no value.
func missingIn(m Manifest, vars []storedVar) []string {
	var missing []string
	for _, d := range m.Vars() {
		if _, src := resolve(d, vars); d.Required && src == SourceUnset {
			missing = append(missing, d.Name)
		}
	}
	return missing
}

// missingValues lists the required variables m declares that have no value.
func (s *Service) missingValues(name string, m Manifest) ([]string, error) {
	if len(m.Vars()) == 0 {
		return nil, nil
	}
	s.varsMu.Lock()
	vars, err := s.loadVars(name)
	s.varsMu.Unlock()
	if err != nil {
		return nil, err
	}
	return missingIn(m, vars), nil
}

// variablesGate refuses to build the approved commit while a variable its
// manifest requires has no value: the stack would boot without it.
func (s *Service) variablesGate(r store.Stack) error {
	m, ok, err := approvedManifest(r)
	if err != nil || !ok {
		return err
	}
	missing, err := s.missingValues(r.Name, m)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return httpx.Conflict("variables_not_ready",
			"stack %q: commit %s needs a value for %s; set it on the stack's page or with `homelab stacks var set %s <NAME> --value-stdin`",
			r.Name, short(r.ApprovedRev), strings.Join(missing, ", "), r.Name).With("missing", missing)
	}
	return nil
}

// materializePaths stores the default of every path variable m declares
// that has no stored value, so the operator's directory stays where it is
// when a later commit changes the default. Called when m is approved.
func (s *Service) materializePaths(name string, m Manifest) error {
	if !slices.ContainsFunc(m.pathVars(), func(d Variable) bool { return d.Default != nil }) {
		return nil
	}
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	vars, err := s.loadVars(name)
	if err != nil {
		return err
	}
	var add []storedVar
	for _, d := range m.pathVars() {
		if d.Default != nil && !slices.ContainsFunc(vars, func(v storedVar) bool { return v.Name == d.Name }) {
			add = append(add, storedVar{Name: d.Name, Value: *d.Default, UpdatedAt: s.now().UnixMilli()})
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := s.saveVars(name, append(vars, add...)); err != nil {
		return fmt.Errorf("storing the default host paths: %w", err)
	}
	for _, v := range add {
		s.log.Info("stack path stored from its default", "stack", name, "variable", v.Name, "path", v.Value)
	}
	return nil
}

// materializePathsLogged is materializePaths where a failure must not undo
// what called it. Until it succeeds a path resolves to its default.
func (s *Service) materializePathsLogged(name string, m Manifest) {
	if err := s.materializePaths(name, m); err != nil {
		s.log.Warn("stack default host paths not stored", "stack", name, "error", err)
	}
}

// variablesView is the stack's variables as the API shows them, and whether
// every variable the fetched manifest requires has a value. Declared ones
// take their kind and description from the fetched manifest when it declares
// them, else from the approved one.
func variablesView(vars []storedVar, approved, fetched Manifest, tokenSet bool, builtins []storedVar) ([]StackVariable, bool) {
	stored := map[string]storedVar{}
	for _, v := range vars {
		stored[v.Name] = v
	}
	out := []StackVariable{}
	decl := map[string]Variable{}
	var order []string
	for _, m := range []Manifest{approved, fetched} {
		for _, d := range m.Vars() {
			if _, seen := decl[d.Name]; !seen {
				order = append(order, d.Name)
			}
			decl[d.Name] = d // the fetched one wins
		}
	}
	for _, n := range order {
		d := decl[n]
		v, set := stored[n]
		sv := StackVariable{
			Name: n, Description: d.Description, Declared: true, Kind: StackVariableKind(d.Kind), Required: d.Required,
			Default: d.Default, Secret: d.Kind == VarSecret || (d.Kind == VarEnv && v.Secret), Set: set,
			Type: d.Type, Enum: d.Enum,
		}
		if d.Kind == VarPath {
			ro := d.ReadOnly || (set && v.ReadOnly)
			sv.Mount, sv.ReadOnly = d.Mount, &ro
			sv.ReadOnlyByOperator = !d.ReadOnly && set && v.ReadOnly
			sv.Owner, sv.Group = d.Owner, d.Group
		}
		value, src := resolve(d, vars)
		sv.Source = StackVariableSource(src)
		if src != SourceUnset && d.Kind != VarPath {
			sv.InEnv = envSafe(value) && (secretsInEnv(fetched) || !sv.Secret)
		}
		if set {
			t := time.UnixMilli(v.UpdatedAt)
			sv.UpdatedAt = &t
			if !sv.Secret {
				sv.Value = v.Value
			}
		}
		out = append(out, sv)
	}
	if len(fetched.API) > 0 || len(approved.API) > 0 {
		sv := StackVariable{
			Name: BuiltinToken, Kind: StackVariableKindSecret, Declared: true, Builtin: true, Secret: true, Set: tokenSet,
			Description: "The token this stack calls the homelab API with. The host writes it, with HOMELAB_API_URL, to " +
				"/var/lib/homelab-api/env and issues a new one when the stack's API access changes.",
			Source: StackVariableSourceUnset,
		}
		if tokenSet {
			sv.Source = StackVariableSourceStored
		}
		out = append(out, sv)
	}
	for _, b := range builtins {
		out = append(out, StackVariable{
			Name: b.Name, Kind: StackVariableKindEnv, Declared: true, Builtin: true, Set: true, Value: b.Value,
			Source: StackVariableSourceStored, InEnv: envSafe(b.Value), Description: builtinDescriptions[b.Name],
		})
	}
	var own []string
	for n := range stored {
		if _, declared := decl[n]; !declared {
			own = append(own, n)
		}
	}
	sort.Strings(own)
	for _, n := range own {
		v := stored[n]
		t := time.UnixMilli(v.UpdatedAt)
		sv := StackVariable{
			Name: n, Kind: StackVariableKindEnv, Secret: v.Secret, Set: true, Source: StackVariableSourceStored,
			InEnv: envSafe(v.Value) && (secretsInEnv(fetched) || !v.Secret), UpdatedAt: &t,
		}
		if v.Secret {
			sv.Kind = StackVariableKindSecret
		} else {
			sv.Value = v.Value
		}
		out = append(out, sv)
	}
	return out, len(missingIn(fetched, vars)) == 0
}

func badVariableName(n string) *httpx.Problem {
	return httpx.Unprocessable("invalid_variable_name",
		"%q is not a variable name: 1-64 characters of A-Z, 0-9 and '_', starting with a letter", n)
}

// SetVariable stores a value for the stack's variable and delivers it. The
// manifest's declaration (the fetched one's, else the approved one's) sets
// the kind: a declared secret is a secret, a declared env variable is plain,
// and a declared path must be a directory on the host, which takes effect at
// the stack's next restart. A variable no manifest declares is plain unless
// in.Secret makes it one, and a secret stays one. The running stack sees a
// new file at once; a service reads it on its next start.
func (s *Service) SetVariable(ctx context.Context, name, varName string, in StackVariableSet) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	if IsBuiltin(varName) {
		return Stack{}, builtinVariable(varName)
	}
	if !ValidVariableName(varName) {
		return Stack{}, badVariableName(varName)
	}
	if in.Value == "" {
		return Stack{}, httpx.Unprocessable("invalid_value", "a value is required; remove the variable to clear it")
	}
	if len(in.Value) > maxVarValue {
		return Stack{}, httpx.Unprocessable("invalid_value", "the value is longer than %d bytes", maxVarValue)
	}
	approved, fetched, err := manifests(r)
	if err != nil {
		return Stack{}, err
	}
	d, declared := fetched.Variable(varName)
	if !declared {
		d, declared = approved.Variable(varName)
	}
	wantSecret := in.Secret != nil && *in.Secret
	if declared && d.Kind != VarSecret && wantSecret {
		return Stack{}, httpx.Unprocessable("kind_mismatch", "%s cannot be made secret: the stack's manifest declares it as %s", varName, kindName(d.Kind))
	}
	if declared && len(d.Enum) > 0 && !slices.Contains(d.Enum, in.Value) {
		return Stack{}, httpx.Unprocessable("invalid_value", "%s must be one of %s", varName, strings.Join(d.Enum, ", "))
	}
	if in.ReadOnly != nil && (!declared || d.Kind != VarPath) {
		return Stack{}, httpx.Unprocessable("not_a_path", "%s is not a path variable; only a path is bound read-only", varName)
	}
	if in.ReadOnly != nil && !*in.ReadOnly && d.ReadOnly {
		return Stack{}, httpx.Unprocessable("declared_read_only", "%s stays read-only: the stack's manifest binds it so", varName)
	}
	if declared && d.Kind == VarPath {
		in.Value = storage.Canonical(in.Value)
		if err := validHostPath(in.Value); err != nil {
			return Stack{}, httpx.Unprocessable("invalid_path", "%s: %v", varName, err)
		}
		if err := s.statDir(in.Value); err != nil {
			return Stack{}, httpx.Unprocessable("invalid_path", "%s: %v", varName, err)
		}
		s.adoptEmpty(ctx, d, in.Value)
	}
	if declared && d.Kind == VarAddress {
		_, value, err := ParseAddresses(in.Value)
		if err != nil {
			return Stack{}, httpx.Unprocessable("invalid_address", "%s: %v", varName, err)
		}
		in.Value = value
	}
	running, err := s.runningManifest(ctx, r)
	if err != nil {
		return Stack{}, err
	}
	err = func() error {
		s.varsMu.Lock()
		defer s.varsMu.Unlock()
		vars, err := s.loadVars(name)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(vars, func(v storedVar) bool { return v.Name == varName })
		wasSecret := i >= 0 && vars[i].Secret
		var secret bool
		switch {
		case declared && d.Kind == VarSecret:
			secret = true
		case declared && d.Kind == VarPath:
			secret = false // a path is not secret; the operator just typed it
		default: // env, declared or not: a secret stays one
			secret = wasSecret || wantSecret
		}
		if in.Secret != nil && !*in.Secret && secret {
			why := "a secret stays one; remove it and add it again to make it plain"
			if declared && d.Kind == VarSecret {
				why = "the stack's manifest declares it as a secret"
			}
			return httpx.Unprocessable("secret_stays_secret", "%s cannot be made plain: %s", varName, why)
		}
		v := storedVar{Name: varName, Value: in.Value, Secret: secret, UpdatedAt: s.now().UnixMilli()}
		if declared && d.Kind == VarPath {
			v.ReadOnly = i >= 0 && vars[i].ReadOnly
			if in.ReadOnly != nil {
				v.ReadOnly = *in.ReadOnly && !d.ReadOnly
			}
		}
		if i >= 0 {
			vars[i] = v
		} else {
			vars = append(vars, v)
		}
		if err := s.saveVars(name, vars); err != nil {
			return fmt.Errorf("storing %s: %w", varName, err)
		}
		if err := s.renderVarsLocked(name, running, delivered(running, vars, approved, fetched)); err != nil {
			return fmt.Errorf("%s is stored but not delivered to the stack: %w", varName, err)
		}
		s.log.Info("stack variable set", "stack", name, "variable", varName, "secret", secret, "read_only", v.ReadOnly)
		return nil
	}()
	if err != nil {
		return Stack{}, err
	}
	s.refreshHostIDs(ctx, name)
	return s.Get(ctx, name)
}

func kindName(kind string) string {
	switch kind {
	case VarPath:
		return "a path"
	case VarAddress:
		return "an address"
	}
	return "an env variable"
}

// isDir is the default statDir: path must be an existing directory.
func isDir(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%q is not on the host: %w", path, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	return nil
}

// DeleteVariable removes the stack's variable, declared or not. Only the
// operator removes one; the daemon never does on its own.
func (s *Service) DeleteVariable(ctx context.Context, name, varName string) (Stack, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	if IsBuiltin(varName) {
		return Stack{}, builtinVariable(varName)
	}
	if !ValidVariableName(varName) {
		return Stack{}, badVariableName(varName)
	}
	running, err := s.runningManifest(ctx, r)
	if err != nil {
		return Stack{}, err
	}
	approved, fetched, err := manifests(r)
	if err != nil {
		return Stack{}, err
	}
	err = func() error {
		s.varsMu.Lock()
		defer s.varsMu.Unlock()
		vars, err := s.loadVars(name)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(vars, func(v storedVar) bool { return v.Name == varName })
		if i < 0 {
			return httpx.NotFound("unknown_variable", "stack %q has no value for %s", name, varName)
		}
		vars = slices.Delete(vars, i, i+1)
		if err := s.saveVars(name, vars); err != nil {
			return fmt.Errorf("removing %s: %w", varName, err)
		}
		if err := s.renderVarsLocked(name, running, delivered(running, vars, approved, fetched)); err != nil {
			return fmt.Errorf("%s is removed from the store but still delivered to the stack: %w", varName, err)
		}
		s.log.Info("stack variable removed", "stack", name, "variable", varName)
		return nil
	}()
	if err != nil {
		return Stack{}, err
	}
	s.refreshHostIDs(ctx, name)
	return s.Get(ctx, name)
}

// runningManifest is the manifest of the stack's current generation, or the
// approved one while nothing is built (zero when nothing is approved): the
// one whose env defaults the stack is delivered.
func (s *Service) runningManifest(ctx context.Context, r store.Stack) (Manifest, error) {
	g, err := s.generation(ctx, r)
	if err != nil {
		return Manifest{}, err
	}
	if g != nil {
		return decode(g.Manifest)
	}
	m, _, err := approvedManifest(r)
	return m, err
}

// manifests are the stack's approved manifest (zero when nothing is
// approved) and its fetched one.
func manifests(r store.Stack) (approved, fetched Manifest, err error) {
	if approved, _, err = approvedManifest(r); err != nil {
		return
	}
	fetched, err = decode(r.FetchedManifest)
	return
}

// deliveredDigest sums what the stack is handed now: the files in
// /run/secrets and the API env file. Services read them when they start, so
// one that differs from the digest recorded at boot means a restart applies
// a change.
func (s *Service) deliveredDigest(name string) string {
	h := sha256.New()
	if entries, err := os.ReadDir(s.varsDir(name)); err == nil {
		for _, e := range entries { // sorted by name
			b, _ := os.ReadFile(filepath.Join(s.varsDir(name), e.Name()))
			fmt.Fprintf(h, "%s\x00%d\x00", e.Name(), len(b))
			h.Write(b)
		}
	}
	b, _ := os.ReadFile(s.apiEnvFile(name))
	fmt.Fprintf(h, "%s\x00%d\x00", apiEnvName, len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// BuiltinToken is the variable the host fills in for a stack with an API
// grant: its token, delivered in the API env file. It cannot be set.
const BuiltinToken = "HOMELAB_API_TOKEN"

// The variables the host gives every version 8 stack in /run/secrets/env.
const (
	// BuiltinDomain is the host's private domain: a route labelled "photos"
	// is served at photos.$HOMELAB_DOMAIN.
	BuiltinDomain = "HOMELAB_DOMAIN"
	// BuiltinDashboardURL is the address of the homelab dashboard.
	BuiltinDashboardURL = "HOMELAB_DASHBOARD_URL"
)

var builtinDescriptions = map[string]string{
	BuiltinDomain:       "The host's private domain. A route labelled photos is served at photos.<domain>.",
	BuiltinDashboardURL: "The address of the homelab dashboard.",
}

func builtinVariable(name string) *httpx.Problem {
	return httpx.Conflict("builtin_variable", "%s is filled in by the host", name)
}

// builtins are the values the host gives a stack booting m, as delivered
// variables: none before version 8.
func (s *Service) builtins(m Manifest) []storedVar {
	if m.Version < 8 {
		return nil
	}
	var out []storedVar
	if d := PrivateDomain(); d != "" {
		out = append(out, storedVar{Name: BuiltinDomain, Value: d})
	}
	if u := s.hostDashboardURL(); u != "" {
		out = append(out, storedVar{Name: BuiltinDashboardURL, Value: u})
	}
	return out
}

// SecretValues are the stored values of every stack's secret variables, so
// that text leaving the host (the security audit's material) can be scrubbed
// of them. Never returned by the API.
func (s *Service) SecretValues(ctx context.Context) ([]string, error) {
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return nil, err
	}
	s.varsMu.Lock()
	defer s.varsMu.Unlock()
	var out []string
	for _, r := range rows {
		vars, err := s.loadVars(r.Name)
		if err != nil {
			return nil, err
		}
		for _, v := range vars {
			if v.Secret && v.Value != "" {
				out = append(out, v.Value)
			}
		}
	}
	return out, nil
}
