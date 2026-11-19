// Command homelab-daemon is the privileged homelab orchestrator: stacks,
// backups, storage and the HTTP API on a unix socket.
//
// main is a linear composition root: every dependency is constructed here and
// handed to its consumer explicitly.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/ask"
	"homelab-ngx.com/core/internal/audit"
	"homelab-ngx.com/core/internal/auth"
	"homelab-ngx.com/core/internal/backups"
	"homelab-ngx.com/core/internal/cloudflare"
	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/configstore"
	"homelab-ngx.com/core/internal/drafts"
	"homelab-ngx.com/core/internal/hostcfg"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/installer"
	"homelab-ngx.com/core/internal/llm"
	logging "homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/metrics"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/services"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/storage"
	"homelab-ngx.com/core/internal/storage/bcachefs"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/system"
	"homelab-ngx.com/core/internal/systemd"
	"homelab-ngx.com/core/internal/sysupdates"
)

const defaultConfigPath = "/etc/homelab-daemon/config.json"

func main() {
	logging.Init()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "check-config":
			os.Exit(runCheckConfig(os.Args[2:]))
		case "openapi":
			os.Exit(runOpenAPI(os.Args[2:], os.Stdout))
		case "core-deploy":
			os.Exit(runCoreDeploy(os.Args[2:]))
		}
	}
	configPath := flag.String("config", defaultConfigPath, "nix-rendered config.json")
	flag.Parse()
	if err := run(*configPath); err != nil {
		logging.Logger("api").Error("homelab-daemon exiting", "error", err)
		os.Exit(1)
	}
}

// crashNoticeAfter is how long a daemon restarted after a crash must stay up
// before it says so.
const crashNoticeAfter = time.Minute

func run(configPath string) error {
	log := logging.Logger("api")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// ── Config + store ──────────────────────────────────────────────────
	nix, err := config.LoadNix(configPath) // fail fast: nix/Go drift must not boot half-configured
	if err != nil {
		return err
	}
	// The installer stick runs only the installer: no store, stacks or
	// host settings of its own.
	if hostcfg.Detect("/etc/os-release") == hostcfg.ManagedImage && installer.Active() {
		return runInstaller(ctx, nix.Paths.Socket, log)
	}
	if err := os.MkdirAll(filepath.Dir(nix.Paths.DB), 0o750); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	// Stack state lives under the host's cache directory.
	stacks.SetCacheDir(nix.Paths.Cache())
	st, err := store.Open(ctx, nix.Paths.DB, store.Options{Migrate: true})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	cfg := config.NewManager(configPath, configstore.New(st))
	if err := cfg.Load(ctx); err != nil {
		_ = st.Close()
		return fmt.Errorf("load config: %w", err)
	}

	// ── Crash detection ─────────────────────────────────────────────────
	// clean_shutdown_at is set on a clean exit and blanked while running, so
	// a crash leaves it blank. A fresh database has no row yet and no history
	// to judge, so only one that has run before can report a crash.
	prev, ranBefore, _ := st.Meta(ctx, store.MetaCleanShutdown)
	crashed := ranBefore && prev == ""
	if err := st.SetMeta(ctx, store.MetaCleanShutdown, ""); err != nil {
		log.Warn("could not clear clean-shutdown marker", "error", err)
	}

	// ── Build ───────────────────────────────────────────────────────────
	// Notifications go to the channels added on the dashboard; with none
	// they are off. The age key is the one stack variables are sealed with.
	linkBase := os.Getenv("HOMELAB_LINK_BASE")
	channels := notifications.NewChannels(st, stacks.DefaultHost(cmdrunner.OS{}).AgeKeyFile)
	notify := notifications.New(notifications.Config{Targets: channels, LinkBase: linkBase}, notifications.NewDBStore(st), nil)
	if notify.Enabled() {
		log.Info("notifier configured")
	} else {
		log.Info("notifier not configured")
	}
	r := cmdrunner.OS{}

	sd := systemd.New(r)
	sysMod := sysupdates.New(r, nix.Paths.StateDir)
	sysMod.SetNotifier(notify)
	// Only a host on the image has updates.
	sysMod.SetImage(hostcfg.Detect("/etc/os-release") == hostcfg.ManagedImage)
	if exe, err := os.Executable(); err == nil {
		sysMod.SetDeploy(sysupdates.DeployConfig{
			Source: os.Getenv("HOMELAB_CORE_SOURCE"), Socket: nix.Paths.Socket, Exe: exe,
		})
	}
	// The host's own settings: kept and applied by the daemon on the image.
	// The stacks follow their domain, dashboard address and LAN.
	hostSvc := hostcfg.New(st, stacks.DefaultHost(r).AgeKeyFile, hostcfg.Detect("/etc/os-release"), nix,
		&hostcfg.ImageApplier{Runner: r}, logging.Logger("hostcfg"))
	stackHost := stacks.DefaultHost(r)
	// A fresh host has no age key yet: stack variables, channel tokens and
	// backup keys are sealed to it.
	if made, err := agefile.EnsureKey(stackHost.AgeKeyFile); err != nil {
		log.Warn("no host age key", "path", stackHost.AgeKeyFile, "error", err)
	} else if made {
		log.Info("host age key created", "path", stackHost.AgeKeyFile)
	}
	if v, err := hostSvc.Values(ctx); err == nil {
		stacks.SetPrivateDomain(v.Domain)
		stackHost.DashboardURL, stackHost.LAN = v.DashboardURL, v.LAN
	} else {
		log.Error("host settings unreadable", "error", err)
	}
	stackHost.GPU = stacks.GPU{Packages: nix.Stacks.GPU.Packages, LibvaDriver: nix.Stacks.GPU.LibvaDriver}
	stacksSvc := stacks.New(st, stacks.NewNixSource(r), stackHost, notify, logging.Logger("stacks"))
	hostSvc.OnChange(func(v hostcfg.Values) {
		stacks.SetPrivateDomain(v.Domain)
		stacksSvc.SetHostValues(v.DashboardURL, v.LAN)
	})
	svcs := services.New(sd, cfg, notify, nil)
	svcs.SetStacks(stackUnits{stacksSvc})
	bkCfg := backups.DefaultConfig()
	bkCfg.Runner = nix.Paths.BackupRunner
	bkCfg.KeyFile = stacks.DefaultHost(r).AgeKeyFile
	assistant := llm.NewSettings(st, stacks.DefaultHost(r).AgeKeyFile, logging.Logger("llm"))
	draftsSvc := drafts.New(filepath.Join(nix.Paths.StateDir, "drafts"), r, st, stacksSvc, stackHost.GPU, logging.Logger("drafts"))
	draftsSvc.SetAssistant(assistant)
	bkCfg.DBBackupDir, bkCfg.StacksDir, bkCfg.DraftsDir = nix.Paths.DBBackupDir, stacks.StacksStateDir, draftsSvc.Dir()
	bkCfg.Snapshot = func(ctx context.Context) error {
		_, err := st.Snapshot(ctx, nix.Paths.DBBackupDir, nix.DB.SnapshotKeep)
		return err
	}
	bkCfg.PoolMounts = func() []string {
		var out []string
		for _, p := range cfg.Current().Pools {
			out = append(out, p.Mountpoint)
		}
		return out
	}
	bk := backups.New(bkCfg, sd, r, st, stackBackups{stacksSvc}, notify, backups.NewHealthchecks(), logging.Logger("backups"))
	stacksSvc.OnBackupFoldersAdded(bk.FoldersAdded)
	stor := storage.New(bcachefs.New(r), cfg, nil)
	stor.SetPoolDir(nix.Paths.PoolDir)
	stor.UseStore(st)
	stor.UseStackFolders(stacksSvc)
	// metrics.db sits next to homelab.db but outside its snapshots: it is
	// throwaway telemetry. Without it the live charts still work.
	mdb, err := metrics.OpenDB(ctx, filepath.Join(filepath.Dir(nix.Paths.DB), "metrics.db"))
	if err != nil {
		log.Warn("metrics history off", "error", err)
		mdb = nil
	}
	met := metrics.New(metrics.DefaultPaths(), stackScopes{stacksSvc}, mdb, logging.Logger("metrics"))
	stacksSvc.SetUsage(met.Usage)

	// Ask reads the same log viewer list the Logs page does.
	logs := system.New(cfg, r, logSources{stackUnits{stacksSvc}, bk})
	askSvc := ask.New(assistant, ask.Sources{
		Logs: logs, Backups: bk, Stacks: stacksSvc, Deploys: sysMod, Notifications: notify,
	}, logging.Logger("llm"))

	// The security audit reads what the host's configuration says, the
	// stacks, backups and updates, and asks the audit model.
	auditSvc := audit.New(st, assistant, audit.Sources{
		Facts: func(ctx context.Context) (config.Security, config.CloudflareTunnel, string) {
			v, err := hostSvc.Values(ctx)
			if err != nil {
				return nix.Security, nix.CloudflareTunnel, nix.Stacks.Domain
			}
			return v.Security, v.Tunnel, v.Domain
		},
		Probe: func(ctx context.Context, t config.CloudflareTunnel) []cloudflare.HostnameStatus {
			return cloudflare.ProbeHostnames(ctx, nil, t.Hostnames, t.PublicHostnames)
		},
		Runner: r, Stacks: stacksSvc, Backups: bk, Updates: sysMod, Problems: notify, Assistant: assistant,
	}, notify, logging.Logger("llm"))

	handler, webHandler, stackHandler := handlers(deps{
		services: svcs, backups: bk, storage: stor, stacks: stacksSvc,
		sysupdates: sysMod, notifications: notify, channels: channels, hostcfg: hostSvc, config: cfg, runner: r, metrics: met,
		drafts: draftsSvc, assistant: assistant, ask: askSvc, audit: auditSvc,
		auth: auth.New(st, logging.Logger("auth")),
	})

	// ── Run ─────────────────────────────────────────────────────────────
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Serve(gctx, nix.Paths.Socket, "", handler, log) }) // first: the API is up while the pools mount
	// Caddy's way in: the same API behind the dashboard's sign-in.
	g.Go(func() error {
		return httpx.Serve(gctx, httpx.WebSocketPath(nix.Paths.Socket), httpx.WebGroup, webHandler, log)
	})
	// The image host's files are written again at every start: the firewall
	// table and the transient host name are gone after a reboot.
	// On an installed host's first start the installer's host name becomes
	// its setting (and is applied); every start after, the settings apply.
	g.Go(func() error {
		if !hostSvc.AdoptInstall(gctx, installer.SetupFile) {
			hostSvc.Apply(gctx)
		}
		return nil
	})
	// Stacks with an approved api grant reach the daemon here; their
	// firewall opens the port only for them, and every request carries the
	// stack's token.
	stackLn, err := net.Listen("tcp", fmt.Sprintf(":%d", stacks.APIPort))
	if err != nil {
		return fmt.Errorf("stack API listener: %w", err)
	}
	g.Go(func() error { return httpx.ServeListener(gctx, stackLn, stackHandler, log) })
	// Independent of the pools, so they must not wait for AutoMount (a hung
	// mount would otherwise leave the dashboard without the system timeline
	// or DB snapshots).
	g.Go(func() error { sysMod.Start(gctx); return nil })
	// Said only once the daemon has stayed up a while: a daemon that dies at
	// every start would otherwise send one at each of systemd's restarts.
	if crashed && notify.Enabled() {
		g.Go(func() error {
			select {
			case <-gctx.Done():
				return nil
			case <-time.After(crashNoticeAfter):
			}
			if _, err := notify.Send(gctx, notifications.Message{
				Severity: notifications.Alert,
				Title:    "Daemon recovered from crash",
				Body:     "homelab-daemon was restarted after an unclean shutdown. All services are being re-monitored.",
				Path:     "/services",
				Key:      "daemon-crash",
			}); err != nil {
				log.Warn("failed to send crash notification", "error", err)
			}
			return nil
		})
	}
	g.Go(func() error { met.Run(gctx); return nil })
	// An audit a restart cut off goes on with its next chunk.
	g.Go(func() error { auditSvc.Resume(gctx); return nil })
	g.Go(func() error {
		st.RunMaintenance(gctx, nix.DB.SnapshotSchedule, "30 4 * * *", nix.Paths.DBBackupDir,
			nix.DB.SnapshotKeep, time.Duration(nix.DB.RetentionDays)*24*time.Hour)
		return nil
	})

	g.Go(func() error {
		stor.AutoMount(gctx)
		// Stacks bind paths on the pools, so they start after the mount; on
		// their own goroutine, because a stack can take minutes to be healthy.
		g.Go(func() error {
			stacksSvc.StartWanted(gctx)
			stacksSvc.Monitor(gctx)
			return nil
		})
		g.Go(func() error { stacksSvc.RunChecks(gctx); return nil })
		g.Go(func() error { stacksSvc.WatchStore(gctx); return nil })
		// Disk health and folder sizes; folders are only listed once mounted.
		g.Go(func() error { stor.Run(gctx); return nil })
		// Backups read the pools, so they wait for the mount.
		g.Go(func() error { svcs.Watch(gctx); return nil })
		g.Go(func() error { bk.Run(gctx); return nil })
		return nil
	})

	werr := g.Wait()
	if werr != nil {
		log.Error("daemon worker exited with error", "error", werr)
	}
	// Mark the shutdown clean even after a worker error: that is an orderly
	// stop; a crash never reaches this line.
	if err := st.SetMeta(context.Background(), store.MetaCleanShutdown, strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
		log.Warn("failed to mark clean shutdown", "error", err)
	}
	if mdb != nil {
		_ = mdb.Close()
	}
	if err := st.Close(); err != nil {
		log.Warn("closing store", "error", err)
	}
	log.Info("homelab-daemon shutting down")
	return werr
}

// runCoreDeploy is the process an update runs in: the daemon starts it in a
// transient unit, because a core update restarts the daemon.
func runCoreDeploy(args []string) int {
	fs := flag.NewFlagSet("core-deploy", flag.ContinueOnError)
	var o sysupdates.RunOptions
	fs.Int64Var(&o.ID, "id", 0, "update record id")
	fs.StringVar(&o.Kind, "kind", "", "build, download, core-update, image-update or install")
	fs.StringVar(&o.Source, "source", "", "the repository a build fetches")
	fs.StringVar(&o.Socket, "socket", "", "daemon socket")
	fs.StringVar(&o.State, "state", "", "update records file")
	fs.StringVar(&o.Reboot, "reboot", "", "image update: \"\", \"now\" or HH:MM")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.ID == 0 || o.Kind == "" || o.State == "" || o.Socket == "" {
		fmt.Fprintln(os.Stderr, "core-deploy: started by the daemon; see POST /system-updates/image/release")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := sysupdates.RunDeploy(ctx, cmdrunner.OS{}, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// runCheckConfig validates a nix-rendered config.json and prints a summary.
func runCheckConfig(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: homelab-daemon check-config <path>")
		return 2
	}
	n, err := config.LoadNix(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-config:", err)
		return 1
	}
	fmt.Printf("ok: version %d, %d core services, db %s\n", n.Version, len(n.CoreServices), n.Paths.DB)
	return 0
}

// stackScopes adapts the stacks service to the metrics sampler.
type stackScopes struct{ *stacks.Service }

func (a stackScopes) Scopes(ctx context.Context) []metrics.Scope {
	in := a.Service.Measured(ctx)
	out := make([]metrics.Scope, len(in))
	for i, m := range in {
		out[i] = metrics.Scope{Name: m.Name, Units: m.Units}
	}
	return out
}

// stackUnits adapts the stacks service to the service views, so neither
// package imports the other.
type stackUnits struct{ *stacks.Service }

func (a stackUnits) Units(ctx context.Context) []services.StackUnit {
	in := a.Service.Units(ctx)
	out := make([]services.StackUnit, len(in))
	for i, u := range in {
		out[i] = services.StackUnit{Stack: u.Stack, Unit: u.Unit, Name: u.Name, ActiveState: u.ActiveState, SubState: u.SubState, StartedAt: u.StartedAt, Usage: u.Usage}
	}
	return out
}

// stackBackups adapts the stacks service to the backups: the folders the
// stacks recommend, grouped per stack, and their prepare units.
type stackBackups struct{ *stacks.Service }

func (a stackBackups) BackupFolders(ctx context.Context) ([]backups.StackFolders, error) {
	in, err := a.Service.BackupFolders(ctx)
	if err != nil {
		return nil, err
	}
	var out []backups.StackFolders
	at := map[string]int{}
	for _, f := range in {
		i, ok := at[f.Stack]
		if !ok {
			i = len(out)
			at[f.Stack] = i
			out = append(out, backups.StackFolders{Stack: f.Stack, Running: f.Running, Prepare: f.Prepare})
		}
		ex := make([]backups.Exclude, 0, len(f.Excludes))
		for _, e := range f.Excludes {
			ex = append(ex, backups.Exclude{Pattern: e.Pattern, Reason: e.Reason})
		}
		out[i].Folders = append(out[i].Folders, backups.StackFolder{
			Key: f.Key, Label: f.Label, HostPath: f.HostPath, Excludes: ex, Problem: f.Error,
		})
	}
	return out, nil
}

// logSources adapts the stacks and the backups to the log viewer.
type logSources struct {
	stackUnits
	backups *backups.Service
}

func (a logSources) BackupLogUnits(ctx context.Context) []system.BackupUnit {
	if a.backups == nil {
		return nil
	}
	in := a.backups.LogUnits(ctx)
	out := make([]system.BackupUnit, len(in))
	for i, u := range in {
		out[i] = system.BackupUnit{Name: u.Name, Unit: u.Unit}
	}
	return out
}

func (a stackUnits) LogUnits(ctx context.Context) []system.StackUnit {
	in := a.Service.Units(ctx)
	out := make([]system.StackUnit, len(in))
	for i, u := range in {
		out[i] = system.StackUnit{Name: u.Name, Unit: u.Unit, JournalDir: stacks.JournalDir(u.Stack)}
	}
	return out
}
