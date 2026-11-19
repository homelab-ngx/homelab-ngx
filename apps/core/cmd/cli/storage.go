package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/storage"
)

func storageCmd() *cli.Command {
	return &cli.Command{
		Name:  "storage",
		Usage: "Show bcachefs pool status",
		Subcommands: []*cli.Command{
			{Name: "mount", Usage: "Mount a storage pool", ArgsUsage: "<name> <device> [device...]", Description: "The pool mounts at " + storage.MountRoot + "/<name>.", Action: func(c *cli.Context) error {
				if err := requireArgs(c, 2, "homelab storage mount <name> <device> [device...]"); err != nil {
					return err
				}
				name := c.Args().Get(0)
				if err := send(c.Context, http.MethodPost, "/storage/mount", storage.MountRequest{Name: name, Devices: c.Args().Slice()[1:]}); err != nil {
					return err
				}
				return emit(c, nil, "Mounted "+storage.Mountpoint(name))
			}},
			{Name: "unmount", Usage: "Safely unmount a storage pool", ArgsUsage: "<mountpoint>", Action: func(c *cli.Context) error {
				if err := requireArgs(c, 1, "homelab storage unmount <mountpoint>"); err != nil {
					return err
				}
				mp := c.Args().First()
				if err := send(c.Context, http.MethodPost, "/storage/unmount", storage.UnmountRequest{Mountpoint: mp}); err != nil {
					return err
				}
				return emit(c, nil, "Unmounted "+mp)
			}},
			{Name: "usage", Usage: "Show subvolume usage", ArgsUsage: "<path> [path...]", Action: func(c *cli.Context) error {
				if err := requireArgs(c, 1, "homelab storage usage <path> [path...]"); err != nil {
					return err
				}
				u, err := call[storage.SubvolumeUsage](c.Context, http.MethodPost, "/storage/subvolume-usage", storage.SubvolumeUsageRequest{Paths: c.Args().Slice()})
				if err != nil {
					return err
				}
				return printJSON(u)
			}},
			{Name: "subvolume-create", Usage: "Create a bcachefs subvolume", ArgsUsage: "<path>", Action: func(c *cli.Context) error {
				if err := requireArgs(c, 1, "homelab storage subvolume-create <path>"); err != nil {
					return err
				}
				p := c.Args().First()
				if err := send(c.Context, http.MethodPost, "/storage/subvolume", storage.SubvolumeRequest{Path: p}); err != nil {
					return err
				}
				return emit(c, nil, "Created subvolume "+p)
			}},
			{Name: "subvolume-delete", Usage: "Delete an empty bcachefs subvolume", ArgsUsage: "<path>", Action: func(c *cli.Context) error {
				if err := requireArgs(c, 1, "homelab storage subvolume-delete <path>"); err != nil {
					return err
				}
				p := c.Args().First()
				if err := send(c.Context, http.MethodDelete, "/storage/subvolume?path="+url.QueryEscape(p), nil); err != nil {
					return err
				}
				return emit(c, nil, "Deleted subvolume "+p)
			}},
			{
				Name:  "status",
				Usage: "Show status of all storage pools",
				Action: func(c *cli.Context) error {
					st, err := call[storage.StorageStatus](c.Context, http.MethodGet, "/storage", nil)
					if err != nil {
						return err
					}
					return printStorageStatus(c, st)
				},
			},
		},
	}
}

func printStorageStatus(c *cli.Context, st *storage.StorageStatus) error {
	if jsonFlag(c) {
		return printJSON(st)
	}
	if st == nil || len(st.Pools) == 0 {
		fmt.Println("No storage pools found.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tMOUNT\tSTATE\tUSED\tTOTAL\tUSED%")
	for _, p := range st.Pools {
		name := p.Name
		if name == "" {
			if len(p.Uuid) >= 8 {
				name = "pool-" + p.Uuid[:8]
			} else {
				name = p.Uuid
			}
		}
		used, total, pct := "-", "-", "-"
		if p.Usage != nil {
			used = fmtStorageBytes(uint64(p.Usage.UsedBytes))
			total = fmtStorageBytes(uint64(p.Usage.TotalBytes))
			pct = fmt.Sprintf("%.1f%%", p.Usage.UsedPercent)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", name, p.Mountdir, p.State, used, total, pct)
	}
	return w.Flush()
}

func fmtStorageBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
