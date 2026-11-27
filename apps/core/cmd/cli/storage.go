package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/storage"
)

func storageCmd() *cli.Command {
	cmd := storageBaseCmd()
	cmd.Subcommands = append(cmd.Subcommands, poolCmds()...)
	return cmd
}

func storageBaseCmd() *cli.Command {
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
			{
				Name:      "subvolume-profile",
				Usage:     "Change placement profile or copy count on a bcachefs subvolume",
				ArgsUsage: "<path>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "profile", Usage: "placement on a tiered pool: hot, cached or cold"},
					&cli.IntFlag{Name: "copies", Value: -1, Usage: "copies kept for this subvolume; 0 resets to pool default"},
				},
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab storage subvolume-profile <path>"); err != nil {
						return err
					}
					profile := c.String("profile")
					hasCopies := c.IsSet("copies")
					if profile == "" && !hasCopies {
						return errors.New("set --profile or --copies")
					}
					p := c.Args().First()
					req := storage.SubvolumeProfileRequest{Path: p, Profile: profile}
					if hasCopies {
						copies := c.Int("copies")
						req.Copies = &copies
					}
					if err := send(c.Context, http.MethodPatch, "/storage/subvolume", req); err != nil {
						return err
					}
					return emit(c, nil, "Updated subvolume "+p)
				},
			},
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
			{Name: "partitions", Usage: "List the partitions that are mounted here or can be", Action: func(c *cli.Context) error {
				st, err := call[storage.StorageStatus](c.Context, http.MethodGet, "/storage", nil)
				if err != nil {
					return err
				}
				if jsonFlag(c) {
					return printJSON(st.Partitions)
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
				fmt.Fprintln(w, "DEVICE\tFILE SYSTEM\tLABEL\tSTATE\tMOUNT\tUUID")
				for _, p := range st.Partitions {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.Path, p.Fstype, p.Label, p.State, p.Mountdir, p.Uuid)
				}
				return w.Flush()
			}},
			{
				Name: "partition-mount", Usage: "Mount a partition", ArgsUsage: "<device|uuid> [name]",
				Description: "It mounts at " + storage.MountRoot + "/<name>; with no name, under its label.",
				Flags:       []cli.Flag{&cli.BoolFlag{Name: "read-only", Usage: "mount it read-only"}},
				Action: func(c *cli.Context) error {
					if err := requireArgs(c, 1, "homelab storage partition-mount <device|uuid> [name]"); err != nil {
						return err
					}
					p, err := findPartition(c, c.Args().First())
					if err != nil {
						return err
					}
					req := storage.PartitionMountRequest{Name: c.Args().Get(1), ReadOnly: c.Bool("read-only")}
					if err := send(c.Context, http.MethodPost, "/storage/partitions/"+url.PathEscape(p.Uuid)+"/mount", req); err != nil {
						return err
					}
					if req.Name == "" {
						req.Name = p.MountName
					}
					return emit(c, nil, "Mounted "+storage.Mountpoint(req.Name))
				},
			},
			{Name: "partition-unmount", Usage: "Unmount a partition", ArgsUsage: "<device|uuid|mountpoint>", Action: func(c *cli.Context) error {
				if err := requireArgs(c, 1, "homelab storage partition-unmount <device|uuid|mountpoint>"); err != nil {
					return err
				}
				p, err := findPartition(c, c.Args().First())
				if err != nil {
					return err
				}
				if err := send(c.Context, http.MethodPost, "/storage/partitions/"+url.PathEscape(p.Uuid)+"/unmount", nil); err != nil {
					return err
				}
				return emit(c, nil, "Unmounted "+p.Mountdir)
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

// findPartition is the listed partition with that device path, UUID or
// mountpoint.
func findPartition(c *cli.Context, ref string) (storage.Partition, error) {
	st, err := call[storage.StorageStatus](c.Context, http.MethodGet, "/storage", nil)
	if err != nil {
		return storage.Partition{}, err
	}
	for _, p := range st.Partitions {
		if ref == p.Path || ref == p.Uuid || (p.Mountdir != "" && ref == p.Mountdir) {
			return p, nil
		}
	}
	return storage.Partition{}, fmt.Errorf("no partition %s: see homelab storage partitions", ref)
}

func printStorageStatus(c *cli.Context, st *storage.StorageStatus) error {
	if jsonFlag(c) {
		return printJSON(st)
	}
	if st == nil {
		st = &storage.StorageStatus{}
	}
	if len(st.Pools) == 0 {
		fmt.Println("No storage pools found.")
	}
	defer printSpareDisks(st)
	if len(st.Pools) == 0 {
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
	if err := w.Flush(); err != nil {
		return err
	}
	for _, p := range st.Pools {
		fmt.Printf("\n%s: %d copies\n", p.Name, max(p.DataReplicas, 1))
		d := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(d, "  DISK\tKIND\tUSED\tSIZE\tUSE%\tSTATE\tMODEL")
		for _, disk := range p.Disks {
			dUsed, dPct := "-", "-"
			if disk.Usage != nil {
				dUsed = fmtStorageBytes(uint64(max(disk.Usage.UsedBytes, 0)))
				dPct = fmt.Sprintf("%d%%", disk.Usage.UsedPercent)
			}
			fmt.Fprintf(d, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n", disk.Path, disk.Kind, dUsed, fmtStorageBytes(uint64(disk.Size)), dPct, disk.State, disk.FriendlyName)
		}
		if err := d.Flush(); err != nil {
			return err
		}
		if p.Usage != nil {
			if p.Usage.CachedBytes > 0 {
				fmt.Printf("  cache: %s cached on SSD\n", fmtStorageBytes(uint64(p.Usage.CachedBytes)))
			}
			if p.Usage.ReconcileDataBytes > 0 || p.Usage.ReconcileMetadataBytes > 0 {
				var parts []string
				if p.Usage.ReconcileDataBytes > 0 {
					parts = append(parts, fmtStorageBytes(uint64(p.Usage.ReconcileDataBytes))+" data")
				}
				if p.Usage.ReconcileMetadataBytes > 0 {
					meta := fmtStorageBytes(uint64(p.Usage.ReconcileMetadataBytes)) + " metadata"
					ssdCount := 0
					for _, disk := range p.Disks {
						if disk.Kind == "ssd" {
							ssdCount++
						}
					}
					if p.MetadataTarget == "ssd" && p.MetadataReplicas > ssdCount {
						meta += fmt.Sprintf(" (%d copies on %d SSD)", p.MetadataReplicas, ssdCount)
					}
					parts = append(parts, meta)
				}
				fmt.Printf("  pending reconcile: %s\n", strings.Join(parts, ", "))
			}
		}
		if op := p.Operation; op != nil {
			line := fmt.Sprintf("  removing %s: %s", op.Disk, op.Phase)
			if op.Phase == "moving" {
				line += fmt.Sprintf(", %s of %s left", fmtStorageBytes(uint64(max(op.LeftBytes, 0))), fmtStorageBytes(uint64(max(op.TotalBytes, 0))))
			}
			if op.Error != "" {
				line += ": " + op.Error
			}
			fmt.Println(line)
		}
	}
	return nil
}

// printSpareDisks lists the disks a pool can be made from or grown with.
func printSpareDisks(st *storage.StorageStatus) {
	if len(st.Unassigned) == 0 {
		return
	}
	fmt.Println("\nSpare disks")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "  DISK\tKIND\tSIZE\tCONTENTS\tMODEL")
	for _, d := range st.Unassigned {
		contents := d.Contents
		if contents == "" {
			contents = "blank"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", d.Path, d.Kind, fmtStorageBytes(uint64(d.Size)), contents, d.FriendlyName)
	}
	_ = w.Flush()
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
