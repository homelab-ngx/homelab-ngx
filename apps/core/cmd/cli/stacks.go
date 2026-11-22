package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/stacks"
)

// stackLockPath is where the daemon keeps the lock of the installed stacks:
// stacks.lock.json in the stacks' state directory under the host's cache
// directory.
func stackLockPath() string {
	cache := config.DefaultCacheDir
	if nix, err := loadNix(); err == nil {
		cache = nix.Paths.Cache()
	}
	return filepath.Join(stacks.StateDirIn(cache), "stacks.lock.json")
}

func printStackList(c *cli.Context, in *[]stacks.Stack) error {
	list := []stacks.Stack{}
	if in != nil {
		list = *in
	}
	if jsonFlag(c) {
		return printJSON(list)
	}
	if len(list) == 0 {
		fmt.Println("No stacks installed.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STACK\tSTATE\tRUNS\tAPPROVED\tFETCHED\tWAITING\tSOURCE")
	for _, s := range list {
		approved := "-"
		if s.Approved != nil {
			approved = shortRev(s.Approved.Rev)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, stackState(s), stackRuns(s), approved, shortRev(s.Fetched.Rev), stackWaiting(s), s.Source)
	}
	return w.Flush()
}

// `homelab stacks` — service stacks: which are installed, the commit each
// is pinned to, the grants the operator approved, and running them.

func stacksCmd() *cli.Command {
	return &cli.Command{
		Name:  "stacks",
		Usage: "Install service stacks from their repos, approve what they ask of the host and run them",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List installed stacks with their approved and fetched commits",
				Action: func(c *cli.Context) error {
					l, err := call[[]stacks.Stack](c.Context, http.MethodGet, "/stacks", nil)
					if err != nil {
						return err
					}
					return printStackList(c, l)
				},
			},
			{
				Name:  "check",
				Usage: "Read every stack's repo for new commits now (the daemon also does it every 6 hours)",
				Action: func(c *cli.Context) error {
					l, err := call[[]stacks.Stack](c.Context, http.MethodPost, "/stacks-check", nil)
					if err != nil {
						return err
					}
					return printStackList(c, l)
				},
			},
			{
				Name:  "lock",
				Usage: "Print the lock of the installed stacks (the daemon keeps it in the stacks' state directory)",
				Action: func(c *cli.Context) error {
					l, err := call[stacks.StackLock](c.Context, http.MethodGet, "/stacks-lock", nil)
					if err != nil {
						return err
					}
					return printJSON(l)
				},
			},
			{
				Name:      "restore",
				Usage:     "Reinstall the stacks a lock lists that are not installed, and start those that were running",
				ArgsUsage: "[lock file]  (default: the daemon's stacks.lock.json)",
				Action: func(c *cli.Context) error {
					path := stackLockPath()
					if c.NArg() > 0 {
						path = c.Args().First()
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					var l stacks.StackLock
					if err := json.Unmarshal(data, &l); err != nil {
						return fmt.Errorf("%s: %w", path, err)
					}
					r, err := call[stacks.StackRestoreResult](c.Context, http.MethodPost, "/stacks-lock/restore", l)
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(r)
					}
					for _, n := range r.Restored {
						fmt.Printf("✔ %s restored\n", n)
					}
					for n, why := range r.Skipped {
						fmt.Printf("✗ %s: %s\n", n, why)
					}
					return nil
				},
			},
			{
				Name:      "show",
				Usage:     "Show one stack: its commits and what its manifest asks for",
				ArgsUsage: "<name>",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab stacks show <name>"); err != nil {
						return err
					}
					s, err := getStack(c, c.Args().First())
					if err != nil {
						return err
					}
					return printStack(c, s)
				},
			},
			{
				Name:      "add",
				Usage:     "Install a stack from its repo; nothing is approved, built or started",
				ArgsUsage: "[--name <name>] <source>  (github:owner/repo[/branch], git+ssh://…, git+https://…)",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Usage: "install under this name instead of the one the stack's manifest suggests; fixed once installed"},
				},
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab stacks add [--name <name>] <source>"); err != nil {
						return err
					}
					s, err := call[stacks.Stack](c.Context, http.MethodPost, "/stacks",
						stacks.StackAdd{Source: c.Args().First(), Name: c.String("name")})
					if err != nil {
						return err
					}
					return printStack(c, *s)
				},
			},
			stacksBuildCommand(),
			{
				Name:      "nixpkgs",
				Usage:     "Choose the nixpkgs a version 7 stack is built with (nixos-unstable, nixos-YY.MM, a commit, or recommended); the next update applies it",
				ArgsUsage: "[--allow-downgrade] <name> <channel|commit|recommended>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "allow-downgrade", Usage: "accept a nixpkgs older than the one the stack runs (it can break a stack whose data a newer version upgraded)"},
				},
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 2, "homelab stacks nixpkgs [--allow-downgrade] <name> <channel|commit|recommended>"); err != nil {
						return err
					}
					spec := c.Args().Get(1)
					if spec == "recommended" {
						spec = ""
					}
					s, err := call[stacks.Stack](c.Context, http.MethodPatch, stackPath(c.Args().First(), ""),
						stacks.StackPatch{Nixpkgs: &spec, AllowDowngrade: c.Bool("allow-downgrade")})
					if err != nil {
						return err
					}
					return printStack(c, *s)
				},
			},
			{
				Name:      "label",
				Usage:     "Set the name the dashboard shows for a stack; an empty label shows its name again",
				ArgsUsage: "<name> <label>",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 2, "homelab stacks label <name> <label>"); err != nil {
						return err
					}
					label := c.Args().Get(1)
					s, err := call[stacks.Stack](c.Context, http.MethodPatch, stackPath(c.Args().First(), ""),
						stacks.StackPatch{Label: &label})
					if err != nil {
						return err
					}
					return printStack(c, *s)
				},
			},
			{
				Name:      "fetch",
				Usage:     "Read the newest commit from the stack's repo; new grants wait for approval",
				ArgsUsage: "<name>",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab stacks fetch <name>"); err != nil {
						return err
					}
					s, err := call[stacks.Stack](c.Context, http.MethodPost, stackPath(c.Args().First(), "/fetch"), nil)
					if err != nil {
						return err
					}
					return printStack(c, *s)
				},
			},
			{
				Name:      "approve",
				Usage:     "Approve the fetched commit and every grant its manifest asks for",
				ArgsUsage: "<name> <rev>  (the fetched commit `show` printed, at least 7 characters)",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 2, "homelab stacks approve <name> <rev>"); err != nil {
						return err
					}
					name, rev := c.Args().Get(0), c.Args().Get(1)
					if len(rev) < 7 {
						return errors.New("give at least 7 characters of the commit")
					}
					s, err := getStack(c, name)
					if err != nil {
						return err
					}
					// A prefix of the fetched commit is completed; anything
					// else goes to the daemon as typed and is refused there.
					if strings.HasPrefix(s.Fetched.Rev, rev) {
						rev = s.Fetched.Rev
					}
					approved, err := call[stacks.Stack](c.Context, http.MethodPost, stackPath(name, "/approve"), stacks.StackApprove{Rev: rev})
					if err != nil {
						return err
					}
					return emit(c, approved, fmt.Sprintf("✔ %s approved at %s.", name, shortRev(rev)))
				},
			},
			{
				Name:      "remove",
				Usage:     "Forget a stopped stack, its approvals and its built generations; its state directory stays",
				ArgsUsage: "<name>",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab stacks remove <name>"); err != nil {
						return err
					}
					name := c.Args().First()
					if err := send(c.Context, http.MethodDelete, stackPath(name, ""), nil); err != nil {
						return err
					}
					return emit(c, nil, fmt.Sprintf("✔ %s removed.", name))
				},
			},
			stackOpCmd("start", "Start a stack, building its approved commit first when nothing is built", nil),
			stackOpCmd("stop", "Stop a stack and remove its routes and network rules", nil),
			stackFreezeCmd("freeze", "Freeze a running stack: its processes stop where they are and keep their memory until it is thawed; a reboot loses it"),
			stackFreezeCmd("thaw", "Thaw a frozen stack: its processes carry on where they stopped"),
			{
				Name:      "autostart",
				Usage:     "Set whether a stack starts after the host boots",
				ArgsUsage: "<name> on|off",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 2, "homelab stacks autostart <name> on|off"); err != nil {
						return err
					}
					var on bool
					switch c.Args().Get(1) {
					case "on":
						on = true
					case "off":
					default:
						return fmt.Errorf("autostart is on or off, not %q", c.Args().Get(1))
					}
					s, err := call[stacks.Stack](c.Context, http.MethodPatch, stackPath(c.Args().First(), ""),
						stacks.StackPatch{Autostart: &on})
					if err != nil {
						return err
					}
					return printStack(c, *s)
				},
			},
			stackOpCmd("update", "Build the approved commit and switch the stack to it; an unhealthy result is rolled back", nil),
			withFlags(stackOpCmd("rollback", "Switch a stack back to an earlier generation (the one before the current one by default)",
				func(c *cli.Context) any {
					if g := c.Int("generation"); g != 0 {
						return stacks.StackRollback{Generation: g}
					}
					return nil
				}), &cli.IntFlag{Name: "generation", Usage: "the generation to go back to"}),
			stackVarCmd(),
			stackFolderCmd(),
			stackIDsCmd(),
		},
	}
}

// stackVarCmd is `homelab stacks var`: the values a stack gets at
// /run/secrets. A value is read from stdin or a prompt, never argv.
// stackFolderCmd makes and re-owns the folder of a stack's path variable.
func stackFolderCmd() *cli.Command {
	return &cli.Command{
		Name:  "folder",
		Usage: "Bind more host folders into a stack; make the folder of a path variable, or change its owner to the stack ids",
		Subcommands: []*cli.Command{
			{
				Name:      "list",
				Usage:     "List the folders you bind into a stack on top of its manifest",
				ArgsUsage: "<stack>",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab stacks folder list <stack>"); err != nil {
						return err
					}
					s, err := getStack(c, c.Args().First())
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(s.Folders)
					}
					printStackFolders(s)
					return nil
				},
			},
			{
				Name:      "add",
				Usage:     "Bind a host folder into a stack at a mount, read-only unless --read-write; the stack sees it at its next restart",
				ArgsUsage: "<stack> <host-folder> <mount>",
				Flags:     []cli.Flag{&cli.BoolFlag{Name: "read-write", Usage: "let the stack write to the folder"}},
				Action: func(c *cli.Context) error {
					args, flags := argsAndBools(c, "read-write")
					if len(args) < 3 {
						return fmt.Errorf("usage: homelab stacks folder add <stack> <host-folder> <mount> [--read-write]")
					}
					name := args[0]
					cur, err := getStack(c, name)
					if err != nil {
						return err
					}
					in := foldersIn(cur, "")
					in = append(in, stacks.StackFolderIn{Path: args[1], Mount: args[2], ReadOnly: !flags["read-write"]})
					return setStackFolders(c, name, in, fmt.Sprintf("✔ %s is bound at %s in %s.", args[1], args[2], name))
				},
			},
			{
				Name:      "rm",
				Usage:     "Stop binding the folder at a mount; the stack loses it at its next restart, and the folder stays",
				ArgsUsage: "<stack> <mount>",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 2, "homelab stacks folder rm <stack> <mount>"); err != nil {
						return err
					}
					name, mount := c.Args().Get(0), c.Args().Get(1)
					cur, err := getStack(c, name)
					if err != nil {
						return err
					}
					in := foldersIn(cur, mount)
					if len(in) == len(cur.Folders) {
						return fmt.Errorf("%s binds no added folder at %s", name, mount)
					}
					return setStackFolders(c, name, in, fmt.Sprintf("✔ %s no longer binds a folder at %s.", name, mount))
				},
			},
			{
				Name:      "create",
				Usage:     "Make the folder (a subvolume on a pool), owned by the stack ids, and set it as the value",
				ArgsUsage: "<stack> <NAME>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "place", Usage: "the pool or partition, by its directory; needed when the host has more than one"},
					&cli.StringFlag{Name: "name", Usage: "the folder's name there (default: the stack's name)"},
				},
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 2, "homelab stacks folder create <stack> <NAME> [--place DIR] [--name FOLDER]"); err != nil {
						return err
					}
					name, v := c.Args().Get(0), c.Args().Get(1)
					body := stacks.StackFolderCreate{Place: c.String("place"), Folder: c.String("name")}
					s, err := call[stacks.Stack](c.Context, http.MethodPost, stackVarPath(name, v)+"/folder", body)
					if err != nil {
						return err
					}
					return emit(c, s, "✔ "+v+" is "+stackVarValue(s, v))
				},
			},
			{
				Name:      "own",
				Usage:     "Change the owner of the folder and everything in it to the stack ids; cannot be undone",
				ArgsUsage: "<stack> <NAME>",
				Flags:     []cli.Flag{&cli.StringFlag{Name: "confirm", Usage: "the folder's path, to confirm", Required: true}},
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 2, "homelab stacks folder own <stack> <NAME> --confirm <path>"); err != nil {
						return err
					}
					name, v := c.Args().Get(0), c.Args().Get(1)
					s, err := call[stacks.Stack](c.Context, http.MethodPost, stackVarPath(name, v)+"/own", stacks.StackFolderOwn{Confirm: c.String("confirm")})
					if err != nil {
						return err
					}
					return emit(c, s, "✔ "+stackVarValue(s, v)+" and everything in it has the stack ids")
				},
			},
		},
	}
}

// foldersIn is the stack's added folders as a request, without the one at
// mount drop.
func foldersIn(s stacks.Stack, drop string) []stacks.StackFolderIn {
	out := []stacks.StackFolderIn{}
	for _, f := range s.Folders {
		if f.Mount != drop {
			out = append(out, stacks.StackFolderIn{Path: f.Path, Mount: f.Mount, ReadOnly: f.ReadOnly})
		}
	}
	return out
}

func setStackFolders(c *cli.Context, name string, in []stacks.StackFolderIn, msg string) error {
	s, err := call[stacks.Stack](c.Context, http.MethodPut, stackPath(name, "/folders"), stacks.StackFoldersSet{Folders: in})
	if err != nil {
		return err
	}
	if s != nil && s.RestartNeeded {
		msg += fmt.Sprintf(" Restart the stack to apply it: homelab stacks stop %s && homelab stacks start %s", name, name)
	}
	return emit(c, s, msg)
}

// printStackFolders prints a stack's added folders as a table.
func printStackFolders(s stacks.Stack) {
	if len(s.Folders) == 0 {
		fmt.Println("No added folders.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MOUNT\tFOLDER\tMODE\tNOTES")
	for _, f := range s.Folders {
		mode := "read-write"
		if f.ReadOnly {
			mode = "read-only"
		}
		notes := ""
		if f.ShadowedBy != "" {
			notes = "not bound: overlaps " + f.ShadowedBy
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.Mount, f.Path, mode, notes)
	}
	_ = w.Flush()
}

func stackVarValue(s *stacks.Stack, name string) string {
	for _, v := range s.Variables {
		if v.Name == name {
			return v.Value
		}
	}
	return name
}

// stackIDsCmd lists and pins the host's ids for the names stacks declare.
func stackIDsCmd() *cli.Command {
	list := func(c *cli.Context, l *stacks.StackIDList) error {
		if jsonFlag(c) {
			return printJSON(l)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tID")
		for _, e := range l.IDs {
			fmt.Fprintf(w, "%s\t%d\n", e.Name, e.ID)
		}
		fmt.Fprintf(w, "(%d of %d to %d in use)\n", len(l.IDs), l.Min, l.Max)
		return w.Flush()
	}
	return &cli.Command{
		Name:  "ids",
		Usage: "This host's ids for the users and groups its stacks declare (2000 to 2099)",
		Action: func(c *cli.Context) error {
			l, err := call[stacks.StackIDList](c.Context, http.MethodGet, "/stacks-ids", nil)
			if err != nil {
				return err
			}
			return list(c, l)
		},
		Subcommands: []*cli.Command{{
			Name:      "set",
			Usage:     "Pin a name's id; folders made before keep the id they have",
			ArgsUsage: "<name> <id>",
			Action: func(c *cli.Context) error {
				if err := requireArgs(c, 2, "homelab stacks ids set <name> <id>"); err != nil {
					return err
				}
				id, err := strconv.ParseInt(c.Args().Get(1), 10, 64)
				if err != nil {
					return fmt.Errorf("%q is not a number", c.Args().Get(1))
				}
				l, err := call[stacks.StackIDList](c.Context, http.MethodPut, "/stacks-ids/"+url.PathEscape(c.Args().Get(0)), stacks.StackIDSet{ID: id})
				if err != nil {
					return err
				}
				return list(c, l)
			},
		}},
	}
}

func stackVarCmd() *cli.Command {
	return &cli.Command{
		Name:  "var",
		Usage: "Set, remove and list a stack's variables (the env, secret and path values its manifest declares, and your own)",
		Subcommands: []*cli.Command{
			{
				Name:      "list",
				Usage:     "List a stack's variables; secret values are never shown",
				ArgsUsage: "<stack>",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab stacks var list <stack>"); err != nil {
						return err
					}
					s, err := getStack(c, c.Args().First())
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(s.Variables)
					}
					printStackVars(s)
					return nil
				},
			},
			{
				Name:      "set",
				Usage:     "Set a variable; the stack sees it at once, a service on its next restart; a path (an existing host directory) on the stack's next restart",
				ArgsUsage: "<stack> <NAME>",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "secret", Usage: "make the variable write-only (declared secrets always are; declared env and path ones cannot be)"},
					&cli.BoolFlag{Name: "value-stdin", Usage: "read the value from stdin"},
					&cli.BoolFlag{Name: "read-only", Usage: "bind a path read-only although the manifest asks for read-write; without a new value it keeps the folder"},
					&cli.BoolFlag{Name: "writable", Usage: "bind a path as its manifest says again"},
				},
				Action: func(c *cli.Context) error {
					args, flags := argsAndBools(c, "secret", "value-stdin", "read-only", "writable")
					if len(args) < 2 {
						return fmt.Errorf("usage: homelab stacks var set <stack> <NAME> [--secret] [--read-only|--writable] --value-stdin")
					}
					name, v := args[0], args[1]
					if flags["read-only"] && flags["writable"] {
						return fmt.Errorf("give --read-only or --writable, not both")
					}
					mode := flags["read-only"] || flags["writable"]
					var val string
					var err error
					if mode && !flags["value-stdin"] {
						// Only the mode changes: keep the folder.
						s, err := getStack(c, name)
						if err != nil {
							return err
						}
						if val = stackVarValue(&s, v); val == v {
							return fmt.Errorf("%s has no folder set; give one with --value-stdin", v)
						}
					} else if val, err = readSecretFor(name+" "+v, flags["value-stdin"]); err != nil {
						return err
					}
					body := stacks.StackVariableSet{Value: val}
					if flags["secret"] {
						t := true
						body.Secret = &t
					}
					if mode {
						ro := flags["read-only"]
						body.ReadOnly = &ro
					}
					s, err := call[stacks.Stack](c.Context, http.MethodPut, stackVarPath(name, v), body)
					if err != nil {
						return err
					}
					msg := fmt.Sprintf("✔ %s set on %s; restart the services that use it.", v, name)
					if s != nil && s.RestartNeeded {
						msg = fmt.Sprintf("✔ %s set on %s; restart the stack to apply it: homelab stacks stop %s && homelab stacks start %s", v, name, name, name)
					}
					return emit(c, s, msg)
				},
			},
			{
				Name:      "rm",
				Usage:     "Remove a variable, declared or not",
				ArgsUsage: "<stack> <NAME>",
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 2, "homelab stacks var rm <stack> <NAME>"); err != nil {
						return err
					}
					name, v := c.Args().Get(0), c.Args().Get(1)
					s, err := call[stacks.Stack](c.Context, http.MethodDelete, stackVarPath(name, v), nil)
					if err != nil {
						return err
					}
					return emit(c, s, fmt.Sprintf("✔ %s removed from %s.", v, name))
				},
			},
		},
	}
}

func stackVarPath(name, v string) string {
	return stackPath(name, "/variables/"+url.PathEscape(v))
}

// printStackVars prints a stack's variables as a table. A secret shows
// only whether it is set; a value from the manifest's default is marked so.
func printStackVars(s stacks.Stack) {
	if len(s.Variables) == 0 {
		fmt.Println("No variables.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tREQUIRED\tVALUE\tNOTES")
	for _, v := range s.Variables {
		required := "no"
		switch {
		case !v.Declared:
			required = "-"
		case v.Required:
			required = "yes"
		}
		value := "(not set)"
		switch {
		case v.Secret && v.Set:
			value = "(set)"
		case v.Set:
			value = shownValue(v.Value, v.InEnv || v.Kind == stacks.StackVariableKindPath)
		case v.Source == stacks.StackVariableSourceDefault && v.Default != nil:
			value = shownValue(*v.Default, v.InEnv || v.Kind == stacks.StackVariableKindPath) + " (default)"
		}
		var notes []string
		if !v.Declared {
			notes = append(notes, "not declared")
		}
		if v.Kind == stacks.StackVariableKindPath {
			mode := "read-write"
			if v.ReadOnly != nil && *v.ReadOnly {
				mode = "read-only"
			}
			if v.ReadOnlyByOperator {
				mode = "read-only (set by you)"
			}
			notes = append(notes, "at "+v.Mount+", "+mode)
		} else if v.Source != stacks.StackVariableSourceUnset && !v.InEnv {
			notes = append(notes, "file only")
		}
		if v.Description != "" {
			notes = append(notes, v.Description)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Kind, required, value, strings.Join(notes, "; "))
	}
	_ = w.Flush()
}

// shownValue is a plain value as the table prints it: quoted when it is not
// one safe line, and cut at 40 characters.
func shownValue(value string, plain bool) string {
	if !plain || len(value) > 40 {
		value = strconv.Quote(value)
		if r := []rune(value); len(r) > 40 {
			value = string(r[:37]) + "…"
		}
	}
	return value
}

// stackOpPoll is how often a running operation is read; tests shorten it.
var stackOpPoll = 2 * time.Second

// stackOpCmd is one of start, stop, update and rollback. The daemon runs
// them in the background, so the command starts one and follows it to its
// end, printing each phase. It fails when the operation does. body, when
// not nil, gives the request body.
func stackOpCmd(verb, usage string, body func(*cli.Context) any) *cli.Command {
	return &cli.Command{
		Name:      verb,
		Usage:     usage,
		ArgsUsage: "<name>",
		Action: func(c *cli.Context) error {
			if err := requireArgs(c, 1, "homelab stacks "+verb+" <name>"); err != nil {
				return err
			}
			name := c.Args().First()
			var in any
			if body != nil {
				in = body(c)
			}
			s, err := call[stacks.Stack](c.Context, http.MethodPost, stackPath(name, "/"+verb), in)
			if err != nil {
				return err
			}
			if s == nil || s.Operation == nil {
				return errors.New("the daemon did not report the operation it started")
			}
			started, phase := s.Operation.StartedAt, ""
			for {
				op := s.Operation
				if op == nil || !op.StartedAt.Equal(started) {
					return fmt.Errorf("lost track of the %s: the daemon restarted or another operation replaced it", verb)
				}
				if op.Phase != phase && !jsonFlag(c) {
					phase = op.Phase
					fmt.Fprintf(os.Stderr, "  %s\n", phase)
				}
				if op.FinishedAt != nil {
					break
				}
				time.Sleep(stackOpPoll)
				next, err := getStack(c, name)
				if err != nil {
					return err
				}
				s = &next
			}
			if jsonFlag(c) {
				if err := printJSON(s); err != nil {
					return err
				}
			}
			if e := s.Operation.Error; e != "" {
				return fmt.Errorf("%s %s: %s", verb, name, e)
			}
			if !jsonFlag(c) {
				fmt.Printf("✔ %s: %s.\n", name, s.Operation.Phase)
			}
			return nil
		},
	}
}

// stackFreezeCmd freezes or thaws a stack. Both answer at once.
func stackFreezeCmd(verb, usage string) *cli.Command {
	return &cli.Command{
		Name:      verb,
		Usage:     usage,
		ArgsUsage: "<name>",
		Action: func(c *cli.Context) error {
			if err := requireArgs(c, 1, "homelab stacks "+verb+" <name>"); err != nil {
				return err
			}
			s, err := call[stacks.Stack](c.Context, http.MethodPost, stackPath(c.Args().First(), "/"+verb), nil)
			if err != nil {
				return err
			}
			return emit(c, s, fmt.Sprintf("✔ %s is %s.", s.Name, stackState(*s)))
		},
	}
}

func stackState(s stacks.Stack) string {
	if s.Frozen {
		return "frozen"
	}
	if s.Running {
		return "running"
	}
	return "stopped"
}

// stackRuns is the commit of the generation in use.
func stackRuns(s stacks.Stack) string {
	for _, g := range s.Generations {
		if g.Id == s.Generation {
			return shortRev(g.Rev)
		}
	}
	return "-"
}

func getStack(c *cli.Context, name string) (stacks.Stack, error) {
	s, err := call[stacks.Stack](c.Context, http.MethodGet, stackPath(name, ""), nil)
	if err != nil {
		return stacks.Stack{}, err
	}
	return *s, nil
}

// stackPath is the path of a stack, or of one of its actions (suffix
// "/start" and so on).
func stackPath(name, suffix string) string {
	return "/stacks/" + url.PathEscape(name) + suffix
}

func shortRev(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// stackWaiting says what the fetched commit is waiting for, if anything.
func stackWaiting(s stacks.Stack) string {
	switch {
	case s.Approved != nil && s.Approved.Rev == s.Fetched.Rev:
		return "-"
	case s.Approved == nil:
		return "first approval"
	default:
		return fmt.Sprintf("%d new grant(s)", len(s.NewGrants))
	}
}

func printStack(c *cli.Context, s stacks.Stack) error {
	if jsonFlag(c) {
		return printJSON(s)
	}
	fmt.Printf("Stack:    %s\n", s.Name)
	if s.Label != "" {
		fmt.Printf("Label:    %s\n", s.Label)
	}
	fmt.Printf("Source:   %s\n", s.Source)
	if s.Approved != nil {
		fmt.Printf("Approved: %s\n", s.Approved.Rev)
	} else {
		fmt.Println("Approved: nothing yet")
	}
	fmt.Printf("Fetched:  %s\n", s.Fetched.Rev)
	if s.NixpkgsRev != "" {
		spec := s.Nixpkgs
		if spec == "" && s.Approved != nil {
			spec = s.Approved.Manifest.Nixpkgs + " (recommended)"
		}
		fmt.Printf("Nixpkgs:  %s, %s\n", spec, s.NixpkgsRev)
	}
	fmt.Printf("State:    %s\n", stackState(s))
	if s.Autostart {
		fmt.Println("At boot:  starts")
	} else {
		fmt.Println("At boot:  stays stopped")
	}
	if s.Address != "" {
		fmt.Printf("Address:  %s\n", s.Address)
	}
	if s.Approved != nil && len(s.Approved.Manifest.Api) > 0 {
		fmt.Printf("API:      %s (on %s port %d)\n", strings.Join(s.Approved.Manifest.Api, ", "), stackHostAddr(s.Address), stacks.APIPort)
	} else if s.Approved != nil {
		fmt.Println("API:      none")
	}
	if len(s.Variables) > 0 {
		fmt.Println("\nVariables:")
		printStackVars(s)
		if !s.VariablesReady {
			fmt.Printf("Fill in the missing values with: homelab stacks var set %s <NAME> --value-stdin\n", s.Name)
		}
	}
	if s.RestartNeeded {
		fmt.Printf("\nRestart needed: the container runs with other paths than its values give now. Apply them with: homelab stacks stop %s && homelab stacks start %s\n", s.Name, s.Name)
	}
	if len(s.Generations) > 0 {
		fmt.Println("\nGenerations:")
		for _, g := range s.Generations {
			mark := ""
			switch {
			case g.Id == s.Generation:
				mark = "  (current)"
			case g.Failed:
				mark = "  (failed)"
			}
			fmt.Printf("  %-4d %s  built %s%s\n", g.Id, shortRev(g.Rev), g.BuiltAt.Local().Format("2006-01-02 15:04"), mark)
		}
	}
	if op := s.Operation; op != nil {
		switch {
		case op.FinishedAt == nil:
			fmt.Printf("\nRunning %s: %s\n", op.Kind, op.Phase)
		case op.Error != "":
			fmt.Printf("\nLast %s failed: %s\n", op.Kind, op.Error)
		}
	}
	if s.Approved != nil && s.Approved.Rev == s.Fetched.Rev {
		return nil
	}
	if len(s.NewGrants) == 0 {
		fmt.Println("\nThe fetched commit asks for nothing that needs approval.")
	} else {
		fmt.Println("\nThe fetched commit asks for:")
		for _, g := range s.NewGrants {
			fmt.Printf("  %-8s %s\n", g.Kind, g.Detail)
		}
	}
	fmt.Printf("\nApprove with: homelab stacks approve %s %s\n", s.Name, shortRev(s.Fetched.Rev))
	return nil
}

// stackHostAddr is the host's address on the stack's network (.1), where
// the stack reaches the stack listener.
func stackHostAddr(stackAddr string) string {
	if i := strings.LastIndexByte(stackAddr, '.'); i > 0 {
		return stackAddr[:i] + ".1"
	}
	return "the host"
}

// withFlags adds flags to a command.
func withFlags(c *cli.Command, flags ...cli.Flag) *cli.Command {
	c.Flags = append(c.Flags, flags...)
	return c
}
