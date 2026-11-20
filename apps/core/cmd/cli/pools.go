package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/storage"
)

// poolOpPoll is how often a running removal is read; tests shorten it.
var poolOpPoll = 2 * time.Second

// findPool is the pool with that name, mountpoint or UUID.
func findPool(c *cli.Context, ref string) (storage.Pool, error) {
	st, err := call[storage.StorageStatus](c.Context, http.MethodGet, "/storage", nil)
	if err != nil {
		return storage.Pool{}, err
	}
	for _, p := range st.Pools {
		if ref == p.Name || ref == p.Uuid || (p.Mountdir != "" && ref == p.Mountdir) {
			return p, nil
		}
	}
	return storage.Pool{}, fmt.Errorf("no pool %s: see homelab storage status", ref)
}

func poolPath(p storage.Pool, rest string) string {
	return "/storage/pools/" + url.PathEscape(p.Uuid) + rest
}

// poolCmds make, grow, shrink and delete a pool.
func poolCmds() []*cli.Command {
	erase := &cli.BoolFlag{Name: "erase", Usage: "erase a disk that has partitions or a file system on it"}
	return []*cli.Command{
		{
			Name: "pool-create", Usage: "Make a pool from spare disks and mount it", ArgsUsage: "<name> <disk> [disk...]",
			Description: "The pool mounts at " + storage.MountRoot + "/<name>. `homelab storage status` lists the spare disks.\n" +
				"With SSDs and HDDs together, the SSDs take new writes and the data moves to the HDDs.",
			Flags: []cli.Flag{
				&cli.IntFlag{Name: "copies", Value: 1, Usage: "how many disks hold each file, 1 to 3"},
				erase,
			},
			Action: func(c *cli.Context) error {
				if err := requireArgs(c, 2, "homelab storage pool-create <name> <disk> [disk...]"); err != nil {
					return err
				}
				req := storage.PoolCreateRequest{Name: c.Args().First(), Disks: c.Args().Slice()[1:], Copies: c.Int("copies")}
				if c.Bool("erase") {
					req.Confirm = req.Name
				}
				p, err := call[storage.Pool](c.Context, http.MethodPost, "/storage/pools", req)
				if err != nil {
					return err
				}
				return emit(c, p, fmt.Sprintf("Created %s at %s", req.Name, storage.Mountpoint(req.Name)))
			},
		},
		{
			Name: "pool-add-disk", Usage: "Add a spare disk to a mounted pool", ArgsUsage: "<pool> <disk>",
			Flags: []cli.Flag{erase},
			Action: func(c *cli.Context) error {
				if err := requireArgs(c, 2, "homelab storage pool-add-disk <pool> <disk>"); err != nil {
					return err
				}
				p, err := findPool(c, c.Args().First())
				if err != nil {
					return err
				}
				req := storage.PoolDiskRequest{Disk: c.Args().Get(1)}
				if c.Bool("erase") {
					req.Confirm = p.Name
				}
				if err := send(c.Context, http.MethodPost, poolPath(p, "/disks"), req); err != nil {
					return err
				}
				return emit(c, nil, fmt.Sprintf("Added %s to %s", req.Disk, p.Name))
			},
		},
		{
			Name: "pool-remove-disk", Usage: "Move a disk's data to the pool's other disks and take it out", ArgsUsage: "<pool> <disk>",
			Description: "This can take hours. The command follows the removal to its end; it goes on in the daemon\n" +
				"when the command is stopped. `homelab storage pool-cancel` stops the removal.",
			Flags: []cli.Flag{&cli.BoolFlag{Name: "no-wait", Usage: "start the removal and return"}},
			Action: func(c *cli.Context) error {
				if err := requireArgs(c, 2, "homelab storage pool-remove-disk <pool> <disk>"); err != nil {
					return err
				}
				p, err := findPool(c, c.Args().First())
				if err != nil {
					return err
				}
				disk := c.Args().Get(1)
				started, err := call[storage.Pool](c.Context, http.MethodDelete, poolPath(p, "/disks?disk="+url.QueryEscape(disk)), nil)
				if err != nil {
					return err
				}
				if started == nil || started.Operation == nil {
					return errors.New("the daemon did not report the removal it started")
				}
				if c.Bool("no-wait") {
					return emit(c, started, fmt.Sprintf("Removing %s from %s; see homelab storage status", disk, p.Name))
				}
				return followRemoval(c, *started)
			},
		},
		{
			Name: "pool-cancel", Usage: "Stop the removal of a disk; the disk stays in the pool", ArgsUsage: "<pool>",
			Action: func(c *cli.Context) error {
				if err := requireArgs(c, 1, "homelab storage pool-cancel <pool>"); err != nil {
					return err
				}
				p, err := findPool(c, c.Args().First())
				if err != nil {
					return err
				}
				if err := send(c.Context, http.MethodDelete, poolPath(p, "/operation"), nil); err != nil {
					return err
				}
				return emit(c, nil, "Stopped the removal on "+p.Name)
			},
		},
		{
			Name: "pool-delete", Usage: "Unmount a pool, erase every disk in it and forget it", ArgsUsage: "<pool>",
			Description: "Refused while a stack or a backup keeps a folder on the pool.",
			Flags:       []cli.Flag{&cli.StringFlag{Name: "confirm", Usage: "the pool's name again"}},
			Action: func(c *cli.Context) error {
				if err := requireArgs(c, 1, "homelab storage pool-delete <pool> --confirm <pool>"); err != nil {
					return err
				}
				p, err := findPool(c, c.Args().First())
				if err != nil {
					return err
				}
				if err := send(c.Context, http.MethodDelete, poolPath(p, "?confirm="+url.QueryEscape(c.String("confirm"))), nil); err != nil {
					return err
				}
				return emit(c, nil, fmt.Sprintf("Deleted %s; its %d disks are spare", p.Name, len(p.Disks)))
			},
		},
	}
}

// followRemoval prints a removal's phases and how much is left, until it
// ends. It fails when the removal does.
func followRemoval(c *cli.Context, p storage.Pool) error {
	started, phase, left := p.Operation.StartedAt, "", int64(-1)
	for {
		op := p.Operation
		if op == nil || !op.StartedAt.Equal(started) {
			return errors.New("lost track of the removal: it was stopped, or the daemon restarted")
		}
		if !jsonFlag(c) && (op.Phase != phase || (op.Phase == "moving" && op.LeftBytes != left)) {
			phase, left = op.Phase, op.LeftBytes
			if phase == "moving" {
				fmt.Fprintf(os.Stderr, "  moving: %s of %s left\n", fmtStorageBytes(uint64(max(left, 0))), fmtStorageBytes(uint64(max(op.TotalBytes, 0))))
			} else {
				fmt.Fprintf(os.Stderr, "  %s\n", phase)
			}
		}
		if op.FinishedAt != nil {
			break
		}
		time.Sleep(poolOpPoll)
		next, err := findPool(c, p.Uuid)
		if err != nil {
			return err
		}
		p = next
	}
	if jsonFlag(c) {
		if err := printJSON(p); err != nil {
			return err
		}
	}
	if e := p.Operation.Error; e != "" {
		return fmt.Errorf("remove %s: %s", p.Operation.Disk, e)
	}
	if !jsonFlag(c) {
		fmt.Printf("✔ %s left %s and is spare.\n", p.Operation.Disk, p.Name)
	}
	return nil
}
