package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/power"
	"homelab-ngx.com/core/internal/system"
	"homelab-ngx.com/core/internal/sysupdates"
)

// cliVersion is this CLI's version: the core extension in use, whose file
// name carries the release (no build can know it in advance).
func cliVersion() string {
	v, c := system.BuildInfo()
	if c == "" {
		return v
	}
	if len(c) > 12 {
		c = c[:12]
	}
	return fmt.Sprintf("%s (commit %s)", v, c)
}

// printVersions is --version: the CLI's, the running daemon's and the booted
// image's versions, which differ while an update is half done (a core
// switched before the daemon restarted, or a staged image before a reboot).
func printVersions(c *cli.Context) {
	cliV, _ := system.BuildInfo()
	daemonV := "unreachable"
	ctx, cancel := context.WithTimeout(c.Context, 2*time.Second)
	defer cancel()
	if d, err := call[system.Version](ctx, http.MethodGet, "/version", nil); err == nil {
		daemonV = d.Version
	}
	image := sysupdates.BootedImage()
	if image == "" {
		image = "none (not an image host)"
	}
	fmt.Printf("homelab %s\n", cliVersion())
	fmt.Printf("daemon  %s\n", daemonV)
	fmt.Printf("image   %s\n", image)
	if daemonV != "unreachable" && daemonV != cliV {
		fmt.Println("The daemon runs another core than this CLI's: it has not restarted since the core changed.")
	}
}

func main() {
	logging.InitCLI()
	cli.VersionPrinter = printVersions
	app := &cli.App{
		Name:                 "homelab",
		Usage:                "Manage your homelab services, backups, secrets, and health",
		Version:              cliVersion(),
		EnableBashCompletion: true,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "json",
				Usage: "output as JSON",
			},
		},
		Commands: []*cli.Command{
			servicesCmd(),
			backupCmd(),
			authCmd(),
			configCmd(),
			daemonCmd(),
			doctorCmd(),
			notifyCmd(),
			stacksCmd(),
			systemCmd(),
			storageCmd(),
			hostCmd(),
			updateCmd(),
			completionCmd(),
		},
		Action: func(c *cli.Context) error {
			if c.Args().First() == "" {
				return cli.ShowAppHelp(c)
			}
			return fmt.Errorf("unknown command %q — run 'homelab help'", c.Args().First())
		},
	}

	if err := app.Run(os.Args); err != nil {
		renderError(os.Stderr, jsonRequested(os.Args[1:]), err)
		os.Exit(1)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func die(format string, args ...any) {
	renderError(os.Stderr, jsonRequested(os.Args[1:]), fmt.Errorf(format, args...))
	os.Exit(1)
}

func jsonRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

func jsonFlag(c *cli.Context) bool {
	for _, ctx := range c.Lineage() {
		if ctx.Bool("json") {
			return true
		}
	}
	return false
}

// ── update ───────────────────────────────────────────────────────────────────

func updateCmd() *cli.Command {
	return &cli.Command{
		Name:  "update",
		Usage: "Build, download, install and inspect updates of the image and the core extension",
		Subcommands: []*cli.Command{
			{
				Name:  "status",
				Usage: "Show the booted, installed and staged versions",
				Action: func(c *cli.Context) error {
					st, err := call[sysupdates.ImageStatus](c.Context, http.MethodGet, "/system-updates/image", nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(st)
					}
					fmt.Printf("image %s booted\n", st.Booted)
					for _, v := range st.Installed {
						if v.Version != st.Booted {
							fmt.Printf("  %s %s\n", v.Version, v.State)
						}
					}
					if st.FellBackFrom != "" {
						fmt.Printf("  fell back from %s\n", st.FellBackFrom)
					}
					if len(st.Available) > 0 {
						fmt.Printf("  staged: %s\n", strings.Join(st.Available, ", "))
					}
					switch r := st.Release; {
					case r.Error != "":
						fmt.Printf("  release check failed: %s\n", r.Error)
					case r.Latest != "":
						fmt.Printf("  newest release %s\n", r.Latest)
					}
					fmt.Printf("core %s\n", st.Core.Active)
					if len(st.Core.Available) > 0 {
						fmt.Printf("  staged: %s\n", strings.Join(st.Core.Available, ", "))
					}
					if p, err := call[power.PowerStatus](c.Context, http.MethodGet, "/system/power", nil); err == nil && p.Scheduled != nil {
						fmt.Printf("%s scheduled at %s (homelab system cancel calls it off)\n", p.Scheduled.Action, p.Scheduled.At.Local().Format("2006-01-02 15:04"))
					}
					return nil
				},
			},
			{
				Name:   "download",
				Usage:  "Look for a newer release, download it and stage it; nothing is installed",
				Action: func(c *cli.Context) error { return runDeploy(c, "/system-updates/image/download", nil) },
			},
			{
				Name:   "build",
				Usage:  "Build the image and the core extension from main and stage them",
				Action: func(c *cli.Context) error { return runDeploy(c, "/system-updates/image/build", nil) },
			},
			{
				Name:   "core",
				Usage:  "Update the core extension to the newest staged one, without a reboot",
				Action: func(c *cli.Context) error { return runDeploy(c, "/system-updates/image/core", nil) },
			},
			{
				Name:  "image",
				Usage: "Install the newest staged image into the free slot",
				Flags: []cli.Flag{&cli.StringFlag{Name: "reboot", Usage: `"now", or a time today such as 03:30; unset: at the next reboot`}},
				Action: func(c *cli.Context) error {
					return runDeploy(c, "/system-updates/image/install", map[string]string{"reboot": c.String("reboot")})
				},
			},
		},
	}
}

// deployPoll is how often runDeploy asks for the result; deployWait is how
// long it puts up with a daemon that does not answer (a core update restarts
// it).
var (
	deployPoll = 2 * time.Second
	deployWait = 10 * time.Minute
)

// runDeploy starts an update operation and follows it to its result. The
// daemon restarts during a core update, so a failed call is retried.
func runDeploy(c *cli.Context, path string, body any) error {
	started, err := call[sysupdates.CoreDeploy](c.Context, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	phase, silentSince := "", time.Now()
	for {
		select {
		case <-c.Context.Done():
			return c.Context.Err()
		case <-time.After(deployPoll):
		}
		list, err := call[[]sysupdates.CoreDeploy](c.Context, http.MethodGet, "/system-updates/deploys", nil)
		if err != nil {
			if time.Since(silentSince) > deployWait {
				return fmt.Errorf("the daemon has not answered for %s: %w", deployWait, err)
			}
			continue
		}
		silentSince = time.Now()
		var d *sysupdates.CoreDeploy
		for i := range *list {
			if (*list)[i].Id == started.Id {
				d = &(*list)[i]
			}
		}
		if d == nil {
			return fmt.Errorf("the daemon no longer lists update %d", started.Id)
		}
		if d.Phase != phase && !jsonFlag(c) {
			phase = d.Phase
			fmt.Fprintln(os.Stderr, "►", phase)
		}
		if d.Result == sysupdates.DeployRunning {
			continue
		}
		if err := printDeploy(c, *d); err != nil {
			return err
		}
		if d.Result != sysupdates.DeployHealthy {
			return cli.Exit("", 1)
		}
		return nil
	}
}

// printDeploy prints a finished update: the record as JSON, or a few lines.
// The log is left out of the text form unless it failed.
func printDeploy(c *cli.Context, d sysupdates.CoreDeploy) error {
	if jsonFlag(c) {
		return printJSON(d)
	}
	rev := d.Rev
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if d.Version != "" {
		rev = d.Version
	}
	fmt.Printf("%s %s: %s\n", d.Kind, rev, d.Result)
	if d.Reason != "" {
		fmt.Println(d.Reason)
	}
	for _, u := range d.FailedUnits {
		fmt.Println("failed unit:", u)
	}
	return nil
}

// ── completion ───────────────────────────────────────────────────────────────

func completionCmd() *cli.Command {
	return &cli.Command{
		Name:  "completion",
		Usage: "Generate shell completion script",
		Subcommands: []*cli.Command{
			{
				Name:  "bash",
				Usage: "Generate bash completion script",
				Action: func(c *cli.Context) error {
					fmt.Println(`# Add to ~/.bashrc:
# eval "$(homelab completion bash)"
_homelab() {
    COMPREPLY=($(homelab "${COMP_WORDS[@]:1}" --generate-bash-completion 2>/dev/null))
    return 0
}
complete -F _homelab homelab`)
					return nil
				},
			},
			{
				Name:  "zsh",
				Usage: "Generate zsh completion script",
				Action: func(c *cli.Context) error {
					fmt.Println(`# Add to ~/.zshrc:
# autoload -U compinit && compinit  (required once in ~/.zshrc)
# eval "$(homelab completion zsh)"
_homelab() {
    local -a completions
    completions=(${(f)"$(homelab ${words[2,-1]} --generate-bash-completion 2>/dev/null)"})
    compadd -a completions
}
compdef _homelab homelab`)
					return nil
				},
			},
		},
	}
}

// ── input helpers ────────────────────────────────────────────────────────────

func readLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if err := exec.Command("stty", "-F", "/dev/tty", "-echo").Run(); err != nil {
		return "", fmt.Errorf("stty -echo: %w", err)
	}
	defer func() { _ = exec.Command("stty", "-F", "/dev/tty", "echo").Run() }()
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(line), nil
}
