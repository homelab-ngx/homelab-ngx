package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/backups"
)

// backupID resolves a backup given by name or id.
func backupID(c *cli.Context, ref string) (int64, error) {
	list, err := call[[]backups.Backup](c.Context, http.MethodGet, "/backups", nil)
	if err != nil {
		return 0, err
	}
	for _, b := range *list {
		if b.Name == ref || strconv.FormatInt(b.Id, 10) == ref {
			return b.Id, nil
		}
	}
	return 0, fmt.Errorf("no backup named %q (see: homelab backup status)", ref)
}

// destinationByRef resolves a destination given by name or id.
func destinationByRef(c *cli.Context, ref string) (backups.Destination, error) {
	list, err := call[[]backups.Destination](c.Context, http.MethodGet, "/backup-destinations", nil)
	if err != nil {
		return backups.Destination{}, err
	}
	for _, d := range *list {
		if d.Name == ref || strconv.FormatInt(d.Id, 10) == ref {
			return d, nil
		}
	}
	return backups.Destination{}, fmt.Errorf("no destination named %q (see: homelab backup dest list)", ref)
}

func backupPath(id int64, suffix string) string { return fmt.Sprintf("/backups/%d%s", id, suffix) }
func destPath(id int64, suffix string) string {
	return fmt.Sprintf("/backup-destinations/%d%s", id, suffix)
}

func backupCmd() *cli.Command {
	// one resolves the first argument to a backup and calls fn with its id.
	one := func(usage string, fn func(c *cli.Context, id int64, ref string) error) cli.ActionFunc {
		return func(c *cli.Context) error {
			if err := requireArgs(c, 1, usage); err != nil {
				return err
			}
			id, err := backupID(c, c.Args().First())
			if err != nil {
				return err
			}
			return fn(c, id, c.Args().First())
		}
	}
	enabledAction := func(enabled bool) cli.ActionFunc {
		verb := "Enabled"
		if !enabled {
			verb = "Disabled"
		}
		return one("homelab backup "+strings.ToLower(verb[:len(verb)-1])+" <backup>", func(c *cli.Context, id int64, ref string) error {
			b, err := call[backups.Backup](c.Context, http.MethodPatch, backupPath(id, ""), backups.BackupPatch{Enabled: &enabled})
			if err != nil {
				return err
			}
			return emit(c, b, verb+" backup "+ref)
		})
	}
	return &cli.Command{
		Name:  "backup",
		Usage: "Run and inspect backups and their destinations",
		Subcommands: []*cli.Command{
			{
				Name:  "status",
				Usage: "List the backups with their last and next run",
				Action: func(c *cli.Context) error {
					list, err := call[[]backups.Backup](c.Context, http.MethodGet, "/backups", nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) || len(*list) == 0 {
						return printJSON(list)
					}
					w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
					fmt.Fprintln(w, "NAME\tENABLED\tSCHEDULE\tLAST RESULT\tLAST RUN\tNEXT RUN")
					for _, b := range *list {
						result, last := "-", "-"
						if b.LastRun != nil {
							result, last = string(b.LastRun.Result), b.LastRun.StartedAt.Local().Format(time.RFC3339)
						}
						if b.Overdue {
							result += " (overdue)"
						}
						fmt.Fprintf(w, "%s\t%v\t%s\t%s\t%s\t%s\n", b.Name, b.Enabled, dash(b.Schedule), result, last, fmtTime(b.NextRun))
					}
					return w.Flush()
				},
			},
			{Name: "show", Usage: "Show one backup: sources, destinations, settings", ArgsUsage: "<backup>",
				Action: one("homelab backup show <backup>", func(c *cli.Context, id int64, _ string) error {
					b, err := call[backups.Backup](c.Context, http.MethodGet, backupPath(id, ""), nil)
					if err != nil {
						return err
					}
					return printJSON(b)
				})},
			{Name: "run", Usage: "Start a backup now", ArgsUsage: "<backup>",
				Action: one("homelab backup run <backup>", func(c *cli.Context, id int64, ref string) error {
					started, err := call[backups.BackupRunStarted](c.Context, http.MethodPost, backupPath(id, "/run"), nil)
					if err != nil {
						return err
					}
					return emit(c, started, fmt.Sprintf("Started backup %s (run %d). Follow it: homelab backup run-show %d --watch",
						ref, started.RunId, started.RunId))
				})},
			{Name: "cancel", Usage: "Stop a backup's run or preview; files already copied stay at the destination", ArgsUsage: "<backup>",
				Action: one("homelab backup cancel <backup>", func(c *cli.Context, id int64, ref string) error {
					if err := send(c.Context, http.MethodPost, backupPath(id, "/cancel"), nil); err != nil {
						return err
					}
					return emit(c, map[string]any{"cancelled": ref}, "Stopped backup "+ref)
				})},
			{Name: "preview", Usage: "Compare a backup with its destinations and copy nothing (S3 and rclone only)", ArgsUsage: "<backup>",
				Action: one("homelab backup preview <backup>", func(c *cli.Context, id int64, ref string) error {
					if err := send(c.Context, http.MethodPost, backupPath(id, "/preview"), nil); err != nil {
						return err
					}
					return emit(c, map[string]any{"previewing": ref},
						fmt.Sprintf("Started a preview of %s. Follow it: homelab backup preview-show %s --watch", ref, ref))
				})},
			{
				Name: "preview-show", Usage: "Show a backup's newest preview: what a run would do per folder", ArgsUsage: "<backup>",
				Flags: []cli.Flag{&cli.BoolFlag{Name: "watch", Usage: "follow the preview until it ends"}},
				Action: func(c *cli.Context) error {
					args, flags := argsAndBools(c, "watch")
					if len(args) < 1 {
						return fmt.Errorf("usage: homelab backup preview-show <backup> [--watch]")
					}
					id, err := backupID(c, args[0])
					if err != nil {
						return err
					}
					for {
						p, err := call[backups.BackupPreview](c.Context, http.MethodGet, backupPath(id, "/preview"), nil)
						if err != nil {
							return err
						}
						if !flags["watch"] || p.Result != backups.BackupRunResultRunning {
							if jsonFlag(c) {
								return printJSON(p)
							}
							return printPreview(args[0], *p)
						}
						if !jsonFlag(c) {
							fmt.Fprintln(os.Stdout, previewLine(*p))
						}
						select {
						case <-c.Context.Done():
							return c.Context.Err()
						case <-time.After(3 * time.Second):
						}
					}
				},
			},
			{Name: "enable", Usage: "Enable a backup", ArgsUsage: "<backup>", Action: enabledAction(true)},
			{Name: "disable", Usage: "Disable a backup", ArgsUsage: "<backup>", Action: enabledAction(false)},
			{Name: "logs", Usage: "Show the recent journal lines of a backup's runs", ArgsUsage: "<backup>",
				Action: one("homelab backup logs <backup>", func(c *cli.Context, id int64, _ string) error {
					logs, err := call[backups.Logs](c.Context, http.MethodGet, backupPath(id, "/logs"), nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(logs)
					}
					fmt.Fprintln(os.Stdout, logs.Logs)
					return nil
				})},
			{Name: "runs", Usage: "List a backup's runs, newest first", ArgsUsage: "<backup>",
				Flags: []cli.Flag{&cli.IntFlag{Name: "limit", Value: 20, Usage: "how many runs"}},
				Action: one("homelab backup runs <backup>", func(c *cli.Context, id int64, _ string) error {
					runs, err := call[[]backups.BackupRun](c.Context, http.MethodGet,
						backupPath(id, "/runs?limit="+strconv.Itoa(c.Int("limit"))), nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) || len(*runs) == 0 {
						return printJSON(runs)
					}
					w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
					fmt.Fprintln(w, "RUN\tSTARTED\tTOOK\tRESULT\tFILES\tSIZE\tERROR")
					for _, r := range *runs {
						fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%s\t%s\n", r.Id, r.StartedAt.Local().Format(time.RFC3339),
							took(r.StartedAt, r.FinishedAt), r.Result, r.Files, fmtBytes(r.Bytes), r.Error)
					}
					return w.Flush()
				})},
			{
				Name: "run-show", Usage: "Show one run: its result per destination and folder", ArgsUsage: "<run>",
				Flags: []cli.Flag{&cli.BoolFlag{Name: "watch", Usage: "follow the run until it ends"}},
				Action: func(c *cli.Context) error {
					args, flags := argsAndBools(c, "watch")
					if len(args) < 1 {
						return fmt.Errorf("usage: homelab backup run-show <run> [--watch]")
					}
					runID, err := strconv.ParseInt(args[0], 10, 64)
					if err != nil {
						return fmt.Errorf("a run is a number (see: homelab backup runs <backup>)")
					}
					for {
						d, err := call[backups.BackupRunDetail](c.Context, http.MethodGet, fmt.Sprintf("/backup-runs/%d", runID), nil)
						if err != nil {
							return err
						}
						running := d.Result == backups.BackupRunResultRunning
						if !flags["watch"] || !running {
							if jsonFlag(c) {
								return printJSON(d)
							}
							return printRun(*d)
						}
						if !jsonFlag(c) {
							fmt.Fprintln(os.Stdout, progressLine(*d))
						}
						select {
						case <-c.Context.Done():
							return c.Context.Err()
						case <-time.After(3 * time.Second):
						}
					}
				},
			},
			{
				Name:  "coverage",
				Usage: "List the folders the stacks recommend and the backups that include them",
				Action: func(c *cli.Context) error {
					cov, err := call[backups.Coverage](c.Context, http.MethodGet, "/backup-coverage", nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) || len(cov.Stacks) == 0 {
						return printJSON(cov)
					}
					w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
					fmt.Fprintln(w, "STACK\tFOLDER\tPATH\tBACKED UP BY")
					for _, st := range cov.Stacks {
						for _, f := range st.Folders {
							by := "not backed up"
							if len(f.CoveredBy) > 0 {
								by = strings.Join(f.CoveredBy, ", ")
							}
							fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", st.Stack, f.Label, dash(f.HostPath), by)
						}
					}
					return w.Flush()
				},
			},
			destCmd(),
		},
	}
}

func destCmd() *cli.Command {
	settings := []cli.Flag{
		&cli.StringFlag{Name: "name", Usage: "rename the destination"},
		&cli.StringFlag{Name: "endpoint", Usage: "s3: the provider's endpoint URL"},
		&cli.StringFlag{Name: "region", Usage: "s3: region"},
		&cli.StringFlag{Name: "bucket", Usage: "s3: bucket"},
		&cli.StringFlag{Name: "prefix", Usage: "s3: folder inside the bucket"},
		&cli.StringFlag{Name: "access-key-id", Usage: "s3: access key id"},
		&cli.StringFlag{Name: "path", Usage: "rclone: bucket or folder on the remote"},
		&cli.StringFlag{Name: "repository", Usage: "restic: the repository"},
		&cli.BoolFlag{Name: "delete-removed", Usage: "s3, rclone: also remove files that are gone on the host"},
		&cli.IntFlag{Name: "keep-daily", Usage: "restic: daily snapshots to keep"},
		&cli.IntFlag{Name: "keep-weekly", Usage: "restic: weekly snapshots to keep"},
		&cli.IntFlag{Name: "keep-monthly", Usage: "restic: monthly snapshots to keep"},
		&cli.IntFlag{Name: "keep-yearly", Usage: "restic: yearly snapshots to keep"},
		&cli.BoolFlag{Name: "value-stdin", Usage: "read the secret from stdin: s3 the secret access key; rclone the remote's config section; " +
			"restic KEY=value lines of backend credentials (a RESTIC_PASSWORD line adds an existing repository)"},
	}
	// overlay applies the flags that were given onto in.
	overlay := func(c *cli.Context, in *backups.DestinationInput) error {
		if c.IsSet("name") {
			in.Name = c.String("name")
		}
		if c.IsSet("delete-removed") {
			in.DeleteRemoved = c.Bool("delete-removed")
		}
		for flag, dst := range map[string]*int{
			"keep-daily": &in.Keep.Daily, "keep-weekly": &in.Keep.Weekly,
			"keep-monthly": &in.Keep.Monthly, "keep-yearly": &in.Keep.Yearly,
		} {
			if c.IsSet(flag) {
				*dst = c.Int(flag)
			}
		}
		secret := ""
		if c.Bool("value-stdin") {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return err
			}
			secret = string(b)
		}
		switch in.Kind {
		case backups.KindS3:
			if in.S3 == nil {
				in.S3 = &backups.S3Input{}
			}
			for flag, dst := range map[string]*string{
				"endpoint": &in.S3.Endpoint, "region": &in.S3.Region, "bucket": &in.S3.Bucket,
				"prefix": &in.S3.Prefix, "access-key-id": &in.S3.AccessKeyId,
			} {
				if c.IsSet(flag) {
					*dst = c.String(flag)
				}
			}
			in.S3.SecretAccessKey = strings.TrimSpace(secret)
		case backups.KindRclone:
			if in.Rclone == nil {
				in.Rclone = &backups.RcloneInput{}
			}
			if c.IsSet("path") {
				in.Rclone.Path = c.String("path")
			}
			in.Rclone.Config = secret
		case backups.KindRestic:
			if in.Restic == nil {
				in.Restic = &backups.ResticInput{}
			}
			if c.IsSet("repository") {
				in.Restic.Repository = c.String("repository")
			}
			if secret != "" {
				env := map[string]string{}
				sc := bufio.NewScanner(strings.NewReader(secret))
				for sc.Scan() {
					line := strings.TrimSpace(sc.Text())
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					k, v, ok := strings.Cut(line, "=")
					if !ok {
						return fmt.Errorf("stdin: expected KEY=value lines")
					}
					if k == "RESTIC_PASSWORD" {
						in.Restic.Password = v
						continue
					}
					env[k] = v
				}
				in.Restic.Env = &env
			}
		default:
			return fmt.Errorf("--kind is s3, rclone or restic")
		}
		return nil
	}
	one := func(usage string, fn func(c *cli.Context, d backups.Destination) error) cli.ActionFunc {
		return func(c *cli.Context) error {
			if err := requireArgs(c, 1, usage); err != nil {
				return err
			}
			d, err := destinationByRef(c, c.Args().First())
			if err != nil {
				return err
			}
			return fn(c, d)
		}
	}
	return &cli.Command{
		Name:  "dest",
		Usage: "Manage backup destinations",
		Subcommands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List the destinations",
				Action: func(c *cli.Context) error {
					list, err := call[[]backups.Destination](c.Context, http.MethodGet, "/backup-destinations", nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) || len(*list) == 0 {
						return printJSON(list)
					}
					w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
					fmt.Fprintln(w, "NAME\tKIND\tWHERE\tUSED BY")
					for _, d := range *list {
						fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Name, d.Kind, destWhere(d), dash(strings.Join(d.UsedBy, ", ")))
					}
					return w.Flush()
				},
			},
			{
				Name: "add", Usage: "Add a destination (flags go before the name)", ArgsUsage: "<name>",
				Flags: append([]cli.Flag{&cli.StringFlag{Name: "kind", Usage: "s3, rclone or restic", Required: true}}, settings[1:]...),
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab backup dest add --kind <kind> [flags] <name>"); err != nil {
						return err
					}
					in := backups.DestinationInput{Name: c.Args().First(), Kind: backups.DestinationKind(c.String("kind"))}
					if err := overlay(c, &in); err != nil {
						return err
					}
					created, err := call[backups.DestinationCreated](c.Context, http.MethodPost, "/backup-destinations", in)
					if err != nil {
						return err
					}
					human := "Added destination " + in.Name
					if created.GeneratedPassword != "" {
						human += "\n\nRepository password (shown once, save it somewhere off this host):\n\n  " + created.GeneratedPassword +
							"\n\nThen confirm it: homelab backup dest confirm " + in.Name
					}
					return emit(c, created, human)
				},
			},
			{
				Name: "set", Usage: "Change a destination's settings; what is not given stays (flags go before the name)", ArgsUsage: "<destination>",
				Flags: settings,
				Action: one("homelab backup dest set [flags] <destination>", func(c *cli.Context, d backups.Destination) error {
					in := backups.DestinationInput{Name: d.Name, Kind: d.Kind, DeleteRemoved: d.DeleteRemoved, Keep: d.Keep}
					if d.S3 != nil {
						in.S3 = &backups.S3Input{Endpoint: d.S3.Endpoint, Region: d.S3.Region, Bucket: d.S3.Bucket,
							Prefix: d.S3.Prefix, AccessKeyId: d.S3.AccessKeyId}
					}
					if d.Rclone != nil {
						in.Rclone = &backups.RcloneInput{Path: d.Rclone.Path}
					}
					if d.Restic != nil {
						in.Restic = &backups.ResticInput{Repository: d.Restic.Repository}
					}
					if err := overlay(c, &in); err != nil {
						return err
					}
					if in.Restic != nil && in.Restic.Password != "" {
						return fmt.Errorf("a repository's password cannot be changed; leave RESTIC_PASSWORD out")
					}
					out, err := call[backups.Destination](c.Context, http.MethodPut, destPath(d.Id, ""), in)
					if err != nil {
						return err
					}
					return emit(c, out, "Updated destination "+out.Name)
				}),
			},
			{Name: "rm", Usage: "Remove a destination no backup uses", ArgsUsage: "<destination>",
				Action: one("homelab backup dest rm <destination>", func(c *cli.Context, d backups.Destination) error {
					if err := send(c.Context, http.MethodDelete, destPath(d.Id, ""), nil); err != nil {
						return err
					}
					return emit(c, nil, "Removed destination "+d.Name)
				})},
			{Name: "test", Usage: "Try a destination without copying anything", ArgsUsage: "<destination>",
				Action: one("homelab backup dest test <destination>", func(c *cli.Context, d backups.Destination) error {
					res, err := call[backups.DestinationTest](c.Context, http.MethodPost, destPath(d.Id, "/test"), nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(res)
					}
					if !res.Ok {
						return fmt.Errorf("%s did not answer: %s", d.Name, res.Detail)
					}
					fmt.Fprintln(os.Stdout, res.Detail)
					return nil
				})},
			{Name: "confirm", Usage: "Confirm a restic repository's password is saved, so runs may use it", ArgsUsage: "<destination>",
				Action: one("homelab backup dest confirm <destination>", func(c *cli.Context, d backups.Destination) error {
					out, err := call[backups.Destination](c.Context, http.MethodPost, destPath(d.Id, "/confirm-password"), nil)
					if err != nil {
						return err
					}
					return emit(c, out, "Confirmed. Backups can now use "+d.Name)
				})},
			{Name: "reveal", Usage: "Print a restic repository's password", ArgsUsage: "<destination>",
				Action: one("homelab backup dest reveal <destination>", func(c *cli.Context, d backups.Destination) error {
					out, err := call[backups.DestinationPassword](c.Context, http.MethodPost, destPath(d.Id, "/reveal-password"),
						backups.RevealRequest{Confirm: d.Name})
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(out)
					}
					fmt.Fprintln(os.Stdout, out.Password)
					return nil
				})},
		},
	}
}

func destWhere(d backups.Destination) string {
	switch {
	case d.S3 != nil:
		return strings.TrimSuffix(d.S3.Bucket+"/"+d.S3.Prefix, "/") + " at " + d.S3.Endpoint
	case d.Rclone != nil:
		return d.Rclone.Remote + ":" + d.Rclone.Path + " (" + d.Rclone.Type + ")"
	case d.Restic != nil:
		return d.Restic.Repository
	}
	return "-"
}

// printRun prints a run's items as a table.
func printRun(d backups.BackupRunDetail) error {
	fmt.Fprintf(os.Stdout, "Run %d of %s: %s", d.Id, d.BackupName, d.Result)
	if d.Error != "" {
		fmt.Fprintf(os.Stdout, " (%s)", d.Error)
	}
	fmt.Fprintf(os.Stdout, "\nStarted %s, took %s, %d files, %s\n\n", d.StartedAt.Local().Format(time.RFC3339),
		took(d.StartedAt, d.FinishedAt), d.Files, fmtBytes(d.Bytes))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "DESTINATION\tFOLDER\tRESULT\tFILES\tSIZE\tTOOK\tDETAIL")
	for _, it := range d.Items {
		label := it.Label
		if it.Kind == "destination" {
			label = "(destination)"
		}
		detail := it.Error
		if detail == "" {
			detail = it.Note
		}
		t := "-"
		if it.StartedAt != nil {
			t = took(*it.StartedAt, it.FinishedAt)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", it.Destination, label, it.Result, it.Files, fmtBytes(it.Bytes), t, detail)
	}
	return w.Flush()
}

// printPreview prints what a run would do, one row per folder and
// destination, then the first paths of each kind.
func printPreview(name string, p backups.BackupPreview) error {
	fmt.Fprintf(os.Stdout, "Preview of %s: %s", name, p.Result)
	if p.Error != "" {
		fmt.Fprintf(os.Stdout, " (%s)", p.Error)
	}
	fmt.Fprintf(os.Stdout, "\nStarted %s, took %s. Nothing was copied or deleted.\n\n",
		p.StartedAt.Local().Format(time.RFC3339), took(p.StartedAt, p.FinishedAt))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "DESTINATION\tFOLDER\tRESULT\tNEW\tCHANGED\tUNCHANGED\tONLY AT DESTINATION\tDETAIL")
	for _, it := range p.Items {
		detail := it.Error
		if detail == "" {
			detail = it.Note
		}
		if it.Kind == "destination" {
			fmt.Fprintf(w, "%s\t(destination)\t%s\t\t\t\t\t%s\n", it.Destination, it.Result, detail)
			continue
		}
		extra := fmt.Sprintf("%d kept", it.Extra)
		if it.Deletes {
			extra = fmt.Sprintf("%d deleted", it.Extra)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d (%s)\t%d (%s)\t%d\t%s\t%s\n", it.Destination, it.Label, it.Result,
			it.New, fmtBytes(it.NewBytes), it.Changed, fmtBytes(it.ChangedBytes), it.Unchanged, extra, detail)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, it := range p.Items {
		for _, s := range []struct {
			what  string
			n     int64
			paths []string
		}{{"new", it.New, it.NewSample}, {"changed", it.Changed, it.ChangedSample}, {"only at the destination", it.Extra, it.ExtraSample}} {
			if len(s.paths) == 0 {
				continue
			}
			fmt.Fprintf(os.Stdout, "\n%s -> %s, %s (%d of %d):\n", it.Label, it.Destination, s.what, len(s.paths), s.n)
			for _, path := range s.paths {
				fmt.Fprintln(os.Stdout, "  "+path)
			}
		}
	}
	return nil
}

// previewLine is one line about a preview in flight.
func previewLine(p backups.BackupPreview) string {
	if p.Progress == nil {
		return "getting ready..."
	}
	return fmt.Sprintf("%s -> %s: %d files compared", p.Progress.Folder, p.Progress.Destination, p.Progress.FilesDone)
}

// progressLine is one line about a run in flight.
func progressLine(d backups.BackupRunDetail) string {
	p := d.Progress
	if p == nil {
		return "preparing..."
	}
	line := fmt.Sprintf("%s -> %s: %s", p.Folder, p.Destination, fmtBytes(p.BytesDone))
	if p.BytesTotal > 0 {
		line += fmt.Sprintf(" of %s (%d%%)", fmtBytes(p.BytesTotal), p.BytesDone*100/p.BytesTotal)
	}
	if p.Speed > 0 {
		line += fmt.Sprintf(", %s/s", fmtBytes(int64(p.Speed)))
	}
	return line
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func took(start time.Time, end *time.Time) string {
	if end == nil {
		return "running"
	}
	return end.Sub(start).Round(time.Second).String()
}

func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format(time.RFC3339)
}
