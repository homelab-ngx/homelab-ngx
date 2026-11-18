package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/sysupdates"
)

var (
	Version   = "0.3.0"
	BuildDate = "2026-06-13"
)

func main() {
	logging.InitCLI()
	app := &cli.App{
		Name:                 "homelab",
		Usage:                "Manage your homelab services, backups, secrets, and health",
		Version:              fmt.Sprintf("%s (built %s)", Version, BuildDate),
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
			secretCmd(),
			authCmd(),
			configCmd(),
			daemonCmd(),
			doctorCmd(),
			notifyCmd(),
			stacksCmd(),
			systemCmd(),
			storageCmd(),
			hostCmd(),
			deployCmd(),
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

// ── deploy ───────────────────────────────────────────────────────────────────

func deployCmd() *cli.Command {
	return &cli.Command{
		Name:  "deploy",
		Usage: "Deploy core from main through the daemon, and wait for the result",
		Subcommands: []*cli.Command{
			{
				Name:   "rollback",
				Usage:  "Switch core back to the generation before",
				Action: func(c *cli.Context) error { return runDeploy(c, "/system-updates/rollback", nil) },
			},
			{
				Name:  "status",
				Usage: "Show the latest deploy",
				Action: func(c *cli.Context) error {
					list, err := call[[]sysupdates.CoreDeploy](c.Context, http.MethodGet, "/system-updates/deploys", nil)
					if err != nil {
						return err
					}
					if len(*list) == 0 {
						return printJSON(nil)
					}
					return printDeploy(c, (*list)[0])
				},
			},
		},
		Action: func(c *cli.Context) error { return runDeploy(c, "/system-updates/deploy", nil) },
	}
}

// ── update (image host) ──────────────────────────────────────────────────────

func updateCmd() *cli.Command {
	return &cli.Command{
		Name:  "update",
		Usage: "Image host: build, install and inspect updates of the image and the core extension",
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
// long it puts up with a daemon that does not answer (the switch restarts it).
var (
	deployPoll = 2 * time.Second
	deployWait = 10 * time.Minute
)

// runDeploy starts a deploy or rollback and follows it to its result. The
// daemon restarts during the switch, so a failed call is retried.
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
			return fmt.Errorf("the daemon no longer lists deploy %d", started.Id)
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

// printDeploy prints a finished deploy: the record as JSON, or a few lines.
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
