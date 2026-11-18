package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// An install into free space fills the free run the user picked. systemd-
// repart places new partitions first-fit into every free run on the disk,
// so a small gap elsewhere (before another system's partitions) would take
// some of them. The other runs are held by temporary partitions while
// repart runs: on a copy of the partition table for the plan, on the disk
// itself for the install, deleted right after.

// reservedName names the temporary partitions; no definition matches
// their type, so repart leaves them alone.
const (
	reservedName = "homelab-reserved"
	reservedType = "6A2D9F6C-1B8E-4C3A-9F47-5F2B8D0E7A11"
)

// reservations are sfdisk lines for every free run of d but the one that
// starts at region, in sectors of ss bytes. Runs too small to hold an
// aligned sector are left out: repart cannot use them either.
func reservations(d InstallDisk, region, ss uint64) []string {
	var out []string
	for _, f := range d.Free {
		if f.Start == region || f.Size < mib {
			continue
		}
		out = append(out, fmt.Sprintf("start=%d, size=%d, type=%s, name=%s", f.Start/ss, f.Size/ss, reservedType, reservedName))
	}
	return out
}

// regionOK reports whether region is the start of one of d's free runs.
func regionOK(d InstallDisk, region uint64) bool {
	for _, f := range d.Free {
		if f.Start == region {
			return true
		}
	}
	return false
}

// partPath is a disk's partition n: nvme0n1 → nvme0n1p3, sda → sda3.
func partPath(disk string, n int) string {
	if disk != "" && disk[len(disk)-1] >= '0' && disk[len(disk)-1] <= '9' {
		return fmt.Sprintf("%sp%d", disk, n)
	}
	return fmt.Sprintf("%s%d", disk, n)
}

// planOnDisk turns a plan made on the copy (nodes file1, file2…) into the
// disk's partitions and drops the reservations.
func planOnDisk(p InstallPlan, file, disk string) InstallPlan {
	rows := []InstallPlanRow{}
	for _, r := range p.Rows {
		if r.Label == reservedName {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(r.Node, file)); err == nil && strings.HasPrefix(r.Node, file) {
			r.Node = partPath(disk, n)
		}
		rows = append(rows, r)
	}
	p.Rows = rows
	return p
}

// copyTable is the disk's partition table as sfdisk input for an image
// file, the partitions keeping their numbers.
func copyTable(dump, disk, file string) string {
	var out []string
	for _, l := range strings.Split(dump, "\n") {
		switch {
		case strings.HasPrefix(l, "device:"):
			continue
		case strings.HasPrefix(l, disk):
			// "/dev/nvme0n1p3 : start=…" → "<file>3 : start=…"
			dev, rest, ok := strings.Cut(l, ":")
			dev = strings.TrimSpace(dev)
			num := strings.TrimLeft(strings.TrimPrefix(dev, disk), "p")
			if !ok || num == "" {
				continue
			}
			l = file + num + " :" + rest
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func (s *Service) sectorSize(ctx context.Context, dev string) (uint64, error) {
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "blockdev", Args: []string{"--getss", dev}})
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(res.Stdout), 10, 64)
}

// planOnCopy runs repart's dry run on a sparse copy of d's partition table
// with every free run but region reserved.
func (s *Service) planOnCopy(ctx context.Context, d InstallDisk, defs string, region uint64) (InstallPlan, error) {
	ss, err := s.sectorSize(ctx, d.Path)
	if err != nil {
		return InstallPlan{}, err
	}
	dump, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "sfdisk", Args: []string{"--dump", d.Path}})
	if err != nil {
		return InstallPlan{}, fmt.Errorf("the partition table: %w", err)
	}
	dir := filepath.Join(filepath.Dir(mountDir), "plan")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return InstallPlan{}, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "disk")
	f, err := os.Create(file)
	if err != nil {
		return InstallPlan{}, err
	}
	err = f.Truncate(int64(d.Size))
	f.Close()
	if err != nil {
		return InstallPlan{}, err
	}
	script := copyTable(dump.Stdout, d.Path, file) + "\n" + strings.Join(reservations(d, region, ss), "\n") + "\n"
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "sfdisk", Args: []string{"-q", "--no-reread", "--no-tell-kernel", file}, Stdin: strings.NewReader(script)}); err != nil {
		return InstallPlan{}, fmt.Errorf("copying the partition table: %w", err)
	}
	res, _ := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "installer", Name: "systemd-repart",
		Args: []string{"--definitions=" + defs, "--dry-run=yes", "--empty=allow", "--json=short", file},
	})
	return planOnDisk(parsePlan(res.Stdout, res.Output), file, d.Path), nil
}

// reserve holds every free run of d but region with temporary partitions;
// release deletes them again.
func (s *Service) reserve(ctx context.Context, d InstallDisk, region uint64) (release func() error, err error) {
	none := func() error { return nil }
	ss, err := s.sectorSize(ctx, d.Path)
	if err != nil {
		return none, err
	}
	lines := reservations(d, region, ss)
	if len(lines) == 0 {
		return none, nil
	}
	s.line(fmt.Sprintf("Holding %d other free region(s) of %s while partitioning", len(lines), d.Path))
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "sfdisk", Args: []string{"-q", "--append", "--no-reread", "--no-tell-kernel", d.Path},
		Stdin: strings.NewReader(strings.Join(lines, "\n") + "\n")}); err != nil {
		return none, fmt.Errorf("holding the other free regions: %w", err)
	}
	return func() error { return s.unreserve(ctx, d.Path) }, nil
}

// unreserve deletes the temporary partitions from dev.
func (s *Service) unreserve(ctx context.Context, dev string) error {
	res, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "sfdisk", Args: []string{"--dump", dev}})
	if err != nil {
		return err
	}
	var nums []string
	for _, l := range strings.Split(res.Stdout, "\n") {
		if !strings.Contains(l, "name=\""+reservedName+"\"") && !strings.Contains(l, "name="+reservedName) {
			continue
		}
		d, _, _ := strings.Cut(l, ":")
		if n := strings.TrimLeft(strings.TrimPrefix(strings.TrimSpace(d), dev), "p"); n != "" {
			nums = append(nums, n)
		}
	}
	if len(nums) == 0 {
		return nil
	}
	if _, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "installer", Name: "sfdisk", Args: append([]string{"-q", "--delete", dev}, nums...)}); err != nil {
		return errors.New("deleting the temporary partitions " + strings.Join(nums, ", ") + " of " + dev + ": " + err.Error())
	}
	return nil
}
